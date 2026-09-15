package daemon

import (
	"flare/internal/config"
	"flare/internal/download"
	"flare/internal/log"
	"flare/internal/relay"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// frpcPidFile：frpc 的 PID 文件。与 cloudflared.pid 各存一份——
// HTTP 隧道（cloudflared）和中继穿透（frpc）是两件独立的事，可以同时跑
const frpcPidFile = "frpc.pid"

// RelayRunning：frpc 是否在运行
func RelayRunning() bool { return procAlive(frpcPidFile) }

// RelayPid：frpc 的 PID（没在运行时返回 0）
func RelayPid() int {
	pid, _ := readPidFile(frpcPidFile)
	return pid
}

// StartRelayQuick：前台运行 frpc（flare fast --relay），Ctrl+C 退出。
//
// 与 StartRelay 的三点区别：
//   - 不后台、不写 PID：跟 fast 的 Cloud 模式一样是"用完即走"
//   - 规则是临时的一条（本地端口 = 远端端口，名字固定 quick）
//   - 配置写到临时文件 frpc-quick.toml，**绝不覆盖**用户长期用的 frpc.toml
func StartRelayQuick(port, proto string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Relay.Server == "" {
		return fmt.Errorf("未配置中继服务器，请先执行 flare relay init <服务器地址:端口>")
	}
	portNum, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || portNum < 1 || portNum > 65535 {
		return fmt.Errorf("端口必须是 1~65535 的数字，收到: %q", port)
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("--proto 只能是 tcp 或 udp，收到: %q", proto)
	}
	bin, err := download.Frpc()
	if err != nil {
		return fmt.Errorf("下载 frpc 失败: %w", err)
	}

	quickCfg := config.RelayConfig{
		Server: cfg.Relay.Server,
		Token:  cfg.Relay.Token,
		Rules: []config.RelayRule{{
			Name:       "quick",
			Proto:      proto,
			LocalPort:  portNum,
			RemotePort: portNum,
		}},
	}
	tmpCfg := filepath.Join(config.Dir(), "frpc-quick.toml")
	if err := relay.GenerateFrpcConfigTo(&quickCfg, tmpCfg); err != nil {
		return err
	}
	defer os.Remove(tmpCfg) // 临时配置用完就删，别留在数据目录里让人误会

	// 说清楚"外面该怎么连"：中继模式下本地端口和远端端口是同一个
	fmt.Printf("中继穿透已启动: %s://localhost:%d → %s:%d\n", proto, portNum, cfg.Relay.Server, portNum)
	fmt.Println("Ctrl+C 退出，远端端口随之关闭")

	logFile, err := log.Open("frpc")
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin, "-c", tmpCfg)
	// 前台模式：日志既要在终端看得见（用户正盯着），也要落一份到文件（事后能查）
	cmd.Stdout = io.MultiWriter(os.Stdout, logFile)
	cmd.Stderr = io.MultiWriter(os.Stderr, logFile)
	log.Debugf("启动 frpc(quick): %s -c %s", bin, tmpCfg)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 frpc 失败: %w", err)
	}
	// 复用 fast 模式那套"监听信号 / 等进程退出"的逻辑
	return quick("frpc", cmd)
}

// StartRelay：后台启动 frpc。
// 与 Start（cloudflared）的关键区别：cloudflared 用 token 模式，规则表存在云端；
// frpc 的规则表在本地文件 frpc.toml 里，所以每次启动都要按当前配置重新生成一遍——
// 这也是"改了规则，relay up 一下即生效"的原因。
func StartRelay(rc *config.RelayConfig) error {
	// 防重复启动：先问 PID 文件
	if RelayRunning() {
		return fmt.Errorf("frpc 已在运行 (PID: %d)，请先执行 flare relay down", RelayPid())
	}
	// 先下载（最耗时、最容易失败的一步），再写配置、再启动——
	// 免得下载失败时白白留下一个"看起来配好了"的 frpc.toml
	bin, err := download.Frpc()
	if err != nil {
		return err
	}
	if err := relay.GenerateFrpcConfig(rc); err != nil {
		return err
	}
	// 与 cloudflared 一样：后台进程的输出进日志文件，否则父进程一退出就无处可写
	logFile, err := log.Open("frpc")
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin, "-c", relay.FrpcConfigPath())
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	log.Debugf("启动 frpc: %s -c %s", bin, relay.FrpcConfigPath())
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 frpc 失败: %w", err)
	}
	if err := writePidFile(frpcPidFile, cmd.Process.Pid); err != nil {
		return fmt.Errorf("写 PID 文件失败: %w", err)
	}

	// frpc.toml 里写了 loginFailExit = true：地址/端口/令牌不对时它会立刻退出。
	// 等一小会儿确认它还活着（为什么不能用 processRunning(pid) 判断，见 waitStartup 的注释）
	if alive, waitErr := waitStartup(cmd); !alive {
		os.Remove(pidFile(frpcPidFile)) // 进程已死，PID 文件不能留
		log.Errorf("frpc 启动后立即退出: %v", waitErr)
		log.PrintTail("frpc", 20) // 原因就在它自己的日志里，直接摆出来
		return fmt.Errorf("frpc 启动后立即退出，请检查中继服务器地址、端口与令牌（详情见 %s）", log.Path("frpc"))
	}
	log.Infof("frpc 已启动 (PID: %d)", cmd.Process.Pid)
	fmt.Printf("日志: %s（flare log frpc -f 可实时查看）\n", log.Path("frpc"))
	return nil
}

// StopRelay：优雅停止 frpc 并清理 PID 文件
func StopRelay() error {
	pid, err := readPidFile(frpcPidFile)
	if err != nil {
		return fmt.Errorf("frpc 未在运行: %w", err)
	}
	// 进程自己崩了、只剩过期 PID 文件：清理掉并把话说明白
	if !processRunning(pid) {
		os.Remove(pidFile(frpcPidFile))
		return fmt.Errorf("frpc 已不在运行（PID %d 已消失），已清理 PID 文件", pid)
	}
	if err := processKill(pid); err != nil {
		return fmt.Errorf("停止 frpc 失败: %w", err)
	}
	os.Remove(pidFile(frpcPidFile))
	log.Infof("frpc 已停止 (PID: %d)", pid)
	return nil
}
