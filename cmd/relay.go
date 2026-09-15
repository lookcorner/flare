package cmd

import (
	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/ops"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// relayCmd 是 "flare relay" 的父命令，自己不干活：
// 具体功能都在 init/add/remove/status/up/down 这些子命令里。
// 为什么需要它？Cloudflare 隧道只转发 HTTP/HTTPS，要暴露 SSH、数据库、
// 游戏服这类 TCP/UDP 服务，得靠自建中转服务器（frps）+ 本地客户端（frpc）。
var relayCmd = &cobra.Command{
	Use:   "relay",
	Short: "端口穿透：把 TCP/UDP 服务通过自建 frps 服务器暴露到公网",
	Long: "本地 frpc 连上你的 frps 服务器，把本机端口映射成服务器的公网端口。\n" +
		"适合 SSH、数据库、游戏服等 Cloudflare 隧道不转发（非 HTTP）的流量。",
}

var relayInitToken string

var relayInitCmd = &cobra.Command{
	Use:   "init [服务器地址]",
	Short: "配置中继服务器地址与令牌",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		server := strings.TrimSpace(args[0])
		// 提前校验格式，别等 frpc 启动才报错：地址必须带端口，
		// 因为 frp 把 serverAddr 和 serverPort 当成两个字段
		host, port, err := net.SplitHostPort(server)
		if err != nil || host == "" {
			return fmt.Errorf("服务器地址格式应为 IP:端口（如 1.2.3.4:7000），收到: %q", server)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("服务器端口必须是 1~65535 的数字，收到: %q", port)
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.Relay.Server = server
		cfg.Relay.Token = strings.TrimSpace(relayInitToken)
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("中继服务器已保存: %s → %s\n", server, config.Path())
		if cfg.Relay.Token == "" {
			fmt.Println("提示: 未设置令牌。frps 若开了 auth.token，请用 --token 再执行一次")
		}
		return nil
	},
}

var relayAddProto string
var relayAddIP string
var relayAddLocal, relayAddRemote int
var relayAddForce bool

var relayAddCmd = &cobra.Command{
	Use:   "add [名称]",
	Short: "添加穿透规则",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Relay.Server == "" {
			return fmt.Errorf("请先执行 flare relay init <服务器地址:端口>")
		}
		existing := cfg.FindRelayRule(name)
		if existing != nil && !relayAddForce {
			// 改规则用 --force 原地覆盖，而不是先 remove 再 add：
			// 直接覆盖能保住规则在列表里的位置（顺序对排错没影响，但对人看列表有影响）
			return fmt.Errorf("穿透规则 %s 已存在（要改它：flare relay add %s --local <端口> --force）", name, name)
		}
		proto := strings.ToLower(strings.TrimSpace(relayAddProto))
		if proto != "tcp" && proto != "udp" {
			return fmt.Errorf("--proto 只能是 tcp 或 udp，收到: %q", relayAddProto)
		}
		// 端口范围就是 net 的合法范围，越界的值 frpc 起不来，在这里拦住
		if relayAddLocal < 1 || relayAddLocal > 65535 {
			return fmt.Errorf("--local 必须是 1~65535 的端口，收到: %d", relayAddLocal)
		}
		if relayAddRemote < 0 || relayAddRemote > 65535 {
			return fmt.Errorf("--remote 留空表示由服务器分配，取值需在 1~65535，收到: %d", relayAddRemote)
		}

		if existing != nil { // --force 覆盖已有规则
			existing.Proto = proto
			existing.LocalIP = strings.TrimSpace(relayAddIP)
			existing.LocalPort = relayAddLocal
			existing.RemotePort = relayAddRemote
		} else {
			cfg.Relay.Rules = append(cfg.Relay.Rules, config.RelayRule{
				Name:       name,
				Proto:      proto,
				LocalIP:    strings.TrimSpace(relayAddIP),
				LocalPort:  relayAddLocal,
				RemotePort: relayAddRemote,
			})
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		rule := cfg.FindRelayRule(name) // 覆盖分支要看改完之后的样子，所以重新取一次
		if existing != nil {
			fmt.Printf("穿透规则已更新: %s (%s) %s → %s\n", name, rule.Proto,
				relayRuleSource(*rule), relayRuleTarget(cfg.Relay.Server, *rule))
		} else {
			fmt.Printf("穿透规则已添加: %s (%s) %s → %s\n", name, rule.Proto,
				relayRuleSource(*rule), relayRuleTarget(cfg.Relay.Server, *rule))
		}
		relayReloadHint()
		return nil
	},
}

var relayRemoveCmd = &cobra.Command{
	Use:   "remove [名称]",
	Short: "删除穿透规则",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.FindRelayRule(args[0]) == nil {
			return fmt.Errorf("穿透规则 %s 不存在（flare relay status 可查看全部规则）", args[0])
		}
		cfg.RemoveRelayRule(args[0])
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("穿透规则已删除: %s\n", args[0])
		relayReloadHint()
		return nil
	},
}

var relayStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看中继配置与 frpc 运行状态",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Relay.Server == "" {
			fmt.Println("未配置中继服务器（flare relay init <服务器地址:端口>）")
			return nil
		}
		fmt.Printf("中继服务器: %s\n", cfg.Relay.Server)
		if daemon.RelayRunning() {
			fmt.Printf("frpc 运行中 (PID: %d)\n", daemon.RelayPid())
		} else {
			fmt.Println("frpc 未运行")
		}
		if len(cfg.Relay.Rules) == 0 {
			fmt.Println("尚无穿透规则（flare relay add <名称> --local <端口>）")
			return nil
		}
		fmt.Println("穿透规则:")
		for _, r := range cfg.Relay.Rules {
			fmt.Printf("  %-12s %-4s %s → %s\n", r.Name, r.Proto,
				relayRuleSource(r), relayRuleTarget(cfg.Relay.Server, r))
		}
		return nil
	},
}

var relayUpCmd = &cobra.Command{
	Use:   "up",
	Short: "后台启动 frpc",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Relay.Server == "" {
			return fmt.Errorf("请先执行 flare relay init <服务器地址:端口>")
		}
		if len(cfg.Relay.Rules) == 0 {
			return fmt.Errorf("还没有穿透规则，请先执行 flare relay add <名称> --local <端口>")
		}
		return daemon.StartRelay(&cfg.Relay)
	},
}

var relayDownCmd = &cobra.Command{
	Use:   "down",
	Short: "停止 frpc",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.StopRelay()
	},
}

// relayRuleSource / relayRuleTarget 已下沉到 internal/ops，供 CLI 与 GUI 共用
func relayRuleSource(r config.RelayRule) string {
	return ops.RelayRuleSource(r)
}

func relayRuleTarget(server string, r config.RelayRule) string {
	return ops.RelayRuleTarget(server, r)
}

// relayReloadHint：规则改动后提醒用户重启 frpc。
// 规则是启动时读进 frpc 的，正在跑的进程不会自动感知文件变化
func relayReloadHint() {
	if daemon.RelayRunning() {
		fmt.Println("提示: 执行 flare relay down && flare relay up 让改动生效")
	}
}

func init() {
	relayInitCmd.Flags().StringVar(&relayInitToken, "token", "", "frps 的 auth.token（服务器没开鉴权可省略）")
	relayAddCmd.Flags().StringVar(&relayAddProto, "proto", "tcp", "协议类型：tcp 或 udp")
	relayAddCmd.Flags().StringVar(&relayAddIP, "ip", "127.0.0.1", "本地服务所在 IP")
	relayAddCmd.Flags().IntVar(&relayAddLocal, "local", 0, "本地端口（必填）")
	relayAddCmd.Flags().IntVar(&relayAddRemote, "remote", 0, "服务器上的公网端口（省略则由服务器分配）")
	relayAddCmd.Flags().BoolVar(&relayAddForce, "force", false, "同名规则已存在时覆盖它")
	relayAddCmd.MarkFlagRequired("local")

	relayCmd.AddCommand(relayInitCmd, relayAddCmd, relayRemoveCmd, relayStatusCmd, relayUpCmd, relayDownCmd)
	systemCmd.AddCommand(relayCmd)
}
