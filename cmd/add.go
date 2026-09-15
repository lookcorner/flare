package cmd

import (
	"encoding/hex"
	"flare/internal/authproxy"
	"flare/internal/cfapi"
	"flare/internal/config"
	"fmt"

	"github.com/spf13/cobra"
)

var addDomain, addAuth string

var addCmd = &cobra.Command{
	Use:   "add [名称] [端口]",
	Short: "添加路由（创建 CNAME + 更新 ingress）",
	// 不给参数校验的话 args[0]/args[1] 直接越界 panic，报错应该是给人的，不是崩堆栈
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, port := args[0], args[1]
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("加载配置出错 %s", err)
		}
		// 前置校验：必须先 create（有隧道）且名字不重（本地表里查）
		if cfg.Tunnel.ID == "" {
			return fmt.Errorf("请先运行 flare init && flare create <名称>")
		}

		if cfg.FindRoute(name) != nil {
			return fmt.Errorf("路由 %s 已存在", name)
		}

		client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
		//把域名归到zone 里面
		zone, err := client.FindZoneForDomain(addDomain)
		if err != nil {
			return fmt.Errorf("查询 %s 所属 Zone 失败: %w", addDomain, err)
		}
		// 域名不在本账户下时返回 nil，不拦住的话下一行就空指针了
		if zone == nil {
			return fmt.Errorf("在账户下找不到 %s 所属的 Zone，请确认域名已接入该 Cloudflare 账户", addDomain)
		}
		// 查 DNS 记录是否已存在：记录已存在 = 之前跑过 add（可能上次推送失败）
		// 更新目标而非创建 —— 这让 add 可安全重跑（幂等）
		target := cfg.Tunnel.ID + ".cfargotunnel.com"

		existing, err := client.FindDNSRecord(zone.ID, addDomain)
		if err != nil {
			return err
		}
		var recordId string
		if existing != "" {
			fmt.Printf("DNS 记录已存在，正在更新 %s → %s\n", addDomain, target)
			if err := client.UpdateCNAME(zone.ID, existing, addDomain, target); err != nil {
				return err
			}
			recordId = existing
		} else {
			fmt.Printf("正在创建 DNS 记录 %s → %s\n", addDomain, target)
			recordId, err = client.CreateCNAME(zone.ID, addDomain, target)
			if err != nil {
				return err
			}

		}
		//本地落盘：记住云端资源的 ID（ZoneID + DNSRecordID），remove 靠它清理
		route := config.RouteConfig{
			Name:        name,
			Hostname:    addDomain,
			Service:     "http://localhost:" + port,
			ZoneID:      zone.ID,
			DNSRecordID: recordId,
		}

		if addAuth != "" {
			user, pwd, err := parseAuth(addAuth)
			if err != nil {
				return err
			}
			route.Auth = &config.AuthProxy{
				Username:   user,
				Password:   pwd,
				SigningKey: hex.EncodeToString(authproxy.RandomKey()), // 持久化！
			}
			fmt.Printf("已启用密码保护: %s\n", addDomain)
		}
		cfg.Routes = append(cfg.Routes, route)
		if err := config.Save(cfg); err != nil {
			return err
		}
		// 推全量 ingress 到云端（隧道真正开始路由这张表）
		if err := pushIngress(client, cfg); err != nil {
			// DNS 记录已建好、本地已存——只有 ingress 没推上去。
			// 文案必须告诉用户"已做了什么、重跑会怎样"
			return fmt.Errorf("推送 ingress 失败: %w\nDNS 记录已创建，直接重跑本命令即可修复（会自动更新）", err)
		}
		fmt.Printf("路由已添加: %s → %s (%s)\n", addDomain, "http://localhost:"+port, name)
		return nil
	},
}

func init() {
	addCmd.Flags().StringVar(&addDomain, "domain", "", "完整域名 (如 web.example.com)")
	addCmd.Flags().StringVar(&addAuth, "auth", "", "启用密码保护 (格式: 用户名:密码)")
	addCmd.MarkFlagRequired("domain") // 缺 --domain 直接报错，别等 RunE
	systemCmd.AddCommand(addCmd)
}
