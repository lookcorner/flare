// Package service 把 flare 依赖的后台程序（cloudflared / frpc）注册成"开机自启"的系统服务。
//
// 为什么需要它：flare up（以及 flare relay up）起的进程是 flare 的子进程，
// 终端一关、机器一重启就没了。想让隧道长期在线，得把进程交给操作系统托管：
//
//	macOS   LaunchAgent（~/Library/LaunchAgents/*.plist）
//	Linux   systemd 用户单位（~/.config/systemd/user/*.service）
//	Windows 系统服务（sc create）
//
// 三套写法差异很大，对外只留三件事：装、卸、查。
// 每个平台一个文件，用 //go:build 头限定编译范围——与 internal/daemon 的 process_*.go 同一思路：
// 调用方只认函数名，编译时自动连到当前平台的实现。
package service

import (
	"fmt"
	"strings"

	"flare/internal/log"
)

// Service 系统服务管理接口
type Service interface {
	Install(binPath, token string) error
	Uninstall() error
	Running() bool
}

// cloudflaredArgs 是注册成服务时 cloudflared 的启动参数。
// 必须与 daemon.Start 手动启动时逐字一致：同一件事有两种启动方式，
// 参数一旦分叉就会出现"flare up 能连上、开机却起不来"这类最难查的问题。
func cloudflaredArgs(token string) []string {
	return []string{"tunnel", "--protocol", "http2", "run", "--token", token}
}

// joinExeArgs 拼"程序 + 参数"的一行命令：systemd 的 ExecStart、Windows 的 binPath= 都要这种形式。
// 路径里带空格时必须整段加双引号——systemd 按空白切分参数，Windows 服务管理器也按空格找 exe，
// 不包住就会把路径从空格处截断（家目录写成 C:\Users\Zhang San\ 这种在 Windows 上很常见）。
func joinExeArgs(binPath string, args []string) string {
	exe := binPath
	if strings.ContainsAny(exe, " \t") {
		exe = `"` + exe + `"`
	}
	if len(args) == 0 {
		return exe
	}
	return exe + " " + strings.Join(args, " ")
}

// prepareLog 先把日志文件建出来，再交给系统服务往里追加。
// 两个原因：launchd / systemd 只负责往文件里追加，不会替我们建目录，目录不存在服务直接起不来；
// 而走 log.Open 建出来的文件权限是 0600，日志里会出现隧道 ID、域名、请求错误，不该让同机器的其他用户读到。
func prepareLog(name string) error {
	f, err := log.Open(name)
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %w", err)
	}
	return f.Close()
}

// successNote 拼"注册成功"后要告诉用户的三件事：服务文件写在哪、以后怎么启停、日志在哪看。
// 三个平台共用同一段文案——同一件事在三份实现里说法不一致，用户会以为是两回事
func successNote(serviceName, installedTo, startStop, logName string) string {
	return fmt.Sprintf("已注册开机自启: %s\n  位置: %s\n  启停方式: %s\n  日志: %s（flare log %s -f 可实时查看）",
		serviceName, installedTo, startStop, log.Path(logName), logName)
}
