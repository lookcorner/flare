package cmd

import (
	"flare/internal/log"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

var logLines int
var logFollow bool

// logCmd：看日志。四个组件的日志分开放，所以这里要挑一个：
//   - flare        本程序自己干了什么（默认）
//   - cloudflared  HTTP 隧道客户端的输出
//   - frpc         中继客户端（relay）的输出
//   - frps         中继服务端（relay server）的输出
//
// 后三个都是后台进程，终端里看不到它们的输出，只能来这里看
var logCmd = &cobra.Command{
	Use:   "log [组件]",
	Short: "查看日志（flare / cloudflared / frpc / frps）",
	Long: "组件可选 flare（默认）/ cloudflared / frpc / frps。\n" +
		"-f 实时跟踪，等价 tail -f。\n" +
		"日志级别用环境变量 FLARE_LOG_LEVEL 调：debug / info / warn / error。",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		comp := "flare"
		if len(args) == 1 {
			comp = args[0]
		}
		// 校验组件名：手滑写错一个名字，不校验就会显示“还没有日志”，
		// 让人以为日志功能坏了
		switch comp {
		case "flare", "cloudflared", "frpc", "frps":
		default:
			return fmt.Errorf("未知组件 %q，可选: flare / cloudflared / frpc / frps", comp)
		}

		path := log.Path(comp)
		lines, err := log.Tail(comp, logLines)
		if err != nil {
			fmt.Printf("还没有 %s 的日志（%s）\n", comp, path)
			return nil
		}
		for _, l := range lines {
			fmt.Println(l)
		}
		if !logFollow {
			return nil
		}

		fmt.Printf("--- 实时跟踪 %s（Ctrl+C 退出）---\n", path)
		// 用信号构造 context：Ctrl+C 时 Done() 被关闭，Follow 收到就返回
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return log.Follow(comp, os.Stdout, ctx.Done())
	},
}

func init() {
	logCmd.Flags().IntVarP(&logLines, "lines", "n", 50, "显示最后多少行")
	logCmd.Flags().BoolVarP(&logFollow, "follow", "f", false, "实时跟踪新增日志")
	systemCmd.AddCommand(logCmd)
}
