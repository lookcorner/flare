package cmd

import (
	"flare/internal/cfapi"
	"flare/internal/config"
	"fmt"

	"github.com/spf13/cobra"
)

var removeCmd = &cobra.Command{
	Use:   "remove [名称]",
	Short: "删除路由(清理dns + ingress)",
	// Args 校验命令行参数个数：ExactArgs(1) 表示必须恰好传 1 个参数，
	// 即 "flare remove <名称>"；不传或传多个都会直接报错
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		route := cfg.FindRoute(args[0])
		if route == nil {
			return fmt.Errorf("路由 %s 不存在（flare list 可查看全部路由）", args[0])
		}
		client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)

		// 先删云端 DNS 记录（凭 add 时存下的 DNSRecordID）。
		// 失败即停：本地保持原样，重跑即可 —— 不会出现"本地没了云上还有"的谜案
		if route.DNSRecordID != "" {
			if err := client.DeleteDNSRecord(route.ZoneID, route.DNSRecordID); err != nil {
				return err
			}
			fmt.Printf("已删除 DNS 记录 %s\n", route.Hostname)
		}
		//本地移除 + 推送剩余路由的 ingress（catch-all 404 依然兜底）
		cfg.RemoveRoute(args[0])
		if err := pushIngress(client, cfg); err != nil {
			// 推送失败：DNS 已删、本地已删，只剩云端规则表多一条——
			// 但 config.yml 已不含它，下次任何 add/remove/up 推送都会把它冲掉，可自愈
			fmt.Printf("警告: ingress 同步失败（%v），本地已移除，下次 add/up 会自动修正\n", err)
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("路由已删除: %s\n", args[0])
		return nil
	},
}

func init() {
	systemCmd.AddCommand(removeCmd)
}
