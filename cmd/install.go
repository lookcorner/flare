package cmd

import (
	"fmt"

	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/download"
	"flare/internal/service"

	"github.com/spf13/cobra"
)

// installCmd 把 cloudflared 注册成开机自启的系统服务（macOS LaunchAgent / systemd 用户单位 / Windows 服务）。
// 和 flare up 的区别：up 起的进程归 flare 管，终端一关、机器一重启就没了；
// install 之后进程归操作系统管，重启也会自己回来。
var installCmd = &cobra.Command{
	Use:   "install",
	Short: "注册为系统服务（开机自启）",
	Long: "把 cloudflared 交给操作系统托管，机器重启后隧道自动恢复。\n" +
		"注册后不要再执行 flare up（会起第二个进程），要停掉它用 flare uninstall。",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 便携模式（数据目录跟着 exe 走）下路径会随 U 盘的挂载位置变化，
		// 注册进服务里的绝对路径下次就失效了，所以这里直接拦住
		if config.PortTable() {
			return fmt.Errorf("便携模式下不支持注册系统服务（路径不固定），请改用 flare up 手动启动")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Tunnel.Token == "" {
			return fmt.Errorf("未找到隧道 Token：请先执行 flare init && flare create <名称>，确认 flare up 能连上再回来 install")
		}
		if daemon.Running() {
			// 不是错误，只是提醒：服务启动时系统会另起一个 cloudflared，
			// 同一个隧道跑两份连接会互相抢流量
			fmt.Printf("提示: cloudflared 正在运行 (PID: %d)，建议先执行 flare down 再注册\n", daemon.Pid())
		}
		binPath, err := download.Download()
		if err != nil {
			return err
		}
		svc := service.New()
		if svc.Running() {
			fmt.Println("检测到已注册过同名服务，将覆盖并重新加载")
		}
		if err := svc.Install(binPath, cfg.Tunnel.Token); err != nil {
			return fmt.Errorf("注册系统服务失败: %w", err)
		}
		fmt.Println("隧道已交给系统托管：机器重启后自动恢复连接")
		fmt.Println("提示: 之后不要再用 flare up（会起第二个进程）；要停掉它用 flare uninstall")
		return nil
	},
}

// uninstallCmd 撤销 install：停掉并删除系统服务。不影响 flare 自己的配置文件与日志
var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "卸载系统服务（取消开机自启）",
	Long:  "停掉并删除 install 注册的系统服务。数据和配置都留着，随时可以用 flare up 再跑起来。",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := service.New()
		if err := svc.Uninstall(); err != nil {
			return fmt.Errorf("卸载系统服务失败: %w", err)
		}
		fmt.Println("已取消开机自启")
		fmt.Println("提示: 想继续用隧道就执行 flare up（这次由 flare 自己管进程）")
		return nil
	},
}

func init() {
	systemCmd.AddCommand(installCmd, uninstallCmd)
}
