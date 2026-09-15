package cmd

import (
	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/log"
	"fmt"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看隧道和中继运行状态",
	RunE: func(cmd *cobra.Command, args []string) error {
		if daemon.Running() {
			fmt.Printf("cloudflared 运行中 (PID: %d)\n", daemon.Pid())
		} else {
			fmt.Println("cloudflared 未运行")
		}
		// 中继（frpc）和隧道是两件独立的事：只有配过才提，免得没用它的人看着糊涂。
		// 配置读不出来（版本过高、内容坏了）也要说一声：status 正是出问题时才跑的命令，
		// 不能让用户对着一句"cloudflared 未运行"猜真正的毛病在哪
		cfg, err := config.Load()
		if err != nil {
			log.Warnf("读取配置失败: %v", err)
		} else if cfg.Relay.Server != "" {
			if daemon.RelayRunning() {
				fmt.Printf("frpc 运行中 (PID: %d) → %s\n", daemon.RelayPid(), cfg.Relay.Server)
			} else {
				fmt.Printf("frpc 未运行（中继服务器 %s）\n", cfg.Relay.Server)
			}
		}
		// 数据放哪、哪种模式：出问题时用户第一个要问的就是这个
		fmt.Println(dataDirNote())
		return nil

	},
}

func init() {
	systemCmd.AddCommand(statusCmd)
}
