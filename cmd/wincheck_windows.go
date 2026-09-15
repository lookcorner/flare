//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
)

// checkWindowsVersion 检测 Windows 版本，低于 Win10 就说完原因再退出。
//
// 为什么值得单独做一步：cloudflared 和 Go 运行时都不再支持 Win7/8.1，
// 但在旧系统上它们失败的方式是"缺 DLL""TLS 握手失败"这类看不懂的报错，
// 用户很难想到根因是系统太老。这里一句话说清。
//
// 不用 golang.org/x/sys/windows 的 RtlGetVersion：为一个版本号引一个依赖不划算，
// `cmd /c ver` 在所有受支持的 Windows 上都在
func checkWindowsVersion() {
	out, err := exec.Command("cmd", "/c", "ver").Output()
	if err != nil {
		return // 查不出来就放行，不因为这个拦住用户
	}
	major := parseWindowsMajor(string(out))
	if major == 0 || major >= 10 {
		return
	}
	fmt.Fprint(os.Stderr, unsupportedWindowsMsg(major))
	os.Exit(1)
}
