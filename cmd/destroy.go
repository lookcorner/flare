package cmd

import (
	"flare/internal/cfapi"
	"flare/internal/config"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var destroyBool bool
var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "删除隧道（清理所有 DNS 记录 + 删除隧道）",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return nil
		}
		if cfg.Tunnel.ID == "" {
			return fmt.Errorf("本地没有隧道记录（无需销毁）")
		}
		// 毁灭性操作：非 -y 必须人工输入确认
		if !destroyBool {
			fmt.Printf("将永久删除隧道 %s (%s) 及其全部 %d 条路由。输入隧道名称确认: ",
				cfg.Tunnel.Name, cfg.Tunnel.ID, len(cfg.Routes))
			if strings.TrimSpace(readLine()) != cfg.Tunnel.Name {
				return fmt.Errorf("已取消")
			}
		}
		client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
		//逐条删 DNS 记录（add 存下的凭据派上用场）
		for _, r := range cfg.Routes {
			if r.DNSRecordID == "" {
				continue
			}
			if err := client.DeleteDNSRecord(r.ZoneID, r.DNSRecordID); err != nil {
				fmt.Printf("警告: 删除 DNS 记录 %s 失败（%v），可重跑本命令\n", r.Hostname, err)
				// 出错不中断，把能处理的处理掉。剩下的靠重跑幂等兜底
			} else {
				fmt.Printf("已删除 DNS 记录 %s\n", r.Hostname)
			}
		}
		// 删除云端隧道本体
		if err := client.DeleteTunnel(cfg.Tunnel.ID); err != nil {
			return err // 到这里若失败，DNS 已删但隧道还在——重跑 destroy 即可（幂等）
		}
		fmt.Println("已删除云端隧道")

		// 清空本地（保留 auth：重新 create 不需要重新 init）
		cfg.Tunnel = config.TunnelConfig{}
		cfg.Routes = nil
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Println("本地配置已清空")
		return nil
	},
}

func init() {
	destroyCmd.Flags().BoolVarP(&destroyBool, "yes", "y", false, "跳过确认")
	systemCmd.AddCommand(destroyCmd)
}
