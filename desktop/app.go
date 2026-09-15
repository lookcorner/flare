package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"flare/internal/authproxy"
	"flare/internal/cfapi"
	"flare/internal/config"
	"flare/internal/daemon"
	flog "flare/internal/log"
	"flare/internal/netutil"
	"flare/internal/ops"
	"flare/internal/relay"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是 Wails 的绑定层：前端 window.go.main.App.* 调到的方法都在这里。
// 原则：只做参数校验/编排，业务逻辑一律走 internal/* ——与 CLI 共用同一份实现。
type App struct {
	ctx context.Context

	mu          sync.Mutex
	authProxies []*authproxy.Proxy // TunnelUp 时为带鉴权路由启动的登录网关
	fast        *daemon.FastSession
	logStops    map[string]chan struct{}
}

func NewApp() *App {
	return &App{logStops: make(map[string]chan struct{})}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := flog.Setup("flare"); err == nil {
		// GUI 模式下 log.Setup 的终端输出没人看，但至少把文件接上
	}
}

// shutdown 应用退出时的清理：关鉴权网关、停快速穿透、停日志跟踪。
// 注意 cloudflared/frpc 后台进程不在此列——它们是 PID 文件记账的独立进程，
// 设计上就是要活过 GUI 的（与 flare up 后 CLI 退出同理）。
func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.authProxies {
		p.Stop()
	}
	a.authProxies = nil
	if a.fast != nil {
		a.fast.Stop()
		a.fast = nil
	}
	for _, stop := range a.logStops {
		close(stop)
	}
	a.logStops = make(map[string]chan struct{})
	flog.Close()
}

// emit 向前端发事件；ctx 未就绪（单元测试）时安全跳过
func (a *App) emit(event string, data ...interface{}) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, event, data...)
	}
}

// ===================== 总览 =====================

// StateInfo 概览页一把梭需要的数据
type StateInfo struct {
	HasAuth       bool   `json:"hasAuth"`
	TunnelID      string `json:"tunnelId"`
	TunnelName    string `json:"tunnelName"`
	TunnelRunning bool   `json:"tunnelRunning"`
	TunnelPID     int    `json:"tunnelPid"`
	RelayServer   string `json:"relayServer"`
	RelayRunning  bool   `json:"relayRunning"`
	RelayPID      int    `json:"relayPid"`
	RouteCount    int    `json:"routeCount"`
	RuleCount     int    `json:"ruleCount"`
	AuthCount     int    `json:"authCount"`
	FastRunning   bool   `json:"fastRunning"`
	DataDir       string `json:"dataDir"`
	Portable      bool   `json:"portable"`
}

func (a *App) GetState() StateInfo {
	s := StateInfo{DataDir: config.Dir(), Portable: config.PortTable()}
	cfg, err := config.Load()
	if err != nil {
		// 配置读不出来（版本过高/损坏）也不能让界面空转：进程状态照报
		s.TunnelRunning = daemon.Running()
		s.TunnelPID = daemon.Pid()
		s.RelayRunning = daemon.RelayRunning()
		s.RelayPID = daemon.RelayPid()
		a.mu.Lock()
		s.FastRunning = a.fast != nil
		a.mu.Unlock()
		return s
	}
	s.HasAuth = cfg.Auth.ApiToken != "" && cfg.Auth.AccountID != ""
	s.TunnelID = cfg.Tunnel.ID
	s.TunnelName = cfg.Tunnel.Name
	s.TunnelRunning = daemon.Running()
	s.TunnelPID = daemon.Pid()
	s.RelayServer = cfg.Relay.Server
	s.RelayRunning = daemon.RelayRunning()
	s.RelayPID = daemon.RelayPid()
	s.RouteCount = len(cfg.Routes)
	s.RuleCount = len(cfg.Relay.Rules)
	for _, r := range cfg.Routes {
		if r.Auth != nil {
			s.AuthCount++
		}
	}
	a.mu.Lock()
	s.FastRunning = a.fast != nil
	a.mu.Unlock()
	return s
}

// ===================== 认证与隧道 =====================

// SaveAuth 对应 flare init：保存 API Token + Account ID
func (a *App) SaveAuth(token, accountID string) error {
	token = strings.TrimSpace(token)
	accountID = strings.TrimSpace(accountID)
	if token == "" || accountID == "" {
		return fmt.Errorf("API 令牌和账户 ID 不能为空")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Auth = config.AuthConfig{ApiToken: token, AccountID: accountID}
	if err := config.Save(cfg); err != nil {
		return err
	}
	flog.Auditf("desktop: 认证信息已保存")
	return nil
}

// CreateTunnel 对应 flare create：建隧道、取凭据。
// 已有隧道但没凭据时走补取分支（与 CLI 行为一致，可安全重跑）
func (a *App) CreateTunnel(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Auth.ApiToken == "" {
		return fmt.Errorf("请先在「设置」页配置 Cloudflare 认证信息")
	}
	client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)

	if cfg.Tunnel.ID != "" {
		if cfg.Tunnel.Token != "" {
			return fmt.Errorf("已存在隧道 %s，想换一条请先销毁", cfg.Tunnel.Name)
		}
		token, err := client.GetTunnelToken(cfg.Tunnel.ID)
		if err != nil {
			return fmt.Errorf("补取隧道凭据失败: %w", err)
		}
		cfg.Tunnel.Token = token
		return config.Save(cfg)
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = "flare"
	}
	tunnel, err := client.CreateTunnel(name)
	if err != nil {
		return err
	}
	// 先落盘身份再取凭据：取凭据失败重跑会走上面的补取分支，不会建出两条隧道
	cfg.Tunnel = config.TunnelConfig{ID: tunnel.ID, Name: tunnel.Name}
	if err := config.Save(cfg); err != nil {
		return err
	}
	token, err := client.GetTunnelToken(tunnel.ID)
	if err != nil {
		return fmt.Errorf("隧道已创建但获取凭据失败: %w（重试会自动补取，不重复建隧道）", err)
	}
	cfg.Tunnel.Token = token
	if err := config.Save(cfg); err != nil {
		return err
	}
	flog.Auditf("desktop: 创建隧道 %s (%s)", tunnel.Name, tunnel.ID)
	return nil
}

// DestroyTunnel 对应 flare destroy -y：删全部 DNS 记录 + 删隧道 + 清本地。
// 确认弹窗在前端做（等价于 CLI 的输入隧道名确认）
func (a *App) DestroyTunnel() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Tunnel.ID == "" {
		return fmt.Errorf("本地没有隧道记录（无需销毁）")
	}
	if daemon.Running() {
		return fmt.Errorf("隧道正在运行，请先停止")
	}
	client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
	for _, r := range cfg.Routes {
		if r.DNSRecordID == "" {
			continue
		}
		if err := client.DeleteDNSRecord(r.ZoneID, r.DNSRecordID); err != nil {
			flog.Warnf("删除 DNS 记录 %s 失败（%v），重试销毁可继续", r.Hostname, err)
		}
	}
	if err := client.DeleteTunnel(cfg.Tunnel.ID); err != nil {
		return err
	}
	cfg.Tunnel = config.TunnelConfig{}
	cfg.Routes = nil
	if err := config.Save(cfg); err != nil {
		return err
	}
	flog.Auditf("desktop: 隧道已销毁")
	return nil
}

// TunnelInfo 云端隧道列表项
type TunnelInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	InUse bool   `json:"inUse"`
}

// ListTunnels 对应 flare tunnels：列云端隧道并标出本地在用的
func (a *App) ListTunnels() ([]TunnelInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Auth.ApiToken == "" {
		return nil, fmt.Errorf("请先配置认证信息")
	}
	list, err := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID).ListTunnels()
	if err != nil {
		return nil, err
	}
	out := make([]TunnelInfo, 0, len(list))
	for _, t := range list {
		out = append(out, TunnelInfo{ID: t.ID, Name: t.Name, InUse: t.ID == cfg.Tunnel.ID})
	}
	return out, nil
}

// DeleteCloudTunnel 对应 flare tunnels delete -y：只许删没用到的孤儿隧道
func (a *App) DeleteCloudTunnel(id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if id == cfg.Tunnel.ID {
		return fmt.Errorf("这是本机正在使用的隧道，请用「销毁隧道」（会一并清理 DNS 与本地配置）")
	}
	return cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID).DeleteTunnel(id)
}

// TunnelUp 对应 flare up：启动鉴权网关 + 同步 ingress + 后台起 cloudflared。
// 与 CLI 的差别：网关由本进程持有，直到 TunnelDown 或应用退出
// （CLI 的 up 是"启动即退出"，网关随 defer 回收——桌面版没这个问题）
func (a *App) TunnelUp() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Tunnel.Token == "" {
		return fmt.Errorf("还没有隧道，请先创建")
	}
	if daemon.Running() {
		return fmt.Errorf("隧道已在运行 (PID: %d)", daemon.Pid())
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	// 防御：上次运行残留网关的话先清干净，避免同路由叠两个代理
	for _, p := range a.authProxies {
		p.Stop()
	}
	a.authProxies = nil

	for i := range cfg.Routes {
		r := cfg.Routes[i]
		if r.Auth == nil {
			continue
		}
		sigKey, err := hex.DecodeString(r.Auth.SigningKey)
		if err != nil {
			return fmt.Errorf("路由 %s 的 signing_key 无效: %w", r.Name, err)
		}
		port := netutil.ExtractPort(r.Service)
		if port == "" {
			return fmt.Errorf("路由 %s 的 service 格式无效: %s", r.Name, r.Service)
		}
		proxy, err := authproxy.New(authproxy.Config{
			Username:   r.Auth.Username,
			Password:   r.Auth.Password,
			TargetPort: port,
			SigningKey: sigKey, // 持久化密钥：重启后既有 Cookie 仍有效
			CookieTTL:  time.Duration(r.Auth.CookieTTLOrDefault()) * time.Second,
		})
		if err != nil {
			return fmt.Errorf("路由 %s 启动鉴权网关失败: %w", r.Name, err)
		}
		if err := proxy.Start(); err != nil {
			return err
		}
		a.authProxies = append(a.authProxies, proxy)
		// 内存中把该路由的 service 改写为网关地址——不落盘
		cfg.Routes[i].Service = "http://localhost:" + strconv.Itoa(proxy.ListenPort())
	}

	if len(cfg.Routes) > 0 {
		client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
		if err := ops.PushIngress(client, cfg); err != nil {
			// 与 CLI 一致：同步失败不阻断启动，云端现有配置兜底
			flog.Warnf("同步 ingress 失败: %v（将使用云端现有配置）", err)
		}
	}
	if err := daemon.Start(cfg.Tunnel.Token); err != nil {
		for _, p := range a.authProxies {
			p.Stop()
		}
		a.authProxies = nil
		return err
	}
	flog.Auditf("desktop: 隧道已启动 (PID: %d)", daemon.Pid())
	return nil
}

// TunnelDown 对应 flare down：停 cloudflared，顺带收掉鉴权网关
func (a *App) TunnelDown() error {
	err := daemon.Stop()
	a.mu.Lock()
	for _, p := range a.authProxies {
		p.Stop()
	}
	a.authProxies = nil
	a.mu.Unlock()
	flog.Auditf("desktop: 隧道已停止")
	return err
}

// ===================== 域名路由 =====================

// RouteInfo 路由列表项
type RouteInfo struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
	AuthUser string `json:"authUser"` // "" = 无密码保护
}

func (a *App) ListRoutes() ([]RouteInfo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	out := make([]RouteInfo, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		user := ""
		if r.Auth != nil {
			user = r.Auth.Username
		}
		out = append(out, RouteInfo{Name: r.Name, Hostname: r.Hostname, Service: r.Service, AuthUser: user})
	}
	return out, nil
}

// AddRoute 对应 flare add：查 Zone → 建/更新 CNAME → 落盘 → 推 ingress。
// 顺序与 CLI 完全一致，重复调用幂等（DNS 已存在则更新目标）
func (a *App) AddRoute(domain string, port int, name, authUser, authPass string) error {
	domain = strings.TrimSpace(domain)
	name = strings.TrimSpace(name)
	if domain == "" {
		return fmt.Errorf("域名不能为空")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口必须是 1~65535 的数字，收到: %d", port)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Tunnel.ID == "" {
		return fmt.Errorf("请先创建隧道（概览页或设置页）")
	}
	if name == "" {
		name = ops.RouteNameFromDomain(domain)
	}
	if cfg.FindRoute(name) != nil {
		return fmt.Errorf("路由 %s 已存在", name)
	}
	if (authUser == "") != (authPass == "") {
		return fmt.Errorf("用户名和密码要一起填")
	}

	client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
	zone, err := client.FindZoneForDomain(domain)
	if err != nil {
		return fmt.Errorf("查询 %s 所属 Zone 失败: %w", domain, err)
	}
	if zone == nil {
		return fmt.Errorf("账户下找不到 %s 所属的 Zone，请确认域名已接入该 Cloudflare 账户", domain)
	}
	target := cfg.Tunnel.ID + ".cfargotunnel.com"

	var recordID string
	existing, err := client.FindDNSRecord(zone.ID, domain)
	if err != nil {
		return err
	}
	if existing != "" {
		if err := client.UpdateCNAME(zone.ID, existing, domain, target); err != nil {
			return err
		}
		recordID = existing
	} else {
		recordID, err = client.CreateCNAME(zone.ID, domain, target)
		if err != nil {
			return err
		}
	}

	route := config.RouteConfig{
		Name:        name,
		Hostname:    domain,
		Service:     "http://localhost:" + strconv.Itoa(port),
		ZoneID:      zone.ID,
		DNSRecordID: recordID,
	}
	if authUser != "" {
		route.Auth = &config.AuthProxy{
			Username:   authUser,
			Password:   authPass,
			SigningKey: hex.EncodeToString(authproxy.RandomKey()),
		}
	}
	cfg.Routes = append(cfg.Routes, route)
	if err := config.Save(cfg); err != nil {
		return err
	}
	if err := ops.PushIngress(client, cfg); err != nil {
		return fmt.Errorf("推送 ingress 失败: %w\nDNS 记录已创建，重新添加即可修复（会自动更新）", err)
	}
	flog.Auditf("desktop: 添加路由 %s → %s（名称 %s）", domain, route.Service, name)
	return nil
}

// RemoveRoute 对应 flare remove：删云端 DNS → 删本地 → 推剩余 ingress
func (a *App) RemoveRoute(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	route := cfg.FindRoute(name)
	if route == nil {
		return fmt.Errorf("路由 %s 不存在", name)
	}
	client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
	if route.DNSRecordID != "" {
		if err := client.DeleteDNSRecord(route.ZoneID, route.DNSRecordID); err != nil {
			return err // 失败即停：本地保持原样，重试即可
		}
	}
	cfg.RemoveRoute(name)
	if err := ops.PushIngress(client, cfg); err != nil {
		flog.Warnf("ingress 同步失败（%v），本地已移除，下次推送会自动修正", err)
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	flog.Auditf("desktop: 删除路由 %s", name)
	return nil
}

// SyncIngress 手动把本地路由表推送到云端（「同步 ingress」按钮）
func (a *App) SyncIngress() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Tunnel.ID == "" {
		return fmt.Errorf("还没有隧道")
	}
	return ops.PushIngress(cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID), cfg)
}

// ===================== 端口中继 =====================

// RelayRuleInfo 穿透规则列表项（含算好的源/目标展示串）
type RelayRuleInfo struct {
	Name       string `json:"name"`
	Proto      string `json:"proto"`
	LocalIP    string `json:"localIp"`
	LocalPort  int    `json:"localPort"`
	RemotePort int    `json:"remotePort"`
	Source     string `json:"source"`
	Target     string `json:"target"`
}

// RelayInfo 中继页一把梭数据
type RelayInfo struct {
	Server   string          `json:"server"`
	HasToken bool            `json:"hasToken"`
	Running  bool            `json:"running"`
	PID      int             `json:"pid"`
	Rules    []RelayRuleInfo `json:"rules"`
}

func (a *App) GetRelay() (RelayInfo, error) {
	info := RelayInfo{Running: daemon.RelayRunning(), PID: daemon.RelayPid()}
	cfg, err := config.Load()
	if err != nil {
		return info, err
	}
	info.Server = cfg.Relay.Server
	info.HasToken = cfg.Relay.Token != ""
	for _, r := range cfg.Relay.Rules {
		info.Rules = append(info.Rules, RelayRuleInfo{
			Name:       r.Name,
			Proto:      r.Proto,
			LocalIP:    r.LocalIP,
			LocalPort:  r.LocalPort,
			RemotePort: r.RemotePort,
			Source:     ops.RelayRuleSource(r),
			Target:     ops.RelayRuleTarget(cfg.Relay.Server, r),
		})
	}
	return info, nil
}

// SaveRelayServer 对应 flare relay init：校验并保存服务器地址 + 令牌
func (a *App) SaveRelayServer(server, token string) error {
	server = strings.TrimSpace(server)
	if err := ops.CheckRelayAddr(server); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.Relay.Server = server
	cfg.Relay.Token = strings.TrimSpace(token)
	if err := config.Save(cfg); err != nil {
		return err
	}
	flog.Auditf("desktop: 中继服务器已保存 %s", server)
	return nil
}

// AddRelayRule 对应 flare relay add：加/覆盖一条穿透规则
func (a *App) AddRelayRule(name, proto, localIP string, localPort, remotePort int, force bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("规则名称不能为空")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Relay.Server == "" {
		return fmt.Errorf("请先配置中继服务器")
	}
	existing := cfg.FindRelayRule(name)
	if existing != nil && !force {
		return fmt.Errorf("穿透规则 %s 已存在（要改它请勾选「覆盖同名规则」）", name)
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("协议只能是 tcp 或 udp，收到: %q", proto)
	}
	if localPort < 1 || localPort > 65535 {
		return fmt.Errorf("本地端口必须是 1~65535，收到: %d", localPort)
	}
	if remotePort < 0 || remotePort > 65535 {
		return fmt.Errorf("公网端口取值需在 1~65535，留空由服务器分配，收到: %d", remotePort)
	}
	if existing != nil {
		existing.Proto = proto
		existing.LocalIP = strings.TrimSpace(localIP)
		existing.LocalPort = localPort
		existing.RemotePort = remotePort
	} else {
		cfg.Relay.Rules = append(cfg.Relay.Rules, config.RelayRule{
			Name:       name,
			Proto:      proto,
			LocalIP:    strings.TrimSpace(localIP),
			LocalPort:  localPort,
			RemotePort: remotePort,
		})
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	flog.Auditf("desktop: 穿透规则已保存 %s (%s)", name, proto)
	// 规则是 frpc 启动时读进去的，正在跑的进程不会感知——提醒前端提示重启
	return nil
}

// RemoveRelayRule 对应 flare relay remove
func (a *App) RemoveRelayRule(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.RemoveRelayRule(name) {
		return fmt.Errorf("穿透规则 %s 不存在", name)
	}
	return config.Save(cfg)
}

// RelayUp 对应 flare relay up
func (a *App) RelayUp() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Relay.Server == "" {
		return fmt.Errorf("请先配置中继服务器")
	}
	if len(cfg.Relay.Rules) == 0 {
		return fmt.Errorf("还没有穿透规则，请先添加")
	}
	if err := daemon.StartRelay(&cfg.Relay); err != nil {
		return err
	}
	flog.Auditf("desktop: frpc 已启动 (PID: %d)", daemon.RelayPid())
	return nil
}

// RelayDown 对应 flare relay down
func (a *App) RelayDown() error {
	return daemon.StopRelay()
}

// CheckRelay 对应 flare relay check：探测服务器与每条规则两端
func (a *App) CheckRelay() (relay.CheckResult, error) {
	var empty relay.CheckResult
	cfg, err := config.Load()
	if err != nil {
		return empty, err
	}
	if cfg.Relay.Server == "" {
		return empty, fmt.Errorf("未配置中继服务器")
	}
	return relay.Check(&cfg.Relay, "", relay.FrpcState{
		Running: daemon.RelayRunning(),
		PID:     daemon.RelayPid(),
	}), nil
}

// ===================== 快速穿透 =====================

// StartFast 启动一次快速穿透会话。
// 进度通过事件回报：fast:url（cloudflare 模式拿到域名）/ fast:target（中继模式入口地址）
// / fast:log（每行输出）/ fast:exit（进程退出，参数为错误或空串）
func (a *App) StartFast(port int, relayMode bool, proto, user, pass string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口必须是 1~65535 的数字，收到: %d", port)
	}
	a.mu.Lock()
	if a.fast != nil {
		a.mu.Unlock()
		return fmt.Errorf("已有快速穿透在运行，请先停止")
	}
	a.mu.Unlock()

	onLine := func(line string) { a.emit("fast:log", line) }
	var s *daemon.FastSession
	var err error
	if relayMode {
		// 中继模式不支持密码保护（与 CLI 一致，给出提醒而不是静默忽略）
		var target string
		s, target, err = daemon.StartFastRelaySession(strconv.Itoa(port), proto, onLine)
		if err == nil {
			a.emit("fast:target", target, proto)
		}
	} else {
		onURL := func(url string) { a.emit("fast:url", url) }
		s, err = daemon.StartFastTunnelSession(strconv.Itoa(port), user, pass, onURL, onLine)
	}
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.fast = s
	a.mu.Unlock()
	flog.Auditf("desktop: 快速穿透已启动（port %d, relay=%v）", port, relayMode)

	go func() {
		runErr := <-s.Done()
		a.mu.Lock()
		a.fast = nil
		a.mu.Unlock()
		msg := ""
		if runErr != nil {
			msg = runErr.Error()
		}
		a.emit("fast:exit", msg)
	}()
	return nil
}

// StopFast 停止当前快速穿透会话
func (a *App) StopFast() error {
	a.mu.Lock()
	s := a.fast
	a.mu.Unlock()
	if s == nil {
		return fmt.Errorf("当前没有运行中的快速穿透")
	}
	return s.Stop()
}

// ===================== 诊断 =====================

// RunDiagnose 对应 flare diagnose：链路每段都探一遍，结果原样给前端渲染
func (a *App) RunDiagnose() (daemon.DiagnoseResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return daemon.DiagnoseResult{}, err
	}
	routes := make([]daemon.RouteInput, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		routes = append(routes, daemon.RouteInput{
			Name:     r.Name,
			Hostname: r.Hostname,
			Service:  r.Service,
			ZoneID:   r.ZoneID,
		})
	}
	return daemon.Diagnose(routes, cfg.Auth.ApiToken, cfg.Auth.AccountID), nil
}

// ===================== 日志 =====================

var logComponents = map[string]bool{"flare": true, "cloudflared": true, "frpc": true, "frps": true}

// TailLog 取组件日志最后 n 行（对应 flare log -n）
func (a *App) TailLog(comp string, n int) ([]string, error) {
	if !logComponents[comp] {
		return nil, fmt.Errorf("未知组件 %q，可选: flare / cloudflared / frpc / frps", comp)
	}
	lines, err := flog.Tail(comp, n)
	if err != nil {
		return []string{}, nil // 还没有日志 = 空列表，不是错误
	}
	return lines, nil
}

// FollowLog 开始实时跟踪（对应 flare log -f）：新增行通过 "log:<组件>" 事件推给前端
func (a *App) FollowLog(comp string) error {
	if !logComponents[comp] {
		return fmt.Errorf("未知组件 %q", comp)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.logStops[comp] != nil {
		return nil // 已在跟踪，重复调用幂等
	}
	stop := make(chan struct{})
	a.logStops[comp] = stop
	w := &lineEmitWriter{fn: func(line string) { a.emit("log:"+comp, line) }}
	go flog.Follow(comp, w, stop)
	return nil
}

// UnfollowLog 停止跟踪
func (a *App) UnfollowLog(comp string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if stop := a.logStops[comp]; stop != nil {
		close(stop)
		delete(a.logStops, comp)
	}
}

// lineEmitWriter 把任意大小的写块按行切开再回调：
// log.Follow 按块 io.Copy，行边界被切开会闪断，必须先攒成行
type lineEmitWriter struct {
	mu  sync.Mutex
	buf []byte
	fn  func(string)
}

func (w *lineEmitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.fn(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// ===================== 杂项 =====================

// LogDir 日志目录（前端展示路径用）
func (a *App) LogDir() string { return flog.Dir() }

// OpenPath 用系统默认方式打开路径/URL（访达、浏览器）
func (a *App) OpenPath(p string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", p)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	return cmd.Start()
}
