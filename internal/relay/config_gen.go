package relay

import (
	"flare/internal/config"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func FrpcConfigPath() string { return filepath.Join(config.Dir(), "frpc.toml") }

// GenerateFrpcConfig 把 Relay 段写到正式配置文件（frpc.toml）
func GenerateFrpcConfig(r *config.RelayConfig) error {
	return GenerateFrpcConfigTo(r, FrpcConfigPath())
}

// GenerateFrpcConfigTo 把 Relay 段翻译成 frpc 的原生 TOML 并写到指定路径。
// 能指定路径是为了 quick 模式（flare fast --relay）：那条临时规则必须写到临时文件，
// 否则会把用户长期使用的 frpc.toml 覆盖掉
func GenerateFrpcConfigTo(r *config.RelayConfig, path string) error {
	if r.Server == "" {
		return fmt.Errorf("未配置中继服务器，请先执行 flare relay init <服务器地址:端口>")
	}
	host, port, err := net.SplitHostPort(r.Server) // "1.2.3.4:7000" → 两段
	if err != nil {
		return fmt.Errorf("服务器地址格式错误（应为 IP:端口）: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "serverAddr = %q\n", host) // %q：自动加引号并转义
	fmt.Fprintf(&b, "serverPort = %s\n", port)
	if r.Token != "" {
		fmt.Fprintf(&b, "auth.token = %q\n", r.Token)
	}
	// 首次登录失败（地址/端口/令牌不对）就直接退出。
	// frp 的默认值也是 true，这里写明是为了让这个行为可依赖：
	// daemon.StartRelay 靠"进程是不是立刻死了"判断配置对错，而不是只说一句"已启动"。
	b.WriteString("loginFailExit = true\n")
	b.WriteString("\n")

	for _, rule := range r.Rules {
		b.WriteString("[[proxies]]\n") // TOML 数组表：每个规则一段
		fmt.Fprintf(&b, "name = %q\n", rule.Name)
		fmt.Fprintf(&b, "type = %q\n", rule.Proto)
		lip := rule.LocalIP
		if lip == "" {
			lip = "127.0.0.1"
		} // 零值即默认
		fmt.Fprintf(&b, "localIP = %q\n", lip)
		fmt.Fprintf(&b, "localPort = %d\n", rule.LocalPort)
		if rule.RemotePort > 0 {
			fmt.Fprintf(&b, "remotePort = %d\n", rule.RemotePort)
		}
		b.WriteString("\n")
	}
	// 首次运行时 ~/.flare 还不存在，先建目录再写（和 config.Save 一样）
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}
