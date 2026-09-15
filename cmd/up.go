package cmd

import (
	"encoding/hex"
	"flare/internal/authproxy"
	"flare/internal/cfapi"
	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/log"
	"flare/internal/netutil"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var upToken string

var upCmd = &cobra.Command{
	Use:   "up",
	Short: "后台启动隧道",
	RunE: func(cmd *cobra.Command, args []string) error { //后台启动命令
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Tunnel.Token == "" {
			return fmt.Errorf("未找到隧道 Token：请先 flare init && flare create <名称>")
		}
		// --token 优先级最高：临时指定/切换隧道时不必改配置文件
		if upToken != "" {
			cfg.Tunnel.Token = strings.TrimSpace(upToken)
		}

		//为所有带 Auth 的路由启动登录网关
		var proxies []*authproxy.Proxy
		for i := range cfg.Routes {
			r := cfg.Routes[i]
			// 没配密码保护的路由直接走原地址，跳过
			if r.Auth == nil {
				continue
			}
			sigKey, err := hex.DecodeString(r.Auth.SigningKey) // hex 还原回字节
			if err != nil {
				return fmt.Errorf("路由 %s 的 signing_key 无效: %w", r.Name, err)
			}
			port := netutil.ExtractPort(r.Service) // 从 "http://localhost:3000" 取 "3000"
			if port == "" {
				return fmt.Errorf("路由 %s 的 service 格式无效: %s", r.Name, r.Service)
			}
			proxy, err := authproxy.New(authproxy.Config{
				Username:   r.Auth.Username,
				Password:   r.Auth.Password,
				TargetPort: port,
				SigningKey: sigKey, // 固定的持久密钥：重启后 Cookie 仍有效
				CookieTTL:  time.Duration(r.Auth.CookieTTLOrDefault()) * time.Second,
			})

			if err != nil {
				return fmt.Errorf("路由 %s 启动鉴权代理失败: %w", r.Name, err)
			}
			if err := proxy.Start(); err != nil {
				return err
			}
			proxies = append(proxies, proxy)
			// 内存中把该路由的 service 改写为网关地址——仅本次运行生效，不落盘
			cfg.Routes[i].Service = "http://localhost:" + strconv.Itoa(proxy.ListenPort())
			fmt.Printf("鉴权网关已启动: %s → 127.0.0.1:%d → 127.0.0.1:%s\n",
				r.Hostname, proxy.ListenPort(), port)
		}
		// 无论 up 后续成功失败，退出前关掉所有网关
		defer func() {
			for _, p := range proxies {
				p.Stop()
			}
		}()
		// 启动前先同步 ingress：本地路由表可能比云端新（add 后没 up 过）或旧
		if len(cfg.Routes) > 0 {
			client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)
			if err := pushIngress(client, cfg); err != nil {
				// 同步失败不阻断启动：cloudflared 会加载云端现有配置，
				// 最多是规则旧一点。进日志文件（事后能查）+ 终端提醒用户
				log.Warnf("同步 ingress 失败: %v（将使用云端现有配置）", err)
			} else {
				fmt.Println("ingress 配置已同步")
			}
		}

		return daemon.Start(cfg.Tunnel.Token)
	},
}

func init() {
	upCmd.Flags().StringVar(&upToken, "token", "", "隧道token")
	systemCmd.AddCommand(upCmd)
}
