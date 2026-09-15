//go:build darwin

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

// Launchd 是 macOS 的实现：用户级 LaunchAgent。
// 为什么用 LaunchAgent（~/Library/LaunchAgents）而不是 LaunchDaemon（/Library/LaunchDaemons）：
// 前者以当前用户身份运行，二进制、配置、日志都在自己家目录里，不需要 sudo；
// 后者是系统级、以 root 运行，还得额外处理文件属主和权限，对"就跑一个隧道"来说是多余的麻烦。
type Launchd struct{}

// plistLabel 是 launchd 的任务标签，同时用作 plist 文件名（"域名倒着写"是 launchd 的命名惯例）。
// 改名要连文件名一起改：launchctl 是按路径找文件的，标签和文件名对不上会很别扭
const plistLabel = "com.flare.cloudflared"

// plistPath plist 的落盘位置：~/Library/LaunchAgents/<标签>.plist
func (l *Launchd) plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
}

// plistData 是模板入参。用结构体而不是 map[string]string：字段名写错在编译期就报错
type plistData struct {
	Label   string   // 任务标签
	BinPath string   // 二进制绝对路径
	Args    []string // 启动参数（不含程序名），必须与 daemon.Start 一致
	LogPath string   // 标准输出/错误落到哪个文件
}

// plistTmpl 是 LaunchAgent 的定义文件。
// RunAtLoad = 加载时立刻启动（登录后自动跑）；KeepAlive = 进程没了就自动拉起来。
// StandardOutPath/StandardErrorPath 都指到 flare 的日志文件：后台服务没有终端，
// 输出不落盘就等于没有日志，"昨晚隧道为什么断了"这类问题只能靠它回答
const plistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
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

// renderPlist 只做"数据 → 字符串"，既不碰文件系统也不执行命令：
// 模板里的标签名、二进制路径、启动参数因此能在单测里直接断言
func renderPlist(d plistData) (string, error) {
	var b strings.Builder
	if err := template.Must(template.New("plist").Parse(plistTmpl)).Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Install 写 plist 并交给 launchd 加载（加载即启动）。
func (l *Launchd) Install(binPath, token string) error {
	// 日志目录/文件先建好：launchd 只往文件里追加，不会替我们建目录，
	// 而目录不存在时任务是静默起不来的，最难查
	if err := prepareLog("cloudflared"); err != nil {
		return err
	}
	content, err := renderPlist(plistData{
		Label:   plistLabel,
		BinPath: binPath,
		Args:    cloudflaredArgs(token),
		LogPath: log.Path("cloudflared"),
	})
	if err != nil {
		return fmt.Errorf("渲染 plist 失败: %w", err)
	}
	// 权限 0600：plist 里写着隧道 token，同机器的其他用户不该读到
	path := l.plistPath()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	// 先 unload 再 load：重复 install 时旧任务还挂着，直接 load 会报 already loaded。
	// unload 失败多半是"本来就没加载"，忽略即可（留一行 debug 日志备查）
	if err := exec.Command("launchctl", "unload", path).Run(); err != nil {
		log.Debugf("launchctl unload 未成功（通常是没有已加载的旧任务）: %v", err)
	}
	if err := exec.Command("launchctl", "load", path).Run(); err != nil {
		return fmt.Errorf("launchctl load 失败: %w（plist 已写入 %s，修好后可手动 launchctl load 它）", err, path)
	}
	log.Infof("cloudflared 已注册开机自启 (launchd: %s)", plistLabel)
	fmt.Println(successNote(plistLabel, path, "登录时自动启动；手动启停: launchctl load/unload "+path, "cloudflared"))
	return nil
}

// Uninstall 卸载任务并删掉 plist。
func (l *Launchd) Uninstall() error {
	path := l.plistPath()
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("未找到 %s，似乎没有注册过开机自启", path)
	}
	if err := exec.Command("launchctl", "unload", path).Run(); err != nil {
		log.Debugf("launchctl unload 未成功（任务可能已不在）: %v", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", path, err)
	}
	log.Infof("cloudflared 已取消开机自启（已删除 %s）", path)
	return nil
}

// Running 任务是否已加载。launchctl list <标签> 在有这个任务时才会打印内容
func (l *Launchd) Running() bool {
	out, err := exec.Command("launchctl", "list", plistLabel).Output()
	return err == nil && len(out) > 0
}

// New 返回当前平台的服务实现（macOS 是 LaunchAgent）
func New() Service { return &Launchd{} }
