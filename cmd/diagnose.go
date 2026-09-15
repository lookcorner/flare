package cmd

import (
	"encoding/json"
	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/log"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var diagnoseJSON bool

// diagnoseCmd：一条命令回答"整条 Cloud 链路哪一段断了"。
// 前面几段（二进制、进程、API、路由）是分头检查的，出问题时用户得自己想先查哪，
// 这里把它们按链路顺序摆在一张表里
var diagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "诊断 Cloud 模式链路连通性",
	Long: "依次检测 cloudflared 二进制与进程、Cloudflare API 连通性与认证、\n" +
		"每条路由的本地服务、域名解析、云端 DNS 记录和 HTTPS 可达性。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// 本地配置里的路由表就是诊断对象（云端 ingress 由 up 推送，是另一个视角）
		routes := make([]daemon.RouteInput, 0, len(cfg.Routes))
		for _, r := range cfg.Routes {
			routes = append(routes, daemon.RouteInput{
				Name:     r.Name,
				Hostname: r.Hostname,
				Service:  r.Service,
				ZoneID:   r.ZoneID,
			})
		}

		result := daemon.Diagnose(routes, cfg.Auth.ApiToken, cfg.Auth.AccountID)
		log.Debugf("Cloud 链路诊断: %d 条路由, %d 通 / %d 断（cloudflared 运行中=%v, API 可达=%v）",
			result.Total, result.Passed, result.Failed, result.Cloudflared.Running, result.API.Reachable)

		if diagnoseJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		printDiagnose(result)
		return nil
	},
}

func printDiagnose(r daemon.DiagnoseResult) {
	fmt.Println("Cloud 链路诊断")
	fmt.Println("==============")

	// cloudflared：装没装、装在哪、跑没跑
	c := r.Cloudflared
	switch {
	case c.Installed && c.Version != "":
		fmt.Printf("cloudflared: ✓ 已安装 (%s)\n", c.Version)
	case c.Installed:
		fmt.Println("cloudflared: ✓ 已安装（版本未知，执行 version 失败）")
	default:
		fmt.Printf("cloudflared: ✗ 未安装（%s，flare up 会自动下载）\n", c.Path)
	}
	if c.Installed {
		fmt.Printf("  路径: %s\n", c.Path)
		if c.Running {
			fmt.Printf("  进程: 运行中 (PID: %d)\n", c.PID)
		} else {
			fmt.Println("  进程: 未运行（flare up 启动）")
		}
	}

	// API：网络可达是一回事，令牌能用是另一回事，分开说
	a := r.API
	switch {
	case !a.Reachable:
		fmt.Printf("Cloudflare API: ✗ %s\n", a.Err)
	case a.Authed:
		fmt.Printf("Cloudflare API: ✓ 可达 (%dms)，认证有效（账户下 %d 个域名）\n", a.LatencyMS, a.Zones)
	default:
		fmt.Printf("Cloudflare API: ✓ 可达 (%dms)\n", a.LatencyMS)
		fmt.Printf("  认证: ✗ %s\n", a.Err)
	}

	if len(r.Routes) == 0 {
		fmt.Println("\n尚无路由（flare add <名称> <端口> --domain <域名>）")
		return
	}

	// 路由表：一列一段链路，从左往右正好是请求依次经过的环节
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "路由\t域名\t本地服务\tDNS\tHTTPS\t云端记录")
	fmt.Fprintln(w, "----\t----\t--------\t---\t-----\t--------")
	for _, route := range r.Routes {
		local := "✓"
		if !route.LocalOK {
			local = "✗ " + route.LocalErr
		}
		dns := "✓"
		if !route.DNSOK {
			dns = "✗ " + route.DNSErr
		}
		https := "-"
		switch {
		case route.HTTPOK && route.HTTPStatus > 0:
			https = fmt.Sprintf("✓ %d", route.HTTPStatus)
		case route.HTTPOK:
			https = "✓"
		case route.HTTPErr != "":
			https = "✗ " + route.HTTPErr
		}
		// 没查（无认证/无 zone_id）和查了没有是两回事，表格里也要分得开
		record := "-"
		if route.RecordChecked {
			record = "✓"
			if !route.RecordOK {
				record = "✗ " + route.RecordErr
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			route.Name, route.Hostname, local, dns, https, record)
	}
	w.Flush()

	fmt.Printf("\n结果: %d 条路由, %d 通 / %d 断\n", r.Total, r.Passed, r.Failed)
	// 隧道没跑时失败是必然的：与其让用户逐条猜，不如把最可能的下一步直接说出来
	if r.Failed > 0 && !r.Cloudflared.Running {
		fmt.Println("提示: cloudflared 未运行，公网访问必然失败，先执行 flare up")
	}
}

func init() {
	diagnoseCmd.Flags().BoolVar(&diagnoseJSON, "json", false, "JSON 格式输出")
	systemCmd.AddCommand(diagnoseCmd)
}
