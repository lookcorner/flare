package daemon

import (
	"context"
	"flare/internal/cfapi"
	"flare/internal/download"
	"flare/internal/netutil"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// diagnoseTimeout：单个网络探测最长等多久。
// 诊断回答的是"现在通不通"，不是"等到通为止"——卡住不动的诊断没人愿意用
const diagnoseTimeout = 5 * time.Second

// apiAddr：Cloudflare API 入口。第一层可达性只用 TCP 拨号测，
// 不带凭据、也不会因为令牌错就误报成"网络不通"
const apiAddr = "api.cloudflare.com:443"

// DiagnoseResult 诊断总结果
type DiagnoseResult struct {
	Cloudflared CloudflaredCheck `json:"cloudflared"`
	API         APICheck         `json:"api"`
	Routes      []RouteDiagnose  `json:"routes"`
	Total       int              `json:"total"`
	Passed      int              `json:"passed"`
	Failed      int              `json:"failed"`
}

// CloudflaredCheck cloudflared 二进制和进程检测
type CloudflaredCheck struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
}

// APICheck Cloudflare API 连通性与凭据状态。
// 分成两层看：Reachable 只说明"网络能到 api.cloudflare.com"；
// Authed 才说明"令牌真能用"（要走一次 cfapi.ListZones）。
// 不分开的话，令牌填错会被报成"API 不可达"，用户会去白查网络
type APICheck struct {
	Reachable bool   `json:"reachable"`
	Authed    bool   `json:"authed"`
	Zones     int    `json:"zones,omitempty"` // 账户下的域名数
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Err       string `json:"err,omitempty"`
}

// RouteDiagnose 单条路由诊断
type RouteDiagnose struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Service  string `json:"service"`

	LocalOK  bool   `json:"local_ok"`
	LocalErr string `json:"local_err,omitempty"`

	DNSOK  bool   `json:"dns_ok"`
	DNSErr string `json:"dns_err,omitempty"`

	HTTPOK     bool   `json:"http_ok"`
	HTTPStatus int    `json:"http_status,omitempty"` // 502/530 这类信息比一句"不可达"有用得多
	HTTPErr    string `json:"http_err,omitempty"`

	// 云端 DNS 记录：只有"有令牌 + 路由存了 zone_id"时才查。
	// RecordChecked（查没查）和 RecordOK（查到没）必须分开：
	// 不分开的话，"没查"会被读成"记录不存在"，凭空多出一条假故障
	RecordChecked bool   `json:"record_checked"`
	RecordOK      bool   `json:"record_ok"`
	RecordErr     string `json:"record_err,omitempty"`
}

// RouteInput 诊断输入
type RouteInput struct {
	Name     string
	Hostname string
	Service  string
	ZoneID   string // 用于查云端 DNS 记录；为空则跳过这一项
}

// Diagnose 执行 Cloud 模式链路诊断。
// token/accountID 只用于"顺带查一眼云端 DNS 记录"：没配认证也能跑，
// 只是那一列显示"-"（宁可标"没查"，也不误报"记录不存在"）
func Diagnose(routes []RouteInput, token, accountID string) DiagnoseResult {
	var result DiagnoseResult

	result.Cloudflared = checkCloudflared()
	result.API = checkAPI(token, accountID)

	// 只在认证有效时才建客户端：查记录要真打 API，
	// 没凭据还去查只会给每条路由添一条"认证失败"
	var client *cfapi.Client
	if result.API.Authed {
		client = cfapi.New(token, accountID)
	}

	// 路由之间互不相干，并行探测：一条卡 5 秒，十条串行就是一分钟
	result.Routes = make([]RouteDiagnose, len(routes))
	var wg sync.WaitGroup
	for i, r := range routes {
		wg.Add(1)
		go func(idx int, route RouteInput) {
			defer wg.Done()
			result.Routes[idx] = diagnoseRoute(client, route)
		}(i, r)
	}
	wg.Wait()

	// 统计：本地服务通 + 域名能解析 +（查过的）云端记录在 = 这条路由是通的。
	// HTTPS 可达性不计入：它取决于 cloudflared 有没有在跑，属于"运行态"而非"配置态"
	result.Total = len(result.Routes)
	for _, r := range result.Routes {
		if r.LocalOK && r.DNSOK && (!r.RecordChecked || r.RecordOK) {
			result.Passed++
		} else {
			result.Failed++
		}
	}
	return result
}

// checkCloudflared 只看二进制在不在、进程活没活。
// 注意这里不调用 download.Download()：诊断是只读的，不该顺手往磁盘上装东西
func checkCloudflared() CloudflaredCheck {
	path := download.Path() // 始终带上路径：即使没装，也告诉用户应该在哪
	c := CloudflaredCheck{Path: path}

	if _, err := os.Stat(path); err != nil {
		return c // Installed=false。flare up / fast 会自动下载
	}
	c.Installed = true

	// 版本：老版本输出多行，只取第一行；执行失败就留空，由命令层说明"版本未知"。
	// 加超时：二进制万一卡住（损坏、被杀毒软件拦），诊断不能跟着一起挂
	ctx, cancel := context.WithTimeout(context.Background(), diagnoseTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, path, "version").CombinedOutput(); err == nil {
		c.Version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}

	c.Running = Running()
	if c.Running {
		c.PID = Pid()
	}
	return c
}

// checkAPI 先测网络可达性，再用 cfapi 验证凭据
func checkAPI(token, accountID string) APICheck {
	var a APICheck

	start := time.Now()
	conn, err := net.DialTimeout("tcp", apiAddr, diagnoseTimeout)
	if err != nil {
		a.Err = "无法连接 api.cloudflare.com:443（检查网络或代理）"
		return a
	}
	conn.Close()
	a.Reachable = true
	a.LatencyMS = time.Since(start).Milliseconds()

	if token == "" {
		a.Err = "未配置 API 令牌（flare init --token <API令牌> --account <账户ID>）"
		return a
	}

	// 唯一一次真 API 调用：ListZones 是 cfapi 里最轻的只读接口，
	// 既验证令牌，又顺带拿到账户下的域名数
	start = time.Now()
	zones, err := cfapi.New(token, accountID).ListZones()
	if err != nil {
		a.Err = err.Error()
		return a
	}
	a.Authed = true
	a.Zones = len(zones)
	a.LatencyMS = time.Since(start).Milliseconds()
	return a
}

// diagnoseRoute 检测一条路由：本地服务 → 域名解析 → 云端记录 → HTTPS 可达性。
// client 可能为 nil（没认证），此时不查云端记录
func diagnoseRoute(client *cfapi.Client, r RouteInput) RouteDiagnose {
	d := RouteDiagnose{
		Name:     r.Name,
		Hostname: r.Hostname,
		Service:  r.Service,
	}

	// 本地服务：端口从 service 里取，探测打在回环地址上——
	// flare add 写的就是 http://localhost:<端口>，cloudflared 也在本机连它
	port := netutil.ExtractPort(r.Service)
	if port == "" {
		d.LocalErr = "service 里解析不出端口（应形如 http://localhost:3000）"
	} else if addr := net.JoinHostPort("127.0.0.1", port); dialOK(addr) {
		d.LocalOK = true
	} else {
		d.LocalErr = "未监听 " + addr
	}

	// 域名解析：确认公网 DNS 能把这个域名解析出来
	if r.Hostname == "" {
		d.DNSErr = "未配置域名"
	} else if addrs, err := net.LookupHost(r.Hostname); err == nil && len(addrs) > 0 {
		d.DNSOK = true
	} else {
		d.DNSErr = "解析失败"
	}

	// 云端记录：确认 Cloudflare 里还挂着这个域名（按 add 时存下的 zone_id 查）
	if client != nil && r.ZoneID != "" && r.Hostname != "" {
		d.RecordChecked = true
		id, err := client.FindDNSRecord(r.ZoneID, r.Hostname)
		switch {
		case err != nil:
			d.RecordErr = err.Error()
		case id == "":
			d.RecordErr = "云端没有这条 DNS 记录"
		default:
			d.RecordOK = true
		}
	}

	// HTTPS 可达性：只有域名能解析才值得试，否则纯属白等超时
	if r.Hostname != "" && d.DNSOK {
		hc := &http.Client{Timeout: diagnoseTimeout}
		resp, err := hc.Get("https://" + r.Hostname)
		switch {
		case err != nil:
			d.HTTPErr = "请求失败"
		case resp.StatusCode >= 500:
			// 5xx 是云端替我们报的错：502 = 云端连不上本地服务，530 = 隧道不在线
			d.HTTPErr = fmt.Sprintf("HTTP %d（云端连不上本地）", resp.StatusCode)
		default:
			d.HTTPStatus = resp.StatusCode
			d.HTTPOK = true
		}
		if resp != nil {
			resp.Body.Close()
		}
	}

	return d
}

// dialOK：TCP 拨一下，通为 true。超时统一用 diagnoseTimeout
func dialOK(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, diagnoseTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
