//go:build !windows

// 为什么会有多个 process_xxx.go 文件？——Go 按"文件"区分平台，不是按代码行：
//
// 写法一：文件名后缀自动生效，零配置。
//
//	文件名以 _windows.go / _darwin.go / _linux.go 结尾时，构建系统自动只在该平台编译它。
//	所以 process_darwin.go 只在 mac 编译，process_windows.go 只在 windows 编译。
//
// 写法二：//go:build 头（本文件就是）。
//
//	后缀只认 GOOS 名字，unix 不是合法的 GOOS → 光叫 process_unix.go 不会限定平台，
//	必须在 package 行上方声明 //go:build unix（"只在 unix 系编译"）。
//	约束能组合：linux || darwin、!windows 都可以。
//	注意：//go:build 行与 package 行之间必须空一行，否则不生效。
//
// 写法三：Go 没有 #ifdef。
//
//	不能在一个文件里写"windows 就走 A、linux 就走 B"——普通 if 两个分支都会被编译，
//	windows 专用的系统调用在 mac 上直接编译报错。
//	所以跨平台差异只能拆成多个文件，各写【同名】函数，
//	反正一次只编译一个平台的文件，同名不冲突。
//
// 效果：调用方只调一个函数名，编译时自动链接当前平台的实现，不用关心跑在什么系统上。
package daemon

import "syscall"

// processRunning 探测 PID 对应的进程是否存活。
// syscall.Kill(pid, 0)：信号 0 不发送任何信号、不杀进程，
// 系统只回答一件事——"这个 PID 合法吗？"
// 返回 nil = 进程存在；ESRCH = 没这进程；EPERM = 存在但无权发信号（也算活着）
func processRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// processKill 优雅终止：SIGTERM 让程序自己清理退出（可被捕获做收尾）。
// 用 SIGTERM 而不是 SIGEMT/SIGKILL：前者是各平台通用的"请你退出"，
// 后者在 Linux 上根本不存在（编译就过不了），而 SIGKILL 不给程序收尾的机会。
func processKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// processInterrupt：向进程发送 SIGINT（等效终端 Ctrl+C），让程序自我清理
func processInterrupt(pid int) error {
	return syscall.Kill(pid, syscall.SIGINT)
}
