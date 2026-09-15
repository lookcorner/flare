package cmd

import (
	"strings"
	"testing"
)

// 版本号解析必须在各平台可测：所以解析和文案留在无编译标签的 wincheck.go 里，
// 只有真正调 `ver` 的那部分带 //go:build windows
func TestParseWindowsMajor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"Microsoft Windows [Version 10.0.19045.3803]", 10},
		{"Microsoft Windows [版本 10.0.19045.3803]", 10}, // 中文系统，"Version" 被本地化
		{"Microsoft Windows [Version 6.1.7601]", 6},    // Win7
		{"Microsoft Windows [Version 6.3.9600]", 6},    // Win8.1
		{"", 0},
		{"命令不存在", 0},
	}
	for _, c := range cases {
		if got := parseWindowsMajor(c.in); got != c.want {
			t.Errorf("parseWindowsMajor(%q) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

// 旧系统提示里要带上真实版本号、并且给出下一步（别只说"不支持"）
func TestUnsupportedWindowsMsg(t *testing.T) {
	msg := unsupportedWindowsMsg(6)
	for _, want := range []string{"Windows NT 6", "Windows 10", "WSL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("提示缺少 %q：\n%s", want, msg)
		}
	}
}
