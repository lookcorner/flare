// Package ops 存放 CLI 与桌面 GUI 共用的"应用层"逻辑：
// 输入校验（端口、auth 串、服务器地址）和 ingress 推送。
// 原来这些散在 cmd/ 里，GUI 用不了；下沉到 internal 后两边共用同一份，
// 校验规则不会出现"CLI 拦住了、GUI 放过去"的分叉。
package ops

import (
	"flare/internal/cfapi"
	"flare/internal/config"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PushIngress 把本地路由表全量翻译成云端 ingress 规则并推送。
func PushIngress(client *cfapi.Client, cfg *config.Config) error {
	rules := make([]cfapi.IngressRule, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		rules = append(rules, cfapi.IngressRule{
			Hostname: r.Hostname,
			Service:  r.Service,
		})
	}
	if err := client.PushIngressConfig(cfg.Tunnel.ID, rules); err != nil {
		return fmt.Errorf("同步 ingress 失败: %w", err)
	}
	return nil
}

// ParseAuth 把 "用户名:密码" 拆成两半（第一个冒号切分，密码里允许再有冒号）
func ParseAuth(s string) (string, string, error) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("格式错误，应为 用户名:密码")
	}
	user, pwd := s[:idx], s[idx+1:]
	if user == "" || pwd == "" {
		return "", "", fmt.Errorf("用户名和密码不能为空")
	}
	return user, pwd, nil
}

// ParsePortNumber 把端口字符串转成数字。1~65535 是 net 的合法范围，
// 在这里拦住比等 cloudflared / frpc 报错强
func ParsePortNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("端口必须是 1~65535 的数字，收到: %q", s)
	}
	return n, nil
}

// CheckRelayAddr 校验"主机:端口"格式的服务器地址。
// frp 把 serverAddr 和 serverPort 当两个字段，地址里必须带端口
func CheckRelayAddr(s string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil || host == "" {
		return fmt.Errorf("服务器地址格式应为 IP:端口（如 1.2.3.4:7000），收到: %q", s)
	}
	if _, err := ParsePortNumber(port); err != nil {
		return fmt.Errorf("服务器端口必须是 1~65535 的数字，收到: %q", port)
	}
	return nil
}

// RouteNameFromDomain 从域名取一个默认路由名：chat.example.com → chat
func RouteNameFromDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	if i := strings.Index(domain, "."); i > 0 {
		return domain[:i]
	}
	return domain
}

// RelayRuleSource：把规则说成人话，如 "127.0.0.1:22"（没填 IP 时按默认回环地址显示）
func RelayRuleSource(r config.RelayRule) string {
	ip := r.LocalIP
	if ip == "" {
		ip = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", ip, r.LocalPort)
}

// RelayRuleTarget：规则最终会落在服务器的哪个端口上。
// RemotePort 为 0 = 由 frps 分配（通常是随机的），说清楚免得用户以为配错了
func RelayRuleTarget(server string, r config.RelayRule) string {
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		host = server
	}
	if r.RemotePort == 0 {
		return host + ":由服务器分配"
	}
	return fmt.Sprintf("%s:%d", host, r.RemotePort)
}
