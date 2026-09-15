package cmd

import (
	"bufio"
	"encoding/hex"
	"flare/internal/authproxy"
	"flare/internal/cfapi"
	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/log"
	"flare/internal/ops"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// wizardDomain / wizardPort / wizardName / wizardAuth 是四个旗标的目标变量。
// 声明为包级变量：旗标解析发生在 RunE 之前，而 RunE 是闭包，只能靠包级变量取值。
// 给了旗标的项不再问用户，这样它也能当"半自动"命令用
var (
	wizardDomain string
	wizardPort   string
	wizardName   string
	wizardAuth   string
)

// wizardCmd：一条命令走完初始配置。
// 为什么需要它：flare 的基本流程是 init → create → add → up，四步各自都有参数要记。
// 对第一次上手的人，向导把"缺什么问什么"做掉，比读四段 help 快得多
var wizardCmd = &cobra.Command{
	Use:   "wizard",
	Short: "交互式引导，一条命令走完初始配置",
	Long: "按顺序问清必需的信息，然后直接干活：配置认证 → 创建隧道 → 启动隧道 → 添加路由（→ 可选的中继配置）。\n" +
		"已经做过的步骤会自动跳过：init 过就不再问令牌，已有隧道就直接加路由。\n" +
		"旗标给了的项不再问；问题后面的括号里是回车时的默认值。\n\n" +
		"示例:\n" +
		"  flare wizard                                                    # 全程交互\n" +
		"  flare wizard --domain chat.example.com --port 8080               # 路由信息直接给\n" +
		"  flare wizard --domain m.example.com --port 3000 --auth alice:pw  # 顺带开密码保护",
	RunE: runWizard,
}

func init() {
	wizardCmd.Flags().StringVar(&wizardDomain, "domain", "", "完整域名（如 chat.example.com）")
	wizardCmd.Flags().StringVar(&wizardPort, "port", "", "本地服务端口")
	wizardCmd.Flags().StringVar(&wizardName, "name", "", "路由名称（默认取域名第一段）")
	wizardCmd.Flags().StringVar(&wizardAuth, "auth", "", "密码保护（格式: 用户名:密码）")
	systemCmd.AddCommand(wizardCmd)
}

func runWizard(cmd *cobra.Command, args []string) error {
	fmt.Println("flare 向导: 认证 → 隧道 → 路由（→ 中继）")
	fmt.Println()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	steps := &stepNo{}
	in := newWizardInput()

	if err := wizardEnsureAuth(steps, in, cfg); err != nil {
		return err
	}
	if err := wizardEnsureTunnel(steps, in, cfg); err != nil {
		return err
	}
	wizardStartTunnel(steps, cfg)
	route, err := wizardAddRoute(steps, in, cfg)
	if err != nil {
		return err
	}
	// 路由参数是旗标给全的（非交互）就不问中继：那种用法多半在脚本里，
	// 突然停下来等一个回车会很难查
	interactive := strings.TrimSpace(wizardDomain) == "" || strings.TrimSpace(wizardPort) == ""
	if err := wizardMaybeRelay(steps, in, cfg, interactive); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("全部完成 ✓")
	fmt.Printf("外网访问: https://%s → %s\n", route.Hostname, route.Service)
	fmt.Println("接着可以: flare status 看运行状态，flare list 看全部路由")
	fmt.Println(dataDirNote())
	return nil
}

// wizardEnsureAuth 保证配置里有可用的 API 令牌和账户 ID：缺什么问什么。
// 已经有的（包括环境变量 FLARE_API_TOKEN / FLARE_ACCOUNT_ID 给的）不再打扰用户
func wizardEnsureAuth(steps *stepNo, in *wizardInput, cfg *config.Config) error {
	if cfg.Auth.ApiToken != "" && cfg.Auth.AccountID != "" {
		fmt.Println("✓ 认证信息已配置（跳过）")
		return nil
	}
	steps.next("配置 Cloudflare 认证信息")
	if cfg.Auth.ApiToken == "" {
		fmt.Println("  API 令牌在 https://dash.cloudflare.com/profile/api-tokens 创建（需要 Tunnel 编辑 + DNS 编辑权限）")
		cfg.Auth.ApiToken = in.ask("  粘贴 API 令牌: ")
	}
	if cfg.Auth.AccountID == "" {
		fmt.Println("  账户 ID 是 Cloudflare 控制台里的 32 位字符")
		cfg.Auth.AccountID = in.ask("  粘贴账户 ID: ")
	}
	if cfg.Auth.ApiToken == "" || cfg.Auth.AccountID == "" {
		return fmt.Errorf("API 令牌和账户 ID 都必须填写（认证信息不全，后面每一步都做不了）")
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Println("✓ 认证信息已保存")
	log.Auditf("wizard: 认证信息已保存")
	return nil
}

// wizardEnsureTunnel 保证本地有一条能用的隧道：没有就建，有 ID 没凭据就补取。
// 这段逻辑与 flare create 保持一致（先落盘身份、再取凭据），
// 所以中途失败重跑不会建出两条隧道
func wizardEnsureTunnel(steps *stepNo, in *wizardInput, cfg *config.Config) error {
	if cfg.Tunnel.ID != "" && cfg.Tunnel.Token != "" {
		fmt.Printf("✓ 已有隧道 %s (%s)（跳过创建）\n", cfg.Tunnel.Name, cfg.Tunnel.ID)
		return nil
	}
	client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)

	if cfg.Tunnel.ID != "" {
		// 上次建了隧道却没拿到凭据：补取回来，别又建一条
		fmt.Printf("隧道 %s 已在本地记录，补取运行凭据...\n", cfg.Tunnel.Name)
		token, err := client.GetTunnelToken(cfg.Tunnel.ID)
		if err != nil {
			return fmt.Errorf("补取隧道凭据失败: %w", err)
		}
		cfg.Tunnel.Token = token
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Println("✓ 隧道凭据已补回")
		return nil
	}

	steps.next("创建隧道")
	name := in.ask("  隧道名称（回车用 flare）: ")
	if name == "" {
		name = "flare"
		fmt.Printf("  使用默认名称: %s\n", name)
	}
	fmt.Printf("正在创建隧道 %s ...\n", name)
	tunnel, err := client.CreateTunnel(name)
	if err != nil {
		return err
	}
	// 先落盘隧道身份，再取凭据：取凭据失败时本地也知道"云端已经有这条隧道了"，
	// 重跑 wizard 会走上面的补取分支，而不是又建一条
	cfg.Tunnel = config.TunnelConfig{ID: tunnel.ID, Name: tunnel.Name}
	if err := config.Save(cfg); err != nil {
		return err
	}
	token, err := client.GetTunnelToken(tunnel.ID)
	if err != nil {
		return fmt.Errorf("隧道已创建并记录到本地，但获取凭据失败: %w\n重跑 flare wizard 可补取凭据（不会重复创建隧道）", err)
	}
	cfg.Tunnel.Token = token
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("✓ 隧道创建成功: %s (%s)\n", tunnel.Name, tunnel.ID)
	log.Auditf("wizard: 创建隧道 %s (%s)", tunnel.Name, tunnel.ID)
	return nil
}

// wizardStartTunnel 让 cloudflared 跑起来。失败不中断流程：隧道配置已经好了，
// 用户稍后自己 flare up 一样能起来 —— 向导不该为一件可补救的事白问一路
func wizardStartTunnel(steps *stepNo, cfg *config.Config) {
	if daemon.Running() {
		fmt.Printf("✓ cloudflared 已在运行 (PID: %d)（跳过启动）\n", daemon.Pid())
		return
	}
	steps.next("启动隧道")
	fmt.Println("  首次运行会下载 cloudflared，需要一点时间...")
	if err := daemon.Start(cfg.Tunnel.Token); err != nil {
		log.Warnf("隧道启动失败: %v（稍后可执行 flare up 重试）", err)
		return
	}
	fmt.Println("✓ 隧道已启动")
}

// wizardAddRoute 配好一条"域名 → 本地端口"的路由（CNAME + ingress）。
// 顺序与 flare add 完全一致，所以重复跑是幂等的（记录已存在就更新目标）
func wizardAddRoute(steps *stepNo, in *wizardInput, cfg *config.Config) (config.RouteConfig, error) {
	var route config.RouteConfig
	domain := strings.TrimSpace(wizardDomain)
	port := strings.TrimSpace(wizardPort)
	routeName := strings.TrimSpace(wizardName)

	// 交互式向导：域名/端口这类必需信息缺了就问。
	// 另一个用途是决定"要不要问可选问题"（密码保护）——都从旗标来就不问
	interactive := domain == "" || port == ""
	steps.next("添加路由")
	if domain == "" {
		domain = in.ask("  完整域名（如 chat.example.com）: ")
	}
	if port == "" {
		port = in.ask("  本地端口（如 8080）: ")
	}
	// 校验放在提问之后：空值在这里一次性拦住，别让人答完一串问题才被告知第一项没填
	if domain == "" {
		return route, fmt.Errorf("域名不能为空（也可以下次用 flare wizard --domain <域名> --port <端口>）")
	}
	if _, err := ops.ParsePortNumber(port); err != nil {
		return route, err
	}
	// 路由名：--name > 交互输入 > 域名第一段（chat.example.com → chat）
	if routeName == "" && interactive {
		routeName = in.ask(fmt.Sprintf("  路由名称（回车用 %s）: ", ops.RouteNameFromDomain(domain)))
	}
	if routeName == "" {
		routeName = ops.RouteNameFromDomain(domain)
	}
	if cfg.FindRoute(routeName) != nil {
		return route, fmt.Errorf("路由 %s 已存在（flare list 可查看全部；换名字用 --name，或先 flare remove %s）", routeName, routeName)
	}
	// 密码保护：--auth 给了就用；没给且在做交互向导时问一句。
	// 命令行模式下不问 —— 没给 --auth 就是不要
	authSpec := strings.TrimSpace(wizardAuth)
	if authSpec == "" && interactive && in.confirm("  给这个域名加密码保护吗？(y/N): ") {
		authSpec = in.ask("  用户名:密码: ")
		if authSpec == "" {
			fmt.Println("  没填写，跳过密码保护")
		}
	}

	service := "http://localhost:" + port
	target := cfg.Tunnel.ID + ".cfargotunnel.com"
	client := cfapi.New(cfg.Auth.ApiToken, cfg.Auth.AccountID)

	zone, err := client.FindZoneForDomain(domain)
	if err != nil {
		return route, fmt.Errorf("查询 %s 所属 Zone 失败: %w", domain, err)
	}
	if zone == nil {
		return route, fmt.Errorf("账户下找不到 %s 所属的 Zone，请确认域名已接入该 Cloudflare 账户", domain)
	}
	// 记录已存在就更新目标：让重复跑向导（比如上次卡在推送 ingress）能自愈
	recordID, err := client.FindDNSRecord(zone.ID, domain)
	if err != nil {
		return route, err
	}
	if recordID != "" {
		fmt.Printf("DNS 记录已存在，正在更新 %s → %s\n", domain, target)
		if err := client.UpdateCNAME(zone.ID, recordID, domain, target); err != nil {
			return route, err
		}
	} else {
		fmt.Printf("正在创建 DNS 记录 %s → %s\n", domain, target)
		recordID, err = client.CreateCNAME(zone.ID, domain, target)
		if err != nil {
			return route, err
		}
	}

	route = config.RouteConfig{
		Name:        routeName,
		Hostname:    domain,
		Service:     service,
		ZoneID:      zone.ID,
		DNSRecordID: recordID,
	}
	if authSpec != "" {
		user, pwd, err := parseAuth(authSpec)
		if err != nil {
			return route, err
		}
		route.Auth = &config.AuthProxy{
			Username: user,
			Password: pwd,
			// 签名密钥必须持久化：不存的话每次 up 都换一把，重启后所有登录 Cookie 失效
			SigningKey: hex.EncodeToString(authproxy.RandomKey()),
		}
	}
	cfg.Routes = append(cfg.Routes, route)
	if err := config.Save(cfg); err != nil {
		return route, err
	}
	if err := pushIngress(client, cfg); err != nil {
		// DNS 已建、本地已存，只有 ingress 没推上去：说清"重跑就能修"
		return route, fmt.Errorf("推送 ingress 失败: %w\nDNS 记录已创建，直接重跑本命令即可修复（会自动更新）", err)
	}
	fmt.Printf("✓ 路由已添加: %s → %s（名称 %s）\n", domain, service, routeName)
	if route.Auth != nil {
		fmt.Printf("  已开启密码保护（用户名 %s）\n", route.Auth.Username)
	}
	log.Auditf("wizard: 添加路由 %s → %s（名称 %s）", domain, service, routeName)
	return route, nil
}

// wizardMaybeRelay 问一句要不要配中继（frps）。
// 中继是另一套东西（TCP/UDP，还得有用户自己的服务器），所以：
//   - 只在交互式向导里问
//   - 已经配过就直接跳过
func wizardMaybeRelay(steps *stepNo, in *wizardInput, cfg *config.Config, interactive bool) error {
	if cfg.Relay.Server != "" {
		fmt.Printf("✓ 中继已配置: %s（跳过）\n", cfg.Relay.Server)
		return nil
	}
	if !interactive {
		return nil
	}

	steps.next("中继（可选）")
	fmt.Println("  Cloudflare 隧道只转发 HTTP/HTTPS。要暴露 SSH、数据库、游戏服这类")
	fmt.Println("  TCP/UDP 服务，需要一台自己的 frps 服务器；没有就跳过这一步")
	if !in.confirm("  现在配置中继吗？(y/N): ") {
		fmt.Println("  跳过。以后要配: flare relay init <服务器地址:端口>")
		return nil
	}

	server := in.ask("  服务器地址（形如 1.2.3.4:7000）: ")
	if err := ops.CheckRelayAddr(server); err != nil {
		return err
	}
	token := in.ask("  frps 的 auth.token（服务器没开鉴权就直接回车）: ")
	cfg.Relay.Server = server
	cfg.Relay.Token = token
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("✓ 中继服务器已保存: %s\n", server)
	log.Auditf("wizard: 中继服务器已保存 %s", server)

	if !in.confirm("  现在加一条穿透规则吗？(y/N): ") {
		fmt.Println("  加规则: flare relay add <名称> --local <端口>")
		fmt.Println("  启动: flare relay up")
		return nil
	}
	if err := wizardAddRelayRule(in, cfg); err != nil {
		return err
	}
	fmt.Println("  启动: flare relay up")
	return nil
}

// wizardAddRelayRule 问出一条穿透规则存下来。
// 校验规则与 flare relay add 一致：端口写错等 frpc 起不来才发现，排查成本高得多
func wizardAddRelayRule(in *wizardInput, cfg *config.Config) error {
	local, err := ops.ParsePortNumber(in.ask("  本地端口（要暴露的本机端口，如 22）: "))
	if err != nil {
		return err
	}
	proto := strings.ToLower(in.ask("  协议（tcp/udp，回车用 tcp）: "))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("协议只能是 tcp 或 udp，收到: %q", proto)
	}
	name := in.ask(fmt.Sprintf("  规则名称（回车用 %s-%d）: ", proto, local))
	if name == "" {
		name = fmt.Sprintf("%s-%d", proto, local)
	}
	if cfg.FindRelayRule(name) != nil {
		return fmt.Errorf("穿透规则 %s 已存在（flare relay status 可查看全部规则）", name)
	}
	remote := 0
	if s := in.ask("  服务器上的公网端口（回车由服务器分配）: "); s != "" {
		if remote, err = ops.ParsePortNumber(s); err != nil {
			return err
		}
	}

	cfg.Relay.Rules = append(cfg.Relay.Rules, config.RelayRule{
		Name:       name,
		Proto:      proto,
		LocalPort:  local,
		RemotePort: remote,
	})
	if err := config.Save(cfg); err != nil {
		return err
	}
	// 目标端口写在服务器的地址上；remote 为 0 表示由 frps 分配，要说清楚，
	// 否则用户会以为自己漏填了
	host, _, err := net.SplitHostPort(cfg.Relay.Server)
	if err != nil {
		host = cfg.Relay.Server
	}
	target := fmt.Sprintf("%s:%d", host, remote)
	if remote == 0 {
		target = host + ":由服务器分配"
	}
	fmt.Printf("✓ 穿透规则已添加: %s (%s) 127.0.0.1:%d → %s\n", name, proto, local, target)
	fmt.Println("  本机服务不在 127.0.0.1 上时用 flare relay add --ip 指定；规则可加多条")
	log.Auditf("wizard: 添加穿透规则 %s (%s) 127.0.0.1:%d → %s", name, proto, local, target)
	return nil
}

// stepNo 给"第 N 步"编号：跳过的步骤不占号 ——
// 否则用户看到第 4 步却没有第 3 步，会以为中间漏了什么
type stepNo struct{ n int }

func (s *stepNo) next(title string) {
	s.n++
	fmt.Printf("第 %d 步: %s\n", s.n, title)
}

// wizardInput 是向导的输入通道。
// 一次向导共用一个 bufio.Reader：每条问题都新建 reader 的话，它可能提前把后面
// 几行读进自己的缓冲区，下一问就读到空（管道输入时尤其明显）
type wizardInput struct {
	r *bufio.Reader
}

func newWizardInput() *wizardInput { return &wizardInput{r: bufio.NewReader(os.Stdin)} }

// ask 打印提示并读一行（去掉首尾空白）
func (in *wizardInput) ask(prompt string) string {
	fmt.Print(prompt)
	line, _ := in.r.ReadString('\n')
	return strings.TrimSpace(line)
}

// confirm 问一个 y/N 问题：只有 y/yes 算同意
func (in *wizardInput) confirm(prompt string) bool { return confirmYes(in.ask(prompt)) }

// 端口/服务器地址/默认路由名的校验与派生逻辑已下沉到 internal/ops，
// 供 CLI 与桌面 GUI 共用。
