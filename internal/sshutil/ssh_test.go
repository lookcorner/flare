package sshutil

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// 这些测试都不联网、不执行真的 ssh：
// 一是断言参数拼装，二是用一个"假 ssh"验证 stdin/stdout 的接管方式

func TestAddr(t *testing.T) {
	cases := []struct {
		cfg  ConnectConfig
		want string
	}{
		{ConnectConfig{Host: "1.2.3.4", Port: 2222}, "1.2.3.4:2222"},
		{ConnectConfig{Host: "example.com"}, "example.com:22"}, // 端口留空按 22
		{ConnectConfig{Host: "::1", Port: 22}, "[::1]:22"},     // IPv6 要加方括号
	}
	for _, c := range cases {
		if got := c.cfg.Addr(); got != c.want {
			t.Errorf("Addr() = %q, 期望 %q", got, c.want)
		}
	}
}

func TestTarget(t *testing.T) {
	if got := (ConnectConfig{Host: "1.2.3.4", User: "root"}).Target(); got != "root@1.2.3.4" {
		t.Errorf("Target() = %q", got)
	}
	// 没给用户名时不能拼出 "@1.2.3.4"
	if got := (ConnectConfig{Host: "1.2.3.4"}).Target(); got != "1.2.3.4" {
		t.Errorf("Target() = %q", got)
	}
	// IPv6 字面量要加方括号，否则 ssh 分不清冒号和 user@
	if got := (ConnectConfig{Host: "::1", User: "root"}).Target(); got != "root@[::1]" {
		t.Errorf("IPv6 Target() = %q, 期望 root@[::1]", got)
	}
	if got := (ConnectConfig{Host: "[::1]"}).Target(); got != "[::1]" {
		t.Errorf("已带方括号的 IPv6 不该重复加: %q", got)
	}
}

// 参数顺序就是 ssh 命令行顺序：-p 端口 -i 私钥 -o … 目标
func TestBaseArgs(t *testing.T) {
	cfg := ConnectConfig{Host: "1.2.3.4", Port: 2222, User: "root", KeyPath: "/tmp/id_ed25519"}
	want := []string{
		"-p", "2222",
		"-i", "/tmp/id_ed25519",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"root@1.2.3.4",
	}
	if got := cfg.baseArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("baseArgs() = %v\n期望 %v", got, want)
	}
}

// 缺省值：端口 22；没给私钥就不传 -i（交给 ssh 自己找 agent/默认密钥）
func TestBaseArgsDefaults(t *testing.T) {
	cfg := ConnectConfig{Host: "example.com", User: "deploy"}
	want := []string{
		"-p", "22",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"deploy@example.com",
	}
	if got := cfg.baseArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("baseArgs() = %v\n期望 %v", got, want)
	}
}

// ~ 要展开：exec.Command 不经过 shell，不会帮忙展开
func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("取不到家目录: %v", err)
	}
	if got := expandHome("~/.ssh/id_rsa"); got != filepath.Join(home, ".ssh", "id_rsa") {
		t.Errorf("expandHome(~/...) = %q", got)
	}
	if got := expandHome("~"); got != home {
		t.Errorf("expandHome(~) = %q", got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("绝对路径不该被改动: %q", got)
	}
}

// 命令必须是 argv 的最后一个元素：中途没有任何拼接的 shell 字符串
func TestArgsForKeepsScriptWhole(t *testing.T) {
	c := &Client{cfg: ConnectConfig{Host: "h", Port: 22, User: "root"}}
	script := `grep 'auth.token' /etc/frps/frps.toml | sed 's/.*"\(.*\)"/\1/'`
	args := c.argsFor(script)
	if args[len(args)-1] != script {
		t.Errorf("最后一个参数 = %q, 期望 %q", args[len(args)-1], script)
	}
}

// 多行脚本走 bash -s + stdin，而不是当命令参数
func TestScriptArgs(t *testing.T) {
	c := &Client{cfg: ConnectConfig{Host: "h", User: "root"}}
	want := []string{
		"-p", "22",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"root@h",
		"bash", "-s",
	}
	if got := c.scriptArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("scriptArgs() = %v\n期望 %v", got, want)
	}
}

// 私钥路径写错：Connect 在起 ssh 之前就拦住，而且不泄露其他信息
func TestConnectRejectsMissingKey(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-key")
	_, err := Connect(ConnectConfig{Host: "1.2.3.4", KeyPath: missing})
	if err == nil || !strings.Contains(err.Error(), "读不到私钥") {
		t.Fatalf("期望报错提到读不到私钥，实际: %v", err)
	}
}

func TestConnectRejectsEmptyHost(t *testing.T) {
	if _, err := Connect(ConnectConfig{Host: "  "}); err == nil {
		t.Fatal("空地址应当报错")
	}
}

// writeFakeSSH 造一个假 ssh 可执行文件：
// 收到的参数逐行写进 argsPath，stdin 原样落到 stdinPath，再把 body 打到 stdout、按 code 退出。
// 用它验证"真的按这些参数调用了 ssh、输入输出确实接管了"，全程不碰网络
func writeFakeSSH(t *testing.T, body string, code int) (bin, argsPath, stdinPath string) {
	t.Helper()
	dir := t.TempDir()
	argsPath = filepath.Join(dir, "args")
	stdinPath = filepath.Join(dir, "stdin")
	bin = filepath.Join(dir, "ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + argsPath + "\n" +
		"cat > " + stdinPath + "\n" +
		body + "\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return bin, argsPath, stdinPath
}

func useFakeSSH(t *testing.T, bin string) {
	t.Helper()
	old := sshBin
	sshBin = bin
	t.Cleanup(func() { sshBin = old })
}

func TestRunScriptFeedsStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 ssh 用 shell 脚本实现，跳过 Windows")
	}
	bin, argsPath, stdinPath := writeFakeSSH(t, "true", 0)
	useFakeSSH(t, bin)

	c := &Client{cfg: ConnectConfig{Host: "example.com", Port: 2200, User: "root"}}
	script := "set -euo pipefail\necho 远程脚本"
	if err := c.RunScript(script); err != nil {
		t.Fatalf("RunScript 出错: %v", err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "-p\n2200\n-o\nStrictHostKeyChecking=accept-new\n-o\nConnectTimeout=10\nroot@example.com\nbash\n-s\n"
	if string(args) != want {
		t.Errorf("ssh 参数 =\n%q\n期望\n%q", args, want)
	}
	in, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(in) != script {
		t.Errorf("脚本没有原样进 stdin: %q", in)
	}
}

// 取回输出时首尾空白要去掉（远程命令的输出常带换行）
func TestRunCommandOutputCaptures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 ssh 用 shell 脚本实现，跳过 Windows")
	}
	bin, _, _ := writeFakeSSH(t, "echo x86_64", 0)
	useFakeSSH(t, bin)

	c := &Client{cfg: ConnectConfig{Host: "h", User: "root"}}
	out, err := c.RunCommandOutput("uname -m")
	if err != nil {
		t.Fatalf("RunCommandOutput 出错: %v", err)
	}
	if out != "x86_64" {
		t.Errorf("输出 = %q, 期望 %q", out, "x86_64")
	}
}

// 远程命令失败时：错误要带出来，输出也不能丢——
// preflight 就是靠 systemctl is-active 的这行文字判断服务状态的
func TestRunCommandOutputKeepsOutputOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 ssh 用 shell 脚本实现，跳过 Windows")
	}
	bin, _, _ := writeFakeSSH(t, "echo inactive", 1)
	useFakeSSH(t, bin)

	c := &Client{cfg: ConnectConfig{Host: "h"}}
	out, err := c.RunCommandOutput("systemctl is-active flare-frps")
	if err == nil {
		t.Fatal("退码非零时应返回错误")
	}
	if out != "inactive" {
		t.Errorf("输出 = %q, 期望 %q", out, "inactive")
	}
}
