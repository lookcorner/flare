package relay

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"flare/internal/config"
)

// TestGenerateFrpcConfig 校验生成的 frpc.toml 内容。
// 这个文件是我们交给外部程序（frpc）的唯一契约：写错了 frpc 只会启动失败，
// 报错还含糊，所以在本地把格式钉死。
func TestGenerateFrpcConfig(t *testing.T) {
	// config.Dir() 内部用 sync.Once 缓存结果，所以必须在"本测试里第一次调用它之前"
	// 就设好 FLARE_DIR。relay 包目前只有这一个测试，顺序有保证；将来加测试要留意。
	// 故意用不存在的嵌套目录：顺带验证 GenerateFrpcConfig 会自己建目录
	dir := filepath.Join(t.TempDir(), "nested")
	t.Setenv("FLARE_DIR", dir)

	rc := &config.RelayConfig{
		Server: "1.2.3.4:7000",
		Token:  "s3cret",
		Rules: []config.RelayRule{
			{Name: "ssh", Proto: "tcp", LocalPort: 22, RemotePort: 2200},
			// 不写 LocalIP：应默认 127.0.0.1；不写 RemotePort：应完全不输出这一行
			{Name: "game", Proto: "udp", LocalIP: "192.168.1.9", LocalPort: 19132},
		},
	}
	if err := GenerateFrpcConfig(rc); err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}

	data, err := os.ReadFile(FrpcConfigPath())
	if err != nil {
		t.Fatalf("读配置失败: %v", err)
	}
	got := string(data)

	// 地址必须是"主机 + 端口"两个字段：frp 的 serverAddr 不接受 "1.2.3.4:7000"
	wants := []string{
		`serverAddr = "1.2.3.4"`,
		"serverPort = 7000",
		`auth.token = "s3cret"`,
		// 写明这一行才能让 relay up 的"启动即退出"判断成立
		"loginFailExit = true",
		"[[proxies]]",
		`name = "ssh"`,
		`type = "tcp"`,
		`type = "udp"`,
		`localIP = "127.0.0.1"`,
		`localIP = "192.168.1.9"`,
		`localPort = 22`,
		"remotePort = 2200",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("生成的配置缺少 %q\n--- 实际内容 ---\n%s", w, got)
		}
	}
	if strings.Contains(got, `"1.2.3.4:7000"`) {
		t.Error("serverAddr 里不能带端口")
	}
	// 只有第一条规则写了 remotePort，所以整个文件里 remotePort 只能出现一次
	if n := strings.Count(got, "remotePort"); n != 1 {
		t.Errorf("remotePort 出现 %d 次，期望 1 次（缺省 = 由 frps 分配）\n%s", n, got)
	}
	// 文件里存着 frps 的令牌，权限必须是"只有自己可读写"
	if runtime.GOOS != "windows" {
		info, err := os.Stat(FrpcConfigPath())
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("文件权限 = %o, 期望 600", perm)
		}
	}
}

// 没配服务器时不能写出一个残缺配置，而要明确让用户先去 relay init
func TestGenerateFrpcConfigWithoutServer(t *testing.T) {
	t.Setenv("FLARE_DIR", t.TempDir())
	if err := GenerateFrpcConfig(&config.RelayConfig{}); err == nil {
		t.Fatal("期望报错，实际成功")
	}
}
