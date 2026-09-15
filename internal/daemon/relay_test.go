//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"flare/internal/config"
)

// TestRelayLifecycle 用一段"假 frpc"shell 脚本验证中继的进程生命周期。
// 为什么要造假：真 frpc 需要一台跑着 frps 的服务器，而 StartRelay 真正关心的
// 只有"进程活没活着、PID 文件对不对"，脚本就足够把这些逻辑钉住。
//
// 两个注意点：
//  1. config.Dir() 内部用 sync.Once 缓存，所以本包内只能有一个测试设置 FLARE_DIR——
//     多个场景都写在同一个测试里，就是为了这个。
//  2. 脚本用 exec 而不是直接 sleep：exec 让 shell 把自己"换成"sleep（同一个 PID），
//     这样 StopRelay 一发 SIGTERM 打到就是 sleep 本身，不会留下孤儿进程。
func TestRelayLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLARE_DIR", dir) // 必须在本包第一次调用 config.Dir() 之前设好
	fake := filepath.Join(dir, "bin", "frpc")
	pidPath := pidFile(frpcPidFile)

	writeFake := func(script string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(fake), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	rc := &config.RelayConfig{
		Server: "127.0.0.1:7000",
		Rules:  []config.RelayRule{{Name: "ssh", Proto: "tcp", LocalPort: 22, RemotePort: 2200}},
	}

	// 场景一：进程活下来 → 启动成功、PID 落盘、stop 后清干净
	writeFake("#!/bin/sh\nexec sleep 30\n")
	if err := StartRelay(rc); err != nil {
		t.Fatalf("StartRelay 失败: %v", err)
	}
	if !RelayRunning() {
		t.Error("刚启动完，RelayRunning() 应为 true")
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Errorf("PID 文件应存在: %v", err)
	}
	// 已经跑着一个就不许再起一个
	if err := StartRelay(rc); err == nil {
		t.Error("重复 StartRelay 应报错，实际成功")
	}
	if err := StopRelay(); err != nil {
		t.Fatalf("StopRelay 失败: %v", err)
	}
	if RelayRunning() {
		t.Error("停止后 RelayRunning() 应为 false")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("停止后 PID 文件应被清掉，Stat 结果: %v", err)
	}

	// 场景二：进程启动即退出（= 服务器地址/令牌配错）→ 必须报错，且不留 PID 文件。
	// 这里正是"僵尸进程"的坑：刚死掉、还没人收尸的子进程，kill(pid, 0) 照样返回成功，
	// 用它判断就会把"启动失败"报成"已启动"
	writeFake("#!/bin/sh\nexit 1\n")
	if err := StartRelay(rc); err == nil {
		t.Error("frpc 秒退时应报错，实际成功")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("启动失败后不该留下 PID 文件，Stat 结果: %v", err)
	}

	// 场景三：进程早没了、只剩过期 PID 文件 → stop 报错，但要把垃圾清掉
	if err := os.WriteFile(pidPath, []byte("999999"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := StopRelay(); err == nil {
		t.Error("frpc 没在跑时 StopRelay 应报错，实际成功")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("过期 PID 文件应被清掉，Stat 结果: %v", err)
	}
}
