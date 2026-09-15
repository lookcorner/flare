package cmd

import (
	"flare/internal/config"
	"fmt"

	"github.com/spf13/cobra"
)

// listCmd：把本地路由表打印出来。
// 纯读本地配置、不碰云端，所以它也能在断网时回答"我到底配了哪些域名"
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "列出所有路由",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if len(cfg.Routes) == 0 {
			fmt.Println("尚无路由（flare add <名称> <端口> --domain <域名>）")
			return nil
		}
		fmt.Printf("%-14s %-28s %-26s %s\n", "名称", "域名", "本地服务", "密码保护")
		for _, r := range cfg.Routes {
			auth := "-"
			if r.Auth != nil {
				auth = "已开启 (" + r.Auth.Username + ")"
			}
			fmt.Printf("%-14s %-28s %-26s %s\n", r.Name, r.Hostname, r.Service, auth)
		}
		return nil
	},
}

func init() {
	systemCmd.AddCommand(listCmd)
}
