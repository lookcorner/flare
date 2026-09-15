//go:build darwin

package service

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	testBin     = "/Users/me/.flare/bin/cloudflared"
	testLogPath = "/Users/me/.flare/logs/cloudflared.log"
	testToken   = "tok_abc123"
)

// 渲染出来的 plist 就是我们交给 launchd 的唯一契约：标签写错它就不认识这个任务，
// 参数写错就成了"开机起了个跑不起来的进程"。所以逐项钉死
func TestRenderPlist(t *testing.T) {
	got, err := renderPlist(plistData{
		Label:   plistLabel,
		BinPath: testBin,
		Args:    cloudflaredArgs(testToken),
		LogPath: testLogPath,
	})
	if err != nil {
		t.Fatalf("渲染 plist 失败: %v", err)
	}
	wants := []string{
		"<string>" + plistLabel + "</string>",
		"<string>" + testBin + "</string>",
		"<string>" + testLogPath + "</string>",
		"<key>Label</key>",
		"<key>ProgramArguments</key>",
		"<key>KeepAlive</key>",
		"<key>RunAtLoad</key>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("plist 缺少 %q\n--- 实际内容 ---\n%s", w, got)
		}
	}

	// 启动参数连顺序一起断言（--protocol 必须紧跟 http2、run 在 --token 之前）。
	// 折掉模板的换行缩进再比，免得断言被排版影响
	seq := "<string>" + testBin + "</string> <string>tunnel</string> <string>--protocol</string> <string>http2</string>" +
		" <string>run</string> <string>--token</string> <string>" + testToken + "</string>"
	if flat := strings.Join(strings.Fields(got), " "); !strings.Contains(flat, seq) {
		t.Errorf("ProgramArguments 与 daemon.Start 的启动方式不一致，期望包含:\n%s\n--- 实际内容 ---\n%s", seq, got)
	}
}

// 注册进服务的参数必须与 daemon.Start 手动启动时逐字一致：
// 这条断言就是那个约定的守门人，改了一边就必须改另一边
func TestCloudflaredArgs(t *testing.T) {
	want := []string{"tunnel", "--protocol", "http2", "run", "--token", "t"}
	if got := cloudflaredArgs("t"); !reflect.DeepEqual(got, want) {
		t.Errorf("cloudflaredArgs = %v, 期望 %v", got, want)
	}
}

// joinExeArgs 被 Linux（ExecStart）和 Windows（binPath=）共用，
// 而那两个平台的文件在本机编译不进来，所以在这里把它测掉：
// 路径带空格必须给 exe 加引号，否则参数会被从空格处截断
func TestJoinExeArgs(t *testing.T) {
	cases := []struct {
		bin  string
		args []string
		want string
	}{
		{"/home/me/.flare/bin/cloudflared", []string{"tunnel", "run"}, "/home/me/.flare/bin/cloudflared tunnel run"},
		{
			`C:\Users\Zhang San\.flare\bin\cloudflared.exe`,
			[]string{"-c", `C:\Users\Zhang San\.flare\frpc.toml`},
			`"C:\Users\Zhang San\.flare\bin\cloudflared.exe" -c C:\Users\Zhang San\.flare\frpc.toml`,
		},
		{"/bin/x", nil, "/bin/x"},
	}
	for _, c := range cases {
		if got := joinExeArgs(c.bin, c.args); got != c.want {
			t.Errorf("joinExeArgs(%q, %v) = %q, 期望 %q", c.bin, c.args, got, c.want)
		}
	}
}

// 服务文件名必须带 com.flare.cloudflared：launchctl 按标签找任务，名字对不上就是白装。
// 只改 HOME 环境变量，不碰真实的 ~/Library/LaunchAgents
func TestPlistPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, "Library", "LaunchAgents", "com.flare.cloudflared.plist")
	if got := (&Launchd{}).plistPath(); got != want {
		t.Errorf("plistPath = %q, 期望 %q", got, want)
	}
}

func TestNewIsLaunchd(t *testing.T) {
	if _, ok := New().(*Launchd); !ok {
		t.Fatalf("darwin 上 New() 应返回 *Launchd，实际 %T", New())
	}
}
