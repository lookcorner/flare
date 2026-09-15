package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flare/internal/config"
	"flare/internal/download"
	"flare/internal/log"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// frps（中继服务端）在本机的三个文件，全在数据目录下——
// 与 frpc.toml / frpc.pid 同一套路，安装、卸载都只动这里，不碰 /etc、不要求 root
func frpsConfigPath() string { return filepath.Join(config.Dir(), "frps.toml") }
func frpsPidPath() string    { return filepath.Join(config.Dir(), "frps.pid") }

var relayServerPort int

var relayServerCmd = &cobra.Command{
	Use:   "server",
	Short: "管理中继服务端（frps）",
	Long: "frps 是中继的服务器端：它跑在有公网 IP 的机器上，把 frpc 映射的端口暴露出去。\n" +
		"  install    在本机安装并启动（仅 Linux）\n" +
		"  setup      通过 SSH 装到远程 Linux 服务器\n" +
		"  status     查看本机 frps 的运行状态与配置\n" +
		"  uninstall  停止并卸载本机的 frps",
}

var relayServerInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "安装并启动 frps（仅 Linux）",
	Long: "下载 frps、生成带随机 Token 的 frps.toml 并后台启动。\n" +
		"全部文件都在 flare 的数据目录里（frps.toml / frps.pid / logs/frps.log），不需要 root。\n" +
		"已经装过时：沿用原来的端口与 Token，客户端不用改配置。",
	RunE: runRelayServerInstall,
}

var relayServerUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "停止并卸载 frps",
	RunE:  runRelayServerUninstall,
}

var relayServerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看 frps 运行状态与配置",
	RunE:  runRelayServerStatus,
}

// relayLinuxOnly：frps 的服务端管理只支持 Linux。
// 为什么卡这么死？install 依赖 Linux 的进程/权限习惯，setup 的远程脚本也按 Linux 写。
// 报错必须给出路：装到远程服务器、或用 Docker，都写清楚
func relayLinuxOnly(action string) error {
	if runtime.GOOS == "linux" {
		return nil
	}
	return fmt.Errorf("frps 服务端%s仅支持 Linux，当前平台: %s"+
		"（装到远程 Linux 服务器: flare relay server setup --host <IP>；或用 Docker: docker/relay-server/）",
		action, runtime.GOOS)
}

// randomToken 生成 32 个十六进制字符（128 位）的令牌，用 frps 的 auth.token
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 在正常系统上不会失败；真失败了也不能退回可预测的随机数
		log.Errorf("生成随机 Token 失败: %v", err)
		return ""
	}
	return hex.EncodeToString(b)
}

// buildFrpsToml 生成 frps 的最小配置：控制端口 + 鉴权令牌。
// 令牌等同密码，客户端不带上它就连不上（除非服务端显式关掉鉴权）
func buildFrpsToml(bindPort int, token string) string {
	return fmt.Sprintf("bindPort = %d\nauth.token = %q\n", bindPort, token)
}

// frpsFileConfig 是从 frps.toml 里读到的两个值
type frpsFileConfig struct {
	Port  int
	Token string
}

// tomlValue 取出一行的值：去掉引号和行尾注释。
// 只服务于我们自己生成的两行配置，不需要一个完整的 TOML 解析器
func tomlValue(raw string) string {
	v := strings.TrimSpace(raw)
	if strings.HasPrefix(v, `"`) { // 带引号的字符串：取到下一个引号为止
		if end := strings.Index(v[1:], `"`); end >= 0 {
			return v[1 : 1+end]
		}
		return strings.Trim(v, `"`)
	}
	if i := strings.IndexByte(v, '#'); i >= 0 { // 不带引号的值：注释从 # 开始
		v = strings.TrimSpace(v[:i])
	}
	return v
}

// parseFrpsConfig 从 frps.toml 文本里认 bindPort 与 auth.token。
// 认不出来就返回零值，调用方会用默认端口、生成新 Token 兜底
func parseFrpsConfig(text string) frpsFileConfig {
	var c frpsFileConfig
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "bindPort":
			if n, err := strconv.Atoi(tomlValue(value)); err == nil {
				c.Port = n
			}
		case "auth.token":
			c.Token = tomlValue(value)
		}
	}
	return c
}

// existingFrpsConfig 读本机已有的 frps.toml（没有或坏掉都返回零值）
func existingFrpsConfig() frpsFileConfig {
	data, err := os.ReadFile(frpsConfigPath())
	if err != nil {
		return frpsFileConfig{}
	}
	return parseFrpsConfig(string(data))
}

// writeFrpsConfig 落盘 frps.toml。0600：里面是连接令牌，权限按密码对待
func writeFrpsConfig(port int, token string) error {
	if err := os.MkdirAll(config.Dir(), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(frpsConfigPath(), []byte(buildFrpsToml(port, token)), 0600); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", frpsConfigPath(), err)
	}
	return nil
}

func readFrpsPid() (int, error) {
	data, err := os.ReadFile(frpsPidPath())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func writeFrpsPid(pid int) error {
	if err := os.MkdirAll(config.Dir(), 0700); err != nil {
		return err
	}
	return os.WriteFile(frpsPidPath(), []byte(strconv.Itoa(pid)), 0600)
}

// processAlive 用信号 0 探测进程是否还活着：不发送任何信号，只问"这个 PID 存在吗"。
// EPERM = 进程在、但不归当前用户管，也算活着
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// frpsRunning：PID 文件在 + 进程真的活着，两步缺一不可
// （进程崩了但 PID 文件没清掉的情况很常见，只看文件会误报"运行中"）
func frpsRunning() bool {
	pid, err := readFrpsPid()
	return err == nil && processAlive(pid)
}

func frpsPid() int {
	pid, _ := readFrpsPid()
	return pid
}

// stopFrps 先请 frps 自己退出（SIGTERM，让它有机会收尾），最多等 5 秒；
// 还不退就 SIGKILL 兜底——卸载卡在半路比强杀更糟
func stopFrps(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("找不到 frps 进程 (PID: %d): %w", pid, err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("停止 frps (PID: %d) 失败: %w", pid, err)
	}
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := p.Kill(); err != nil {
		return fmt.Errorf("强制结束 frps (PID: %d) 失败: %w", pid, err)
	}
	return nil
}

// frpsStartupGrace：后台进程"算启动成功"的观察窗口。
// frps 起不来（端口被占、配置坏了）会在这个窗口里退出，等一秒再看比直接说"已启动"诚实
const frpsStartupGrace = time.Second

// waitFrpsStartup 等一小会儿，返回（是否还活着, 退出错误）。
// 用 cmd.Wait() 而不是查进程表：没人 Wait 的子进程会变成僵尸，
// 僵尸用信号 0 探不出死活，会把"启动即失败"误报成"已启动"
func waitFrpsStartup(cmd *exec.Cmd) (bool, error) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return false, err
	case <-time.After(frpsStartupGrace):
		return true, nil
	}
}

// startFrps 后台启动 frps 并记下 PID。
// 输出进 logs/frps.log：父进程一退出，终端就没人接收了，日志是唯一的事后现场
func startFrps(bin string, port int) error {
	logFile, err := log.Open("frps")
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer logFile.Close() // 子进程已拿到自己那份 fd，这里关掉不影响它

	cmd := exec.Command(bin, "-c", frpsConfigPath())
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	log.Debugf("启动 frps: %s -c %s", bin, frpsConfigPath())
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 frps 失败: %w", err)
	}
	if err := writeFrpsPid(cmd.Process.Pid); err != nil {
		return fmt.Errorf("写 PID 文件失败: %w", err)
	}
	if alive, waitErr := waitFrpsStartup(cmd); !alive {
		os.Remove(frpsPidPath()) // 进程已死，PID 文件不能留
		log.Errorf("frps 启动后立即退出: %v", waitErr)
		log.PrintTail("frps", 20) // 原因就在日志里，直接摆出来
		return fmt.Errorf("frps 启动后立即退出（端口 %d 被占用？），详情见 %s", port, log.Path("frps"))
	}
	log.Infof("frps 已启动 (PID: %d), 监听端口 %d", cmd.Process.Pid, port)
	return nil
}

func runRelayServerInstall(cmd *cobra.Command, args []string) error {
	if err := relayLinuxOnly("安装"); err != nil {
		return err
	}
	if frpsRunning() {
		return fmt.Errorf("frps 已在运行 (PID: %d)；要重装先执行 flare relay server uninstall", frpsPid())
	}
	// 先下载（最容易失败的一步），再写配置、再启动，免得留下"看着配好了却没服务"的现场
	bin, err := download.Frps()
	if err != nil {
		return err
	}

	// 已经有配置时沿用端口与 Token：重装一次就把所有客户端作废，代价太大。
	// 端口只有用户显式传了 --port 才覆盖
	old := existingFrpsConfig()
	port := relayServerPort
	if !cmd.Flags().Changed("port") && old.Port > 0 {
		port = old.Port
	}
	token := old.Token
	if token == "" {
		token = randomToken()
		if token == "" {
			return fmt.Errorf("生成 Token 失败，请重试")
		}
	} else {
		fmt.Println("沿用已有的 Token（客户端配置不用改）")
	}
	if err := writeFrpsConfig(port, token); err != nil {
		return err
	}
	if err := startFrps(bin, port); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("frps 已安装: %s\n", bin)
	fmt.Printf("配置文件:   %s\n", frpsConfigPath())
	fmt.Printf("监听端口:   %d\n", port)
	fmt.Printf("Token:      %s\n", token)
	fmt.Printf("日志:       %s\n", log.Path("frps"))
	fmt.Println()
	fmt.Println("在客户端机器上执行（把 <公网IP> 换成这台机器的公网 IP）:")
	fmt.Printf("  flare relay init <公网IP>:%d --token %s\n", port, token)
	fmt.Println("  flare relay add <名称> --local <端口>")
	fmt.Println("  flare relay up")
	return nil
}

func runRelayServerUninstall(cmd *cobra.Command, args []string) error {
	if err := relayLinuxOnly("卸载"); err != nil {
		return err
	}
	cleaned := false
	if pid, err := readFrpsPid(); err == nil {
		if processAlive(pid) {
			if err := stopFrps(pid); err != nil {
				return err
			}
			fmt.Printf("已停止 frps (PID: %d)\n", pid)
		}
		if err := os.Remove(frpsPidPath()); err == nil {
			cleaned = true
		}
	}
	for _, p := range []string{download.FrpsPath(), frpsConfigPath()} {
		if err := os.Remove(p); err == nil {
			fmt.Printf("已删除 %s\n", p)
			cleaned = true
		}
	}
	if !cleaned {
		fmt.Printf("本机没有安装 frps（数据目录: %s）\n", config.Dir())
		return nil
	}
	// 日志留着：卸载往往是因为服务有问题，日志正是要看的东西
	if _, err := os.Stat(log.Path("frps")); err == nil {
		fmt.Printf("日志保留在 %s（不需要就 rm 掉）\n", log.Path("frps"))
	}
	fmt.Println("frps 已卸载")
	return nil
}

func runRelayServerStatus(cmd *cobra.Command, args []string) error {
	if err := relayLinuxOnly("状态查看"); err != nil {
		return err
	}
	bin := download.FrpsPath()
	if _, err := os.Stat(bin); err != nil {
		fmt.Printf("frps 未安装（%s 不存在）\n", bin)
		fmt.Println("安装: flare relay server install")
		return nil
	}
	fmt.Printf("frps 二进制: %s\n", bin)

	cfg := existingFrpsConfig()
	if _, err := os.Stat(frpsConfigPath()); err != nil {
		fmt.Printf("配置文件:   未找到（%s）\n", frpsConfigPath())
		fmt.Println("生成配置: flare relay server install")
	} else {
		fmt.Printf("配置文件:   %s\n", frpsConfigPath())
		if cfg.Port > 0 {
			fmt.Printf("监听端口:   %d\n", cfg.Port)
		} else {
			fmt.Println("监听端口:   配置里没读到 bindPort（文件被手工改过？）")
		}
		if cfg.Token != "" {
			fmt.Printf("Token:      %s\n", cfg.Token)
		}
	}

	if frpsRunning() {
		fmt.Printf("frps 运行中 (PID: %d)\n", frpsPid())
		fmt.Printf("日志:       %s\n", log.Path("frps"))
		if cfg.Port > 0 && cfg.Token != "" {
			fmt.Println()
			fmt.Println("客户端连接:")
			fmt.Printf("  flare relay init <本机公网IP>:%d --token %s\n", cfg.Port, cfg.Token)
		}
		return nil
	}

	fmt.Println("frps 未运行")
	// 没在跑时把日志末尾摆出来：原因基本就在那几行里
	log.PrintTail("frps", 10)
	return nil
}

func init() {
	relayServerInstallCmd.Flags().IntVar(&relayServerPort, "port", 7000, "frps 控制端口（frpc 连接的端口）")
	relayServerCmd.AddCommand(relayServerInstallCmd, relayServerUninstallCmd, relayServerStatusCmd)
	relayCmd.AddCommand(relayServerCmd)
}
