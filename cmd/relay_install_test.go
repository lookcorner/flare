package cmd

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	testFrpcBin    = "/home/me/.flare/bin/frpc"
	testFrpcConfig = "/home/me/.flare/frpc.toml"
	testFrpcLog    = "/home/me/.flare/logs/frpc.log"
)

func testRelaySpec(label string) relaySpec {
	return relaySpec{
		Label:   label,
		BinPath: testFrpcBin,
		Args:    relayArgs(testFrpcConfig),
		LogPath: testFrpcLog,
	}
}

// 三块模板都是纯函数（入参 → 字符串），所以在 mac 上就能把 Linux / Windows 的写法
// 一起断言，不必等到在对应系统上真装一遍才发现拼错。这里不写文件、不执行任何命令
func TestRenderRelayPlist(t *testing.T) {
	got, err := renderRelayPlist(testRelaySpec(relayPlistLabel))
	if err != nil {
		t.Fatalf("渲染 plist 失败: %v", err)
	}
	wants := []string{
		"<string>" + relayPlistLabel + "</string>",
		"<string>" + testFrpcLog + "</string>",
		"<key>ProgramArguments</key>",
		"<key>KeepAlive</key>",
		"<key>RunAtLoad</key>",
		"<key>StandardOutPath</key>",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("plist 缺少 %q\n--- 实际内容 ---\n%s", w, got)
		}
	}
	// 启动参数就是 daemon.StartRelay 那句 "frpc -c frpc.toml"，顺序不能反（-c 必须在前）
	seq := "<string>" + testFrpcBin + "</string> <string>-c</string> <string>" + testFrpcConfig + "</string>"
	if flat := strings.Join(strings.Fields(got), " "); !strings.Contains(flat, seq) {
		t.Errorf("ProgramArguments 与 daemon.StartRelay 不一致，期望包含:\n%s\n--- 实际内容 ---\n%s", seq, got)
	}
}

func TestRenderRelayUnit(t *testing.T) {
	// 单位名（flare-relay）和 plist 标签（com.flare.frpc）不是同一个名字，各用各的
	got, err := renderRelayUnit(testRelaySpec(relayUnitName))
	if err != nil {
		t.Fatalf("渲染 systemd 单位失败: %v", err)
	}
	wants := []string{
		"[Unit]",
		"Description=" + relayUnitName,
		"ExecStart=" + testFrpcBin + " -c " + testFrpcConfig,
		"Restart=always",
		"RestartSec=5",
		"StandardOutput=append:" + testFrpcLog,
		"StandardError=append:" + testFrpcLog,
		"[Install]",
		// 用户单位挂 default.target（multi-user.target 是系统单位的写法）
		"WantedBy=default.target",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("单位文件缺少 %q\n--- 实际内容 ---\n%s", w, got)
		}
	}
}

// Windows 那一份没有模板文件，binPath= 就是全部：程序 + 参数拼一行，
// 路径带空格必须加引号，否则会被从空格处截断
func TestRelayScBinPath(t *testing.T) {
	const winConfig = `C:\Users\me\.flare\frpc.toml`
	winSpec := func(bin string) relaySpec {
		return relaySpec{Label: relaySvcName, BinPath: bin, Args: relayArgs(winConfig), LogPath: `C:\Users\me\.flare\logs\frpc.log`}
	}
	cases := []struct {
		bin  string
		want string
	}{
		{`C:\Users\me\.flare\bin\frpc.exe`, `C:\Users\me\.flare\bin\frpc.exe -c ` + winConfig},
		{`C:\Users\Zhang San\.flare\bin\frpc.exe`, `"C:\Users\Zhang San\.flare\bin\frpc.exe" -c ` + winConfig},
	}
	for _, c := range cases {
		if got := relayScBinPath(winSpec(c.bin)); got != c.want {
			t.Errorf("relayScBinPath(%q) = %q, 期望 %q", c.bin, got, c.want)
		}
	}
}

// 三个平台的名字不能改错：plist 标签给 launchctl 认、单位名给 systemctl 认、服务名给 sc 认。
// 改名字会让老用户重装时留下两个任务，所以在这里明确钉住
func TestRelayServiceNames(t *testing.T) {
	if relayPlistLabel != "com.flare.frpc" {
		t.Errorf("plist 标签 = %q, 期望 com.flare.frpc", relayPlistLabel)
	}
	if relayUnitName != "flare-relay" {
		t.Errorf("systemd 单位名 = %q, 期望 flare-relay", relayUnitName)
	}
	if relaySvcName != "flare-relay" {
		t.Errorf("Windows 服务名 = %q, 期望 flare-relay", relaySvcName)
	}
}

// 服务文件路径落在用户目录里（不写 /etc，也就不需要 sudo）。
// 只改 HOME / XDG_CONFIG_HOME 环境变量，不碰真实目录
func TestRelayServicePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	wantPlist := filepath.Join(home, "Library", "LaunchAgents", "com.flare.frpc.plist")
	if got := relayPlistPath(); got != wantPlist {
		t.Errorf("relayPlistPath = %q, 期望 %q", got, wantPlist)
	}
	wantUnit := filepath.Join(home, ".config", "systemd", "user", "flare-relay.service")
	if got := relayUnitPath(); got != wantUnit {
		t.Errorf("relayUnitPath = %q, 期望 %q", got, wantUnit)
	}
}

// 启动参数必须与 daemon.StartRelay 手动启动时逐字一致：注册进服务的就是这条命令，
// 参数分叉就会出现"relay up 能用、开机却起不来"这类最难查的问题
func TestRelayArgs(t *testing.T) {
	want := []string{"-c", "/x/frpc.toml"}
	if got := relayArgs("/x/frpc.toml"); !reflect.DeepEqual(got, want) {
		t.Errorf("relayArgs = %v, 期望 %v", got, want)
	}
}
