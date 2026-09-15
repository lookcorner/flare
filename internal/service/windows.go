//go:build windows

package service

import (
	"fmt"
	"os/exec"
	"strings"

	"flare/internal/log"
)

// Windows 用系统自带的 sc.exe 注册服务，不引第三方工具（nssm 之类）：
// 少一个依赖，配置虽然朴素但够用。
type Windows struct{}

// svcName 服务名（sc create 的第一个参数）
const svcName = "flare-cloudflared"

// scBinPath 拼 sc create 的 binPath= 值：二进制 + 启动参数（与 daemon.Start 一致）。
// 复用 joinExeArgs 处理"路径带空格"的情形：Windows 上 C:\Users\Zhang San\ 这种家目录很常见，
// 不给 exe 加引号的话服务管理器会从空格处截断路径，然后报一句"找不到文件"
func scBinPath(binPath string, args []string) string {
	return joinExeArgs(binPath, args)
}

// Install 注册并启动 Windows 服务。需要管理员权限的终端：sc create 会直接回报拒绝访问。
func (w *Windows) Install(binPath, token string) error {
	if err := prepareLog("cloudflared"); err != nil {
		return err
	}
	// 先删后建：重复 install 时 sc create 会报"服务已存在"，删一次再建才是幂等的
	if err := exec.Command("sc", "delete", svcName).Run(); err != nil {
		log.Debugf("sc delete 未成功（通常是没有同名服务）: %v", err)
	}
	// "binPath=" 和 "start=" 后面的等号是 sc 的约定写法，值由下一个参数给出
	if err := exec.Command("sc", "create", svcName, "binPath=", scBinPath(binPath, cloudflaredArgs(token)), "start=", "auto").Run(); err != nil {
		return fmt.Errorf("创建服务失败: %w（需要在管理员终端里执行）", err)
	}
	if err := exec.Command("sc", "start", svcName).Run(); err != nil {
		return fmt.Errorf("启动服务失败: %w（服务已注册，可手动执行 sc start %s）", err, svcName)
	}
	log.Infof("cloudflared 已注册开机自启 (Windows 服务: %s)", svcName)
	fmt.Println(successNote(svcName, "Windows 服务管理器（sc create）", "sc start/stop "+svcName, "cloudflared"))
	fmt.Println("  提示: Windows 服务没有终端，cloudflared 的输出不会写进上面的日志文件；状态用 sc query " + svcName + " 查看")
	return nil
}

// Uninstall 停掉并删除服务
func (w *Windows) Uninstall() error {
	exec.Command("sc", "stop", svcName).Run() // 没在跑或本来就不存在都无所谓，接着删
	if err := exec.Command("sc", "delete", svcName).Run(); err != nil {
		return fmt.Errorf("删除服务失败: %w（用 sc query %s 可确认服务是否存在）", err, svcName)
	}
	log.Infof("cloudflared 已取消开机自启（已删除服务 %s）", svcName)
	return nil
}

// Running 服务是否处于运行状态。sc query 的输出里状态词仍是英文（RUNNING），
// 不受系统语言影响，所以直接匹配它
func (w *Windows) Running() bool {
	out, err := exec.Command("sc", "query", svcName).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "RUNNING")
}

// New 返回当前平台的服务实现（Windows 是 sc 服务）
func New() Service { return &Windows{} }
