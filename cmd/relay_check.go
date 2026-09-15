package cmd

import (
	"encoding/json"
	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/log"
	"flare/internal/relay"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var relayCheckJSON bool

// relayCheckCmd：中继链路的"哪一段断了"。
// 链路是 本地服务 → frpc → frps 服务器 → 公网端口，任何一段不通，
// 用户看到的都只是"连不上"，所以这里逐段量给他看
var relayCheckCmd = &cobra.Command{
	Use:   "check [规则名]",
	Short: "检测中继链路连通性",
	Long: "检测 frps 服务器、本地服务、远程穿透端口的连通性和延迟。\n" +
		"不指定规则名则检测全部规则。udp 规则无法探测，会单独标注、不计入通/断。",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Relay.Server == "" {
			return fmt.Errorf("未配置中继服务器，请先执行 flare relay init <服务器地址:端口>")
		}
		ruleName := ""
		if len(args) > 0 {
			ruleName = args[0]
			// 提前查一次规则名：否则 Check 会返回 0 条规则，
			// 显示成"暂无规则需要检测"，用户会以为自己没配规则
			if cfg.FindRelayRule(ruleName) == nil {
				return fmt.Errorf("穿透规则 %s 不存在（flare relay status 可查看全部规则）", ruleName)
			}
		}

		result := relay.Check(&cfg.Relay, ruleName, relay.FrpcState{
			Running: daemon.RelayRunning(),
			PID:     daemon.RelayPid(),
		})
		log.Debugf("中继链路检测: %s, %d 条规则, %d 通 / %d 断 / %d 跳过（frpc 运行中=%v）",
			result.Server, result.Total, result.Passed, result.Failed, result.Skipped, result.FrpcRunning)

		if relayCheckJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		printRelayCheckTable(result)
		return nil
	},
}

func printRelayCheckTable(r relay.CheckResult) {
	fmt.Println("中继链路检测")
	fmt.Println("============")

	if r.ServerOK {
		fmt.Printf("服务器: %s  ✓ 可达 (%dms)\n", r.Server, r.ServerLatency)
	} else {
		fmt.Printf("服务器: %s  ✗ 不可达（检查地址与服务器防火墙是否放行）\n", r.Server)
	}
	if r.FrpcRunning {
		fmt.Printf("frpc:   运行中 (PID: %d)\n", r.FrpcPID)
	} else {
		fmt.Println("frpc:   未运行（flare relay up 启动）")
	}
	fmt.Println()

	if len(r.Rules) == 0 {
		fmt.Println("尚无穿透规则（flare relay add <名称> --local <端口>）")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "规则\t协议\t本地端口\t远程端口\t本地服务\t远程穿透\t延迟")
	fmt.Fprintln(w, "----\t----\t--------\t--------\t--------\t--------\t----")
	for _, rule := range r.Rules {
		local, remote, latency := "-", "-", "-"
		if rule.Skipped {
			// udp：探测不了就明说，别让它冒充"不通"，也别冒充"通"
			local = "-（udp 无法探测）"
		} else {
			if rule.LocalOK {
				local = "✓"
			} else {
				local = "✗ " + rule.LocalErr
			}
			if rule.RemotePort > 0 {
				if rule.RemoteOK {
					remote = "✓"
				} else {
					remote = "✗ " + rule.RemoteErr
				}
			}
		}
		if rule.RemoteOK {
			latency = fmt.Sprintf("%dms", rule.LatencyMS)
		}
		remotePort := "-"
		if rule.RemotePort > 0 {
			remotePort = fmt.Sprintf("%d", rule.RemotePort)
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			rule.Name, rule.Proto, rule.LocalPort, remotePort, local, remote, latency)
	}
	w.Flush()

	fmt.Printf("\n结果: %d 条规则, %d 通 / %d 断", r.Total, r.Passed, r.Failed)
	if r.Skipped > 0 {
		fmt.Printf("（另有 %d 条 udp 规则无法探测，未计入）", r.Skipped)
	}
	fmt.Println()

	// 中继最常见的两种断法，各给一句下一步：没跑 frpc、服务没起
	if !r.FrpcRunning {
		fmt.Println("提示: frpc 未运行，远端端口不会转发，先执行 flare relay up")
	} else if r.Failed > 0 {
		fmt.Println("提示: 本地服务不通就先把它起起来；远端端口不通请看服务器防火墙是否放行")
	}
}

func init() {
	relayCheckCmd.Flags().BoolVar(&relayCheckJSON, "json", false, "JSON 格式输出")
	relayCmd.AddCommand(relayCheckCmd)
}
