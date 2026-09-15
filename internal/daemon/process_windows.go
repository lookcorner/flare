//go:build windows

package daemon

import (
	"os/exec"
	"strconv"
	"strings"
)

// Windows 没有 kill(pid, 0)。用 tasklist 查进程表：输出里含 "PID" 行 = 活着。
func processRunning(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// taskkill /PID xxx 优雅结束进程（可加 /T 连子进程一起杀）
func processKill(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid)).Run()
}

// windows没有 SIGINT 概念；用 taskkill 不带 /F = 温和请求退出
func processInterrupt(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid)).Run()
}
