//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"flare/internal/log"
)

// Systemd 是 Linux 的实现：systemd 用户单位（systemctl --user）。
// 为什么不做 /etc/systemd/system 下的系统单位：那需要 sudo，还要保证 flare 数据目录
// 对 root 可读；而二进制、配置、日志本来就全在用户家目录里，用户单位刚好贴住这条边界。
// 代价是用户单位默认"随登录会话启动"——想让它在无人登录时也开机自启，
// 得让 root 执行一次 loginctl enable-linger，所以 Install 会把这条命令打出来。
type Systemd struct{}

// unitName 单位名，同时也是文件名：flare-cloudflared.service
const unitName = "flare-cloudflared"

// unitPath 用户级单位目录，按 XDG 规范：XDG_CONFIG_HOME 优先，否则 ~/.config
func (s *Systemd) unitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", unitName+".service")
}

// unitData 是模板入参（ExecStart 已经拼好，模板里只管排版）
type unitData struct {
	Name      string // 单位名，写进 Description，方便在 systemctl list-units 里认出来
	ExecStart string // 二进制 + 启动参数，必须与 daemon.Start 一致
	LogPath   string // 标准输出/错误追加到哪个文件
}

// unitTmpl 是 systemd 用户单位文件。
// Restart=always + RestartSec=5：进程崩了 5 秒后自动回来，是"开机自启"之外的第二层保险。
// StandardOutput/Error=append: 把输出追加到 flare 的日志文件（systemd v240+ 支持 append:），
// 这样系统托管的进程和 flare up 起的进程，用户都用同一条 flare log 命令看得到
const unitTmpl = `[Unit]
Description={{.Name}}（flare: Cloudflare 隧道）
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

// renderUnit 只做"数据 → 字符串"，不碰文件系统也不执行命令，方便单测直接断言
func renderUnit(d unitData) (string, error) {
	var b strings.Builder
	if err := template.Must(template.New("unit").Parse(unitTmpl)).Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Install 写用户单位文件，然后 daemon-reload + enable --now（enabled 就是"开机自启"）。
func (s *Systemd) Install(binPath, token string) error {
	if err := prepareLog("cloudflared"); err != nil {
		return err
	}
	content, err := renderUnit(unitData{
		Name:      unitName,
		ExecStart: joinExeArgs(binPath, cloudflaredArgs(token)),
		LogPath:   log.Path("cloudflared"),
	})
	if err != nil {
		return fmt.Errorf("渲染 systemd 单位失败: %w", err)
	}
	// 权限 0600：单位文件里写着隧道 token，同机器的其他用户不该读到
	path := s.unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建 %s 失败: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload 失败: %w（单位文件已写入 %s）", err, path)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", unitName).Run(); err != nil {
		return fmt.Errorf("systemctl --user enable 失败: %w（单位文件已写入 %s；容器或纯 ssh 会话里可能没有 systemd 用户会话）", err, path)
	}
	log.Infof("cloudflared 已注册开机自启 (systemd 用户单位: %s)", unitName)
	fmt.Println(successNote(unitName, path, "systemctl --user start/stop "+unitName, "cloudflared"))
	fmt.Printf("  提示: 用户单位默认登录后才启动；要让它在无人登录时也自启，执行一次（需要 root）: sudo loginctl enable-linger %s\n", currentUser())
	return nil
}

// Uninstall 停掉并禁用单位，然后删掉单位文件。
func (s *Systemd) Uninstall() error {
	path := s.unitPath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("未找到 %s，似乎没有注册过开机自启", path)
	}
	// disable --now 会停掉正在跑的进程；失败（比如单位已失效）不拦路，继续删文件
	if err := exec.Command("systemctl", "--user", "disable", "--now", unitName).Run(); err != nil {
		log.Debugf("systemctl --user disable 未成功（单位可能已失效）: %v", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", path, err)
	}
	// 让 systemd 忘掉这个已删除的单位，否则 systemctl status 还会报"未找到"
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	log.Infof("cloudflared 已取消开机自启（已删除 %s）", path)
	return nil
}

// Running 单位是否处于激活状态
func (s *Systemd) Running() bool {
	return exec.Command("systemctl", "--user", "is-active", "--quiet", unitName).Run() == nil
}

// currentUser 只用于提示文案（linger 命令需要用户名）：取不到就给个占位符，不影响安装本身
func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "<用户名>"
}

// New 返回当前平台的服务实现（Linux 是 systemd 用户单位）
func New() Service { return &Systemd{} }
