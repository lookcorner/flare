//go:build linux

package service

import (
	"path/filepath"
	"strings"
	"testing"
)

// 单位文件是我们交给 systemd 的唯一契约：ExecStart 写错就是"开机起了个跑不起来的进程"，
// 所以名字、路径、参数逐项钉死。在 mac 上跑不到这个文件（build tag 是 linux），
// 代码正确性靠 GOOS=linux go vet 把关
func TestRenderUnit(t *testing.T) {
	const (
		binPath = "/home/me/.flare/bin/cloudflared"
		logPath = "/home/me/.flare/logs/cloudflared.log"
	)
	got, err := renderUnit(unitData{
		Name:      unitName,
		ExecStart: joinExeArgs(binPath, cloudflaredArgs("tok_abc123")),
		LogPath:   logPath,
	})
	if err != nil {
		t.Fatalf("渲染 systemd 单位失败: %v", err)
	}
	wants := []string{
		"[Unit]",
		"Description=" + unitName,
		"[Service]",
		"ExecStart=" + binPath + " tunnel --protocol http2 run --token tok_abc123",
		"Restart=always",
		"RestartSec=5",
		"StandardOutput=append:" + logPath,
		"StandardError=append:" + logPath,
		"[Install]",
		// 用户单位要挂到 default.target 上，multi-user.target 是系统单位的写法
		"WantedBy=default.target",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("单位文件缺少 %q\n--- 实际内容 ---\n%s", w, got)
		}
	}
}

// 单位文件落在用户目录里（XDG 规范）：不写 /etc，也就不需要 sudo
func TestUnitPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	want := filepath.Join(home, ".config", "systemd", "user", "flare-cloudflared.service")
	if got := (&Systemd{}).unitPath(); got != want {
		t.Errorf("unitPath = %q, 期望 %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	want = "/xdg/systemd/user/flare-cloudflared.service"
	if got := (&Systemd{}).unitPath(); got != want {
		t.Errorf("unitPath（XDG_CONFIG_HOME 优先）= %q, 期望 %q", got, want)
	}
}

func TestNewIsSystemd(t *testing.T) {
	if _, ok := New().(*Systemd); !ok {
		t.Fatalf("linux 上 New() 应返回 *Systemd，实际 %T", New())
	}
}
