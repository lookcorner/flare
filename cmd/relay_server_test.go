package cmd

import (
	"encoding/hex"
	"flare/internal/download"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// buildInstallScript 是"远程装了什么"的唯一出处，脚本片段就是契约：
// 版本号、端口、unit 名、frps.toml 内容改错了都在这里被拦下
func TestBuildInstallScript(t *testing.T) {
	script := buildInstallScript(7000)
	fragments := []string{
		`FRP_VERSION="` + download.FrpVersion() + `"`, // 和本地下载同一个版本
		"BIND_PORT=7000",
		`SERVICE_NAME="flare-frps"`, // unit 名：flare 自己的，不占用别人可能的 frps
		"set -euo pipefail",
		`FILENAME="frp_${FRP_VERSION}_linux_${FRP_ARCH}.tar.gz"`,
		`https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}/${FILENAME}`,
		`install -m 755 "$TMP_DIR/frp_${FRP_VERSION}_linux_${FRP_ARCH}/frps" "$INSTALL_DIR/frps"`, // 装的是 frps，不是 frpc
		"/usr/local/bin",
		`CONFIG_DIR="/etc/frps"`,
		`$CONFIG_DIR/frps.toml`,
		"bindPort = ${BIND_PORT}", // frps.toml 内容
		`auth.token = "${TOKEN}"`, // Token 只能是远程生成的变量，不能写死
		`/etc/systemd/system/${SERVICE_NAME}.service`,
		"Description=frps relay server (flare)",
		`ExecStart=${INSTALL_DIR}/frps -c ${CONFIG_DIR}/frps.toml`,
		`systemctl enable --now "$SERVICE_NAME"`,
		"uname -m",
	}
	for _, f := range fragments {
		if !strings.Contains(script, f) {
			t.Errorf("安装脚本缺少片段: %q", f)
		}
	}
	// 移植痕迹不能留：脚本是 flare 生成的
	if strings.Contains(script, "cftunnel") {
		t.Error("安装脚本里残留了 cftunnel 字样")
	}
	// Token 必须现场生成（openssl/od 两条路），不能由本机传进去
	if !strings.Contains(script, "openssl rand -hex 16") || !strings.Contains(script, "/dev/urandom") {
		t.Error("安装脚本应在远端生成随机 Token")
	}
}

// 端口来自参数，不是写死的 7000
func TestBuildInstallScriptPort(t *testing.T) {
	script := buildInstallScript(7443)
	if !strings.Contains(script, "BIND_PORT=7443") {
		t.Error("BIND_PORT 没有用传入的端口")
	}
	if strings.Contains(script, "BIND_PORT=7000") {
		t.Error("端口写死成了 7000")
	}
}

// frps.toml 生成与解析必须对得上：install 重跑时要能从文件里读回 Token 复用
func TestFrpsTomlRoundTrip(t *testing.T) {
	text := buildFrpsToml(7000, "a1b2c3")
	if !strings.Contains(text, "bindPort = 7000") || !strings.Contains(text, `auth.token = "a1b2c3"`) {
		t.Fatalf("生成的 frps.toml 内容不对:\n%s", text)
	}
	got := parseFrpsConfig(text)
	if got.Port != 7000 || got.Token != "a1b2c3" {
		t.Errorf("解析结果 = %+v", got)
	}
}

// 手工改过的文件也要能读：空格、行尾注释、不带引号的写法
func TestParseFrpsConfigTolerant(t *testing.T) {
	text := `# 手工改过的配置
bindPort=8080   # 换个端口
auth.token = "tok-123"
`
	got := parseFrpsConfig(text)
	if got.Port != 8080 {
		t.Errorf("Port = %d, 期望 8080", got.Port)
	}
	if got.Token != "tok-123" {
		t.Errorf("Token = %q, 期望 %q", got.Token, "tok-123")
	}
}

// 认不出来的内容返回零值，调用方用默认端口 + 新 Token 兜底，不能崩
func TestParseFrpsConfigGarbage(t *testing.T) {
	if got := parseFrpsConfig("这不是 TOML\nbindPort = abc\n"); got.Port != 0 || got.Token != "" {
		t.Errorf("乱内容应返回零值，实际 %+v", got)
	}
}

func TestTomlValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"abc"`, "abc"},
		{`  "abc"  `, "abc"},
		{`7000 # 注释`, "7000"},
		{`7000`, "7000"},
		{`"a" # 注释`, "a"},
		{``, ""},
	}
	for _, c := range cases {
		if got := tomlValue(c.in); got != c.want {
			t.Errorf("tomlValue(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// Token 是 128 位随机数的十六进制写法：32 个字符，两次调用不能重复
func TestRandomToken(t *testing.T) {
	a := randomToken()
	b := randomToken()
	if len(a) != 32 {
		t.Fatalf("Token 长度 = %d, 期望 32", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("Token 不是十六进制: %q", a)
	}
	if a == b {
		t.Error("两次生成的 Token 相同，随机源有问题")
	}
}

// 进程探活与停止：借一个真实的 sleep 进程验证（不涉及 frps、不联网、不动数据目录）。
// "不在运行"的判断是卸载、防重复启动的根基，错一次就会误杀无关进程或重复起服务
func TestProcessAliveAndStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 sh + sleep 验证，跳过 Windows")
	}
	// 让 sh 起一个后台进程后立刻退出：sleep 被过继给 init，不是本进程的子进程，
	// 刻意模拟"frps 由上一次 flare 启动"的真实关系（没有僵尸进程干扰）。
	// sleep 的 stdout/stderr 必须重定向：否则它抱着管道不放，Output() 会一直等下去
	out, err := exec.Command("sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Skipf("起不了测试进程: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		t.Skipf("拿不到测试进程 PID: %q", out)
	}
	defer func() {
		if processAlive(pid) {
			if p, err := os.FindProcess(pid); err == nil {
				p.Kill()
			}
		}
	}()

	if !processAlive(pid) {
		t.Fatalf("PID %d 应当活着", pid)
	}
	if processAlive(0) || processAlive(-1) {
		t.Error("非法 PID 不该算活着")
	}
	if err := stopFrps(pid); err != nil {
		t.Fatalf("停止进程失败: %v", err)
	}
	if processAlive(pid) {
		t.Errorf("SIGTERM 后 PID %d 仍未退出", pid)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
		wantOK   bool
	}{
		{"1.2.3.4:2222", "1.2.3.4", 2222, true},
		{"example.com:22", "example.com", 22, true},
		{"[::1]:22", "::1", 22, true},
		{"example.com", "", 0, false},   // 纯主机名
		{"::1", "", 0, false},           // 裸 IPv6 不是 host:port
		{"1.2.3.4:0", "", 0, false},     // 端口越界
		{"1.2.3.4:70000", "", 0, false}, // 端口越界
		{"1.2.3.4:", "", 0, false},      // 只有冒号
	}
	for _, c := range cases {
		host, port, ok := splitHostPort(c.in)
		if ok != c.wantOK || host != c.wantHost || port != c.wantPort {
			t.Errorf("splitHostPort(%q) = (%q, %d, %v), 期望 (%q, %d, %v)",
				c.in, host, port, ok, c.wantHost, c.wantPort, c.wantOK)
		}
	}
}

// 命令树与默认值：子命令挂错父命令、默认端口改错，用户在 help 里就发现不了
func TestRelayServerCommandWiring(t *testing.T) {
	server := findSubCommand(relayCmd, "server")
	if server == nil {
		t.Fatal("relay 下没有 server 子命令")
	}
	for _, name := range []string{"install", "uninstall", "status", "setup"} {
		if findSubCommand(server, name) == nil {
			t.Errorf("relay server 下缺少 %s 子命令", name)
		}
	}
	wantDefaults := map[string]map[string]string{
		"install": {"port": "7000"},
		"setup":   {"port": "22", "user": "root", "frps-port": "7000"},
	}
	for cmdName, flags := range wantDefaults {
		c := findSubCommand(server, cmdName)
		for flag, want := range flags {
			f := c.Flags().Lookup(flag)
			if f == nil {
				t.Errorf("%s 缺少 --%s", cmdName, flag)
				continue
			}
			if f.DefValue != want {
				t.Errorf("%s --%s 默认值 = %q, 期望 %q", cmdName, flag, f.DefValue, want)
			}
		}
	}
}

func findSubCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
