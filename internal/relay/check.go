package relay

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"flare/internal/config"
)

// checkTimeout：单个 TCP 探测最长等多久。
// 一次诊断要连着测好几条规则，一条等 5 秒、十条就是一分钟，没人等得起
const checkTimeout = 3 * time.Second

// CheckResult 链路检测总结果
type CheckResult struct {
	Server        string            `json:"server"`
	ServerOK      bool              `json:"server_ok"`
	ServerLatency int64             `json:"server_latency_ms"`
	FrpcRunning   bool              `json:"frpc_running"`
	FrpcPID       int               `json:"frpc_pid"`
	Rules         []RuleCheckResult `json:"rules"`
	Total         int               `json:"total"`
	Passed        int               `json:"passed"`
	Failed        int               `json:"failed"`
	Skipped       int               `json:"skipped,omitempty"` // 无法探测的 udp 规则，不计入通/断
}

// RuleCheckResult 单条规则检测结果
type RuleCheckResult struct {
	Name       string `json:"name"`
	Proto      string `json:"proto"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	LocalOK    bool   `json:"local_ok"`
	RemoteOK   bool   `json:"remote_ok"`
	LatencyMS  int64  `json:"latency_ms"`
	LocalErr   string `json:"local_err,omitempty"`
	RemoteErr  string `json:"remote_err,omitempty"`
	// Skipped：udp 规则。udp 无连接，"拨号成功"不代表有服务在听（内核不握手），
	// 所以不做探测：标"未探测"远好过给一个假的 ✓ 或 ✗
	Skipped bool `json:"skipped,omitempty"`
}

// FrpcState：frpc 进程状态，由调用方（cmd）探测后注入。
// 为什么不在这里直接调 daemon.RelayRunning()：daemon 依赖 relay
// （daemon.StartRelay 要调 GenerateFrpcConfig），relay 再 import daemon 就成了循环依赖。
// 进程探测是 daemon 的活，这里只负责把它带来的事实写进报告
type FrpcState struct {
	Running bool
	PID     int
}

// Check 执行链路检测。ruleName 为空 = 检测全部规则
func Check(cfg *config.RelayConfig, ruleName string, frpc FrpcState) CheckResult {
	result := CheckResult{
		Server:      cfg.Server,
		FrpcRunning: frpc.Running,
		FrpcPID:     frpc.PID,
	}

	// frps 服务器：连得上控制端口（默认 7000）才谈得上后面的事
	if cfg.Server != "" {
		start := time.Now()
		if dialOK(cfg.Server) {
			result.ServerOK = true
			result.ServerLatency = time.Since(start).Milliseconds()
		}
	}

	// 筛选要检测的规则
	rules := cfg.Rules
	if ruleName != "" {
		rules = nil
		for _, r := range cfg.Rules {
			if r.Name == ruleName {
				rules = append(rules, r)
				break
			}
		}
	}

	// 规则之间互不相干，并行探测
	result.Rules = make([]RuleCheckResult, len(rules))
	var wg sync.WaitGroup
	for i, rule := range rules {
		wg.Add(1)
		go func(idx int, r config.RelayRule) {
			defer wg.Done()
			result.Rules[idx] = checkRule(r, cfg.Server)
		}(i, rule)
	}
	wg.Wait()

	result.Total = len(result.Rules)
	for _, r := range result.Rules {
		switch {
		case r.Skipped:
			result.Skipped++
		case r.LocalOK && (r.RemoteOK || r.RemotePort == 0):
			// RemotePort 为 0 = 端口由 frps 随机分配，探测不了，只要本地通就算这条规则配好了
			result.Passed++
		default:
			result.Failed++
		}
	}
	return result
}

// checkRule 检测单条规则：本地服务 + 服务器上的穿透端口
func checkRule(r config.RelayRule, server string) RuleCheckResult {
	rc := RuleCheckResult{
		Name:       r.Name,
		Proto:      r.Proto,
		LocalPort:  r.LocalPort,
		RemotePort: r.RemotePort,
	}

	// udp 规则不探测：udp 没有握手，连不上不代表服务没起。
	// 硬按 tcp 去测，只会把正常的 udp 服务报成"未监听"
	if strings.EqualFold(r.Proto, "udp") {
		rc.Skipped = true
		rc.LocalErr = "udp 规则无法探测"
		if r.RemotePort > 0 {
			rc.RemoteErr = "udp 规则无法探测"
		}
		return rc
	}

	localIP := r.LocalIP
	if localIP == "" {
		localIP = "127.0.0.1"
	}
	localAddr := net.JoinHostPort(localIP, strconv.Itoa(r.LocalPort))
	if dialOK(localAddr) {
		rc.LocalOK = true
	} else {
		rc.LocalErr = "未监听 " + localAddr
	}

	// 远程穿透端口：从服务器那一侧看这个端口开没开
	if r.RemotePort > 0 && server != "" {
		host, _, err := net.SplitHostPort(server)
		if err != nil {
			host = server // 容错：地址里没带端口时整个串当主机名
		}
		remoteAddr := net.JoinHostPort(host, strconv.Itoa(r.RemotePort))
		start := time.Now()
		if dialOK(remoteAddr) {
			rc.RemoteOK = true
			rc.LatencyMS = time.Since(start).Milliseconds()
		} else {
			rc.RemoteErr = "不可达（检查服务器防火墙/安全组是否放行该端口）"
		}
	}

	return rc
}

// dialOK：TCP 拨一下，通为 true
func dialOK(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, checkTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
