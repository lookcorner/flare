package cmd

import (
	"flare/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reset 的清理目标是"flare 自己在数据目录里建的东西"。这些测试盯的就是这条边界：
// 该删的在名单里，不该删的绝不在名单里
func TestLocalResetTargets(t *testing.T) {
	dir := t.TempDir()
	targets := localResetTargets(dir)
	if len(targets) == 0 {
		t.Fatal("清理名单为空")
	}

	seen := make(map[string]bool, len(targets))
	for _, p := range targets {
		// 每个目标都必须直接落在数据目录下：名单里混进绝对路径或 .. 就等于
		// 删数据目录之外的东西，而 reset 是不可恢复操作
		if filepath.Dir(p) != dir {
			t.Errorf("%s 不在数据目录 %s 下", p, dir)
		}
		if seen[p] {
			t.Errorf("名单里有重复项: %s", p)
		}
		seen[p] = true
	}

	// 配置必须在名单里：重置的核心就是"连令牌一起清掉"
	if !seen[filepath.Join(dir, "config.yml")] {
		t.Error("名单里没有 config.yml")
	}
	// 下载的二进制也算 flare 自己的东西：留着只是白占几十 MB
	if !seen[filepath.Join(dir, "bin")] {
		t.Error("名单里没有 bin")
	}
}

// 便携模式的 portTable 标记决定"数据放 exe 旁边还是 ~/.flare"，是配置之外的
// 关键状态：删了它，exe 旁边的数据下次就再也找不到了
func TestResetKeepsPortTableMarker(t *testing.T) {
	for _, name := range localResetEntries() {
		if strings.Contains(name, "portTable") {
			t.Fatalf("reset 名单里出现了便携标记: %s", name)
		}
	}
}

// 清理只删该删的：数据目录本身（用户用 FLARE_DIR 指定的）和便携标记都得留下
func TestClearLocalDataKeepsDirAndMarker(t *testing.T) {
	dir := t.TempDir()
	// 设了 FLARE_DIR 就等于"用户指定的目录"，这一支才不会去动真实的家目录
	t.Setenv("FLARE_DIR", dir)

	// 造出配置、二进制目录、日志和一个便携标记（虽然本环境不会是便携模式，
	// 但只要它存在，就不该被名单碰到）
	mustWrite(t, filepath.Join(dir, "config.yml"), "auth: {}")
	mustWrite(t, filepath.Join(dir, "bin", "cloudflared"), "fake")
	mustWrite(t, filepath.Join(dir, "logs", "flare.log"), "log")
	mustWrite(t, filepath.Join(dir, "portTable"), "")

	if err := clearLocalData(dir); err != nil {
		t.Fatalf("clearLocalData 出错: %v", err)
	}

	for _, gone := range []string{"config.yml", filepath.Join("bin", "cloudflared"), filepath.Join("logs", "flare.log")} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s 应该被清掉（err=%v）", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "portTable")); err != nil {
		t.Errorf("便携标记被误删: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("FLARE_DIR 指定的数据目录本身被删了: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmYes(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y", true}, {"Y", true}, {" y ", true}, {"yes", true}, {"YES", true}, {"y\r\n", true},
		{"", false}, {"n", false}, {"no", false}, {"yy", false}, {"随便", false},
	}
	for _, c := range cases {
		if got := confirmYes(c.in); got != c.want {
			t.Errorf("confirmYes(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

// reset 与 destroy 的差别就写在 wipeLocalConfig 里：destroy 保留 Auth（方便马上
// 重建隧道），reset 什么都不留（回到"从没装过"）。这条语义钉在测试里，
// 免得日后被"顺手优化"成保留令牌
func TestWipeLocalConfigClearsEverything(t *testing.T) {
	cfg := &config.Config{
		Version: 1,
		Auth:    config.AuthConfig{ApiToken: "tok", AccountID: "acct"},
		Tunnel:  config.TunnelConfig{ID: "id", Name: "name", Token: "tunnel-tok"},
		Routes: []config.RouteConfig{
			{Name: "web", Hostname: "web.example.com", Service: "http://localhost:3000", ZoneID: "z", DNSRecordID: "d"},
		},
		Relay: config.RelayConfig{
			Server: "1.2.3.4:7000",
			Token:  "relay-tok",
			Rules:  []config.RelayRule{{Name: "ssh", Proto: "tcp", LocalPort: 22}},
		},
	}
	wipeLocalConfig(cfg)

	if cfg.Auth.ApiToken != "" || cfg.Auth.AccountID != "" {
		t.Errorf("reset 后还留着认证信息: %+v", cfg.Auth)
	}
	if cfg.Tunnel.ID != "" || cfg.Tunnel.Name != "" || cfg.Tunnel.Token != "" {
		t.Errorf("reset 后还留着隧道信息: %+v", cfg.Tunnel)
	}
	if len(cfg.Routes) != 0 {
		t.Errorf("reset 后还留着路由: %+v", cfg.Routes)
	}
	if cfg.Relay.Server != "" || cfg.Relay.Token != "" || len(cfg.Relay.Rules) != 0 {
		t.Errorf("reset 后还留着中继设置: %+v", cfg.Relay)
	}
	// 版本号是"配置格式"的标记，与被清掉的内容无关，不该动它
	if cfg.Version != 1 {
		t.Errorf("配置版本被改动了: %d", cfg.Version)
	}
}
