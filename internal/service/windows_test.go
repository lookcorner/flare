//go:build windows

package service

import (
	"strings"
	"testing"
)

// sc create 的 binPath= 是唯一交给 Windows 服务管理器的字符串。
// 路径带空格时必须给 exe 加引号，否则路径会被从空格处截断（报"找不到文件"）——
// Windows 上 C:\Users\Zhang San\ 这种家目录很常见。在 mac 上跑不到这个文件
// （build tag 是 windows），代码正确性靠 GOOS=windows go vet 把关
func TestScBinPath(t *testing.T) {
	cases := []struct {
		bin  string
		args []string
		want string
	}{
		{
			`C:\Users\me\.flare\bin\cloudflared.exe`,
			cloudflaredArgs("tok"),
			`C:\Users\me\.flare\bin\cloudflared.exe tunnel --protocol http2 run --token tok`,
		},
		{
			`C:\Users\Zhang San\.flare\bin\cloudflared.exe`,
			cloudflaredArgs("tok"),
			`"C:\Users\Zhang San\.flare\bin\cloudflared.exe" tunnel --protocol http2 run --token tok`,
		},
	}
	for _, c := range cases {
		if got := scBinPath(c.bin, c.args); got != c.want {
			t.Errorf("scBinPath(%q) = %q, 期望 %q", c.bin, got, c.want)
		}
	}
}

func TestSvcName(t *testing.T) {
	if svcName != "flare-cloudflared" {
		t.Errorf("服务名 = %q, 期望 flare-cloudflared", svcName)
	}
	if !strings.Contains(scBinPath(`C:\x\cloudflared.exe`, cloudflaredArgs("t")), "--protocol http2 run --token t") {
		t.Error("binPath 里必须带上与 daemon.Start 一致的启动参数")
	}
}

func TestNewIsWindows(t *testing.T) {
	if _, ok := New().(*Windows); !ok {
		t.Fatalf("windows 上 New() 应返回 *Windows，实际 %T", New())
	}
}
