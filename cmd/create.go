package cmd

import (
	"flare/internal/cfapi"
	"flare/internal/config"
	"fmt"

	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create [名称]",
	Short: "创建隧道",
	// 不给参数校验的话 args[0] 直接越界 panic
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			fmt.Println("加载配置失败:", err)
			return err
		}
		// 状态机校验①：没有认证信息，云端操作无从谈起
		if cfg.Auth.ApiToken == "" {
			return fmt.Errorf("请先运行 flare init 配置认证信息")
		}
		client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)

		// 状态机校验②：已有一条隧道时禁止重复创建——
		// 否则本地配置会指向另一条隧道，旧 DNS 记录全部作废。
		// 唯一的例外：上次 create 建完隧道却没拿到 token（比如网络抖动），
		// 这里补取回来，免得用户又建一条重复的隧道
		if cfg.Tunnel.ID != "" {
			if cfg.Tunnel.Token != "" {
				return fmt.Errorf("已存在隧道 %s，禁止重复创建（想换一条请先 flare destroy）", cfg.Tunnel.Name)
			}
			token, err := client.GetTunnelToken(cfg.Tunnel.ID)
			if err != nil {
				return fmt.Errorf("隧道 %s 已在本地记录，但补取 token 失败: %w", cfg.Tunnel.Name, err)
			}
			cfg.Tunnel.Token = token
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Println("已补回隧道 token:", cfg.Tunnel.Name)
			return nil
		}

		tunnel, err := client.CreateTunnel(args[0])
		if err != nil {
			return err
		}
		// 先落盘隧道身份，再取 token：取 token 失败时本地也知道"云端已经有这条隧道了"，
		// 下次 create 会走上面的补取分支，而不是又建一条
		cfg.Tunnel = config.TunnelConfig{ID: tunnel.ID, Name: tunnel.Name}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Println("隧道创建成功:", tunnel)

		// 取运行凭据并落盘。
		token, err := client.GetTunnelToken(tunnel.ID)
		if err != nil {
			return fmt.Errorf("隧道已创建并记录到本地，但获取 token 失败: %w\n直接重跑 flare create 可补取 token（不会重复创建隧道）", err)
		}

		cfg.Tunnel.Token = token
		if err := config.Save(cfg); err != nil {
			return err
		}

		fmt.Println("\n下一步: flare add <名称> <端口> --domain <域名>")
		return nil
	},
}

func init() {
	systemCmd.AddCommand(createCmd)
}
