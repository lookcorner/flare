package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"

	"flare/internal/config"
	"flare/internal/daemon"
	"flare/internal/download"
	"flare/internal/log"
	"flare/internal/relay"

	"github.com/spf13/cobra"
)

// 同一件事在三个平台上的名字不一样，各留一个常量：
//   - macOS: LaunchAgent 的标签（同时也是 plist 文件名）
//   - Linux: systemd 用户单位名（也是文件名）
//   - Windows: 服务名
const (
	relayPlistLabel = "com.flare.frpc"
	relayUnitName   = "flare-relay"
	relaySvcName    = "flare-relay"
)

// relaySpec 是"frpc 这个自启项"的完整描述，三块模板共用。
// 平台之间只有外壳不同（plist / systemd unit / sc 命令），里面的程序和参数是同一套
type relaySpec struct {
	Label   string   // 自启项的名字（见上面三个常量）
	BinPath string   // frpc 的绝对路径（flare 数据目录下的 bin/）
	Args    []string // 启动参数，必须与 daemon.StartRelay 一致
	LogPath string   // 标准输出/错误落到哪个文件
}

// relayArgs 是 frpc 的启动参数，必须与 daemon.StartRelay 手动启动时逐字一致：
// 两种启动方式一旦分叉，就会出现"relay up 能用、开机却起不来"这类最难查的问题
func relayArgs(configPath string) []string { return []string{"-c", configPath} }

// relayExeArgs 拼"程序 + 参数"的一行命令：systemd 的 ExecStart、Windows 的 binPath= 都要这种形式。
// 路径里带空格时必须整段加双引号——systemd 按空白切分参数，Windows 服务管理器也按空格找 exe，
// 不包住就会把路径从空格处截断
func relayExeArgs(binPath string, args []string) string {
	exe := binPath
	if strings.ContainsAny(exe, " \t") {
		exe = `"` + exe + `"`
	}
	if len(args) == 0 {
		return exe
	}
	return exe + " " + strings.Join(args, " ")
}

// ensureRelayLog 先把 frpc 的日志文件建出来（0600），再交给系统服务往里追加：
// launchd / systemd 不会替我们建目录，目录不存在服务起不来；
// 日志里会出现服务器地址、规则名，不该让同机器的其他用户读到
func ensureRelayLog() error {
	f, err := log.Open("frpc")
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %w", err)
	}
	return f.Close()
}

// relayInstalledNote 拼"注册成功"后的三件事：服务文件写在哪、以后怎么启停、日志在哪看。
// 三个平台共用同一段文案，免得同一件事在三份实现里说法不一致
func relayInstalledNote(serviceName, serviceFile, startStop string) string {
	return fmt.Sprintf("已注册开机自启: %s\n  位置: %s\n  启停方式: %s\n  日志: %s（flare log frpc -f 可实时查看）",
		serviceName, serviceFile, startStop, log.Path("frpc"))
}

// ==================== macOS launchd ====================

// relayPlistTmpl 是 frpc 的 LaunchAgent 定义。
// KeepAlive = 进程没了自动拉起来；RunAtLoad = 加载时立刻跑
const relayPlistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinPath}}</string>{{range .Args}}
        <string>{{.}}</string>{{end}}
    </array>
    <key>KeepAlive</key>
    <true/>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.LogPath}}</string>
    <key>StandardErrorPath</key>
    <string>{{.LogPath}}</string>
</dict>
</plist>
`

// renderRelayPlist 只做"数据 → 字符串"：不碰文件系统、不执行命令，单测里能直接断言
func renderRelayPlist(s relaySpec) (string, error) {
	var b strings.Builder
	if err := template.Must(template.New("relay-plist").Parse(relayPlistTmpl)).Execute(&b, s); err != nil {
		return "", err
	}
	return b.String(), nil
}

// relayUnitTmpl 是 frpc 的 systemd 用户单位。
// Restart=always：崩了 5 秒后自动回来；StandardOutput=append: 让输出落进 flare 的日志文件
const relayUnitTmpl = `[Unit]
Description={{.Label}}（flare relay: frp 客户端）
After=network.target

[Service]
ExecStart={{.ExecStart}}
Restart=always
RestartSec=5
StandardOutput=append:{{.LogPath}}
StandardError=append:{{.LogPath}}

[Install]
WantedBy=default.target
`

// renderRelayUnit 纯函数，同上：模板渲染单独抽出来，才能在没有 systemd 的机器上验证
func renderRelayUnit(s relaySpec) (string, error) {
	data := struct {
		Label     string
		ExecStart string
		LogPath   string
	}{
		Label:     s.Label,
		ExecStart: relayExeArgs(s.BinPath, s.Args),
		LogPath:   s.LogPath,
	}
	var b strings.Builder
	if err := template.Must(template.New("relay-unit").Parse(relayUnitTmpl)).Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// relayScBinPath 拼 sc create 的 binPath= 值（程序 + 参数，路径带空格时给 exe 加引号）
func relayScBinPath(s relaySpec) string {
	return relayExeArgs(s.BinPath, s.Args)
}

// relayPlistPath frpc 的 plist 落盘位置：~/Library/LaunchAgents/com.flare.frpc.plist
func relayPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", relayPlistLabel+".plist")
}

// relayUnitPath frpc 的用户单位位置：~/.config/systemd/user/flare-relay.service
func relayUnitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", relayUnitName+".service")
}

// installRelayLaunchd 写 plist 并交给 launchd 加载（加载即启动）
func installRelayLaunchd(binPath string) error {
	if err := ensureRelayLog(); err != nil {
		return err
	}
	content, err := renderRelayPlist(relaySpec{
		Label:   relayPlistLabel,
		BinPath: binPath,
		Args:    relayArgs(relay.FrpcConfigPath()),
		LogPath: log.Path("frpc"),
	})
	if err != nil {
		return fmt.Errorf("渲染 plist 失败: %w", err)
	}
	// 0600：能改这个文件的人就能决定你开机跑什么，不该让别的用户可写
	path := relayPlistPath()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	// 先 unload 再 load：重复 install 时旧任务还挂着，直接 load 会报 already loaded
	if err := exec.Command("launchctl", "unload", path).Run(); err != nil {
		log.Debugf("launchctl unload 未成功（通常是没有已加载的旧任务）: %v", err)
	}
	if err := exec.Command("launchctl", "load", path).Run(); err != nil {
		return fmt.Errorf("launchctl load 失败: %w（plist 已写入 %s，修好后可手动 launchctl load 它）", err, path)
	}
	log.Infof("frpc 已注册开机自启 (launchd: %s)", relayPlistLabel)
	fmt.Println(relayInstalledNote(relayPlistLabel, path, "登录时自动启动；手动启停: launchctl load/unload "+path))
	return nil
}

// uninstallRelayLaunchd 卸载任务并删掉 plist
func uninstallRelayLaunchd() error {
	path := relayPlistPath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("未找到 %s，似乎没有注册过开机自启", path)
	}
	if err := exec.Command("launchctl", "unload", path).Run(); err != nil {
		log.Debugf("launchctl unload 未成功（任务可能已不在）: %v", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", path, err)
	}
	log.Infof("frpc 已取消开机自启（已删除 %s）", path)
	return nil
}

// ==================== Linux systemd ====================

// installRelaySystemd 写用户单位文件，再 daemon-reload + enable --now
func installRelaySystemd(binPath string) error {
	if err := ensureRelayLog(); err != nil {
		return err
	}
	content, err := renderRelayUnit(relaySpec{
		Label:   relayUnitName,
		BinPath: binPath,
		Args:    relayArgs(relay.FrpcConfigPath()),
		LogPath: log.Path("frpc"),
	})
	if err != nil {
		return fmt.Errorf("渲染 systemd 单位失败: %w", err)
	}
	path := relayUnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建 %s 失败: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload 失败: %w（单位文件已写入 %s）", err, path)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", relayUnitName).Run(); err != nil {
		return fmt.Errorf("systemctl --user enable 失败: %w（单位文件已写入 %s；容器或纯 ssh 会话里可能没有 systemd 用户会话）", err, path)
	}
	log.Infof("frpc 已注册开机自启 (systemd 用户单位: %s)", relayUnitName)
	fmt.Println(relayInstalledNote(relayUnitName, path, "systemctl --user start/stop "+relayUnitName))
	fmt.Println("  提示: 用户单位默认登录后才启动；想让它在无人登录时也自启，执行一次（需要 root）: sudo loginctl enable-linger <用户名>")
	return nil
}

// uninstallRelaySystemd 停掉并禁用单位，然后删掉单位文件
func uninstallRelaySystemd() error {
	path := relayUnitPath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("未找到 %s，似乎没有注册过开机自启", path)
	}
	if err := exec.Command("systemctl", "--user", "disable", "--now", relayUnitName).Run(); err != nil {
		log.Debugf("systemctl --user disable 未成功（单位可能已失效）: %v", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", path, err)
	}
	exec.Command("systemctl", "--user", "daemon-reload").Run() // 让 systemd 忘掉已删除的单位
	log.Infof("frpc 已取消开机自启（已删除 %s）", path)
	return nil
}

// ==================== Windows sc ====================

// installRelayWindows 注册并启动 Windows 服务（需要管理员终端）
func installRelayWindows(binPath string) error {
	if err := ensureRelayLog(); err != nil {
		return err
	}
	spec := relaySpec{
		Label:   relaySvcName,
		BinPath: binPath,
		Args:    relayArgs(relay.FrpcConfigPath()),
		LogPath: log.Path("frpc"),
	}
	// 先删后建：重复 install 时 sc create 会报"服务已存在"，删一次再建才是幂等的
	if err := exec.Command("sc", "delete", relaySvcName).Run(); err != nil {
		log.Debugf("sc delete 未成功（通常是没有同名服务）: %v", err)
	}
	if err := exec.Command("sc", "create", relaySvcName, "binPath=", relayScBinPath(spec), "start=", "auto").Run(); err != nil {
		return fmt.Errorf("创建服务失败: %w（需要在管理员终端里执行）", err)
	}
	if err := exec.Command("sc", "start", relaySvcName).Run(); err != nil {
		return fmt.Errorf("启动服务失败: %w（服务已注册，可手动执行 sc start %s）", err, relaySvcName)
	}
	log.Infof("frpc 已注册开机自启 (Windows 服务: %s)", relaySvcName)
	fmt.Println(relayInstalledNote(relaySvcName, "Windows 服务管理器（sc create）", "sc start/stop "+relaySvcName))
	fmt.Println("  提示: Windows 服务没有终端，frpc 的输出不会写进上面的日志文件；状态用 sc query " + relaySvcName + " 查看")
	return nil
}

// uninstallRelayWindows 停掉并删除服务
func uninstallRelayWindows() error {
	exec.Command("sc", "stop", relaySvcName).Run() // 没在跑或不存在都无所谓，接着删
	if err := exec.Command("sc", "delete", relaySvcName).Run(); err != nil {
		return fmt.Errorf("删除服务失败: %w（用 sc query %s 可确认服务是否存在）", err, relaySvcName)
	}
	log.Infof("frpc 已取消开机自启（已删除服务 %s）", relaySvcName)
	return nil
}

// ==================== 命令 ====================

// relayInstallCmd 把 frpc 注册成开机自启的服务。
// frpc 的启动方式是 "frpc -c frpc.toml"，所以注册前必须先把 frpc.toml 写出来：
// 服务开机跑的就是这条命令，配置文件不在场（或还是旧的）等于白注册
var relayInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "注册 frpc 为系统服务（开机自启）",
	Long: "把 frpc 交给操作系统托管，机器重启后穿透自动恢复。\n" +
		"注册后不要再执行 flare relay up（会起第二个进程），要停掉它用 flare relay uninstall。",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 便携模式下数据目录跟着 exe 走，注册进服务里的绝对路径下次就失效了
		if config.PortTable() {
			return fmt.Errorf("便携模式下不支持注册系统服务（路径不固定），请改用 flare relay up 手动启动")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.Relay.Server == "" {
			return fmt.Errorf("还没配置中继服务器：请先执行 flare relay init <服务器地址:端口>")
		}
		if len(cfg.Relay.Rules) == 0 {
			return fmt.Errorf("还没有穿透规则：请先执行 flare relay add <名称> --local <端口>")
		}
		if daemon.RelayRunning() {
			// 服务启动时系统会另起一个 frpc，建议先停掉手动那个，免得两条连接互相抢
			fmt.Printf("提示: frpc 正在运行 (PID: %d)，建议先执行 flare relay down 再注册\n", daemon.RelayPid())
		}
		// 先把 frpc.toml 按当前配置写出来，再下载、注册
		if err := relay.GenerateFrpcConfig(&cfg.Relay); err != nil {
			return err
		}
		binPath, err := download.Frpc()
		if err != nil {
			return err
		}
		switch runtime.GOOS {
		case "darwin":
			err = installRelayLaunchd(binPath)
		case "linux":
			err = installRelaySystemd(binPath)
		case "windows":
			err = installRelayWindows(binPath)
		default:
			return fmt.Errorf("不支持的平台: %s（可照 %s 的写法手动配置开机启动）", runtime.GOOS, relay.FrpcConfigPath())
		}
		if err != nil {
			return err
		}
		fmt.Println("frpc 已交给系统托管：机器重启后自动恢复穿透")
		fmt.Println("提示: 之后不要再用 flare relay up（会起第二个进程）；要停掉它用 flare relay uninstall")
		return nil
	},
}

// relayUninstallCmd 撤销 relay install：停掉并删除系统服务。配置和规则都留着
var relayUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "卸载 frpc 系统服务（取消开机自启）",
	Long:  "停掉并删除 relay install 注册的系统服务。中继配置和穿透规则都留着，随时可以用 flare relay up 再跑起来。",
	RunE: func(cmd *cobra.Command, args []string) error {
		switch runtime.GOOS {
		case "darwin":
			return uninstallRelayLaunchd()
		case "linux":
			return uninstallRelaySystemd()
		case "windows":
			return uninstallRelayWindows()
		default:
			return fmt.Errorf("不支持的平台: %s", runtime.GOOS)
		}
	},
}

func init() {
	relayCmd.AddCommand(relayInstallCmd, relayUninstallCmd)
}
