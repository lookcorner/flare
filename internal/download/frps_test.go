package download

import (
	"flare/internal/config"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupFlareDir 把数据目录指到测试临时目录。
// config.Dir() 用 sync.Once 缓存结果，同进程只能设置一次——
// 万一将来有别的测试先读过目录，这里跳过而不是报错
func setupFlareDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FLARE_DIR", dir)
	if got := config.Dir(); got != dir {
		t.Skipf("数据目录已被缓存为 %s，本测试无法重置", got)
	}
	return dir
}

// frps 和 frpc 共用同一个发布包，但取出的 entry 不同。Windows 上要带 .exe
func TestFrpsEntry(t *testing.T) {
	want := "frps"
	if runtime.GOOS == "windows" {
		want = "frps.exe"
	}
	if got := frpsEntry(); got != want {
		t.Errorf("frpsEntry() = %q, 期望 %q", got, want)
	}
}

// 资产包名必须是 平台+架构+版本 拼出来的那个，否则下载地址就是错的
func TestFrpsAssetName(t *testing.T) {
	name, err := frpsName()
	if err != nil {
		t.Skipf("当前平台不支持 frps: %v", err)
	}
	prefix := fmt.Sprintf("frp_%s_%s_%s", frpVersion, runtime.GOOS, runtime.GOARCH)
	if !strings.HasPrefix(name, prefix) {
		t.Errorf("资产名 %q 应以 %q 开头", name, prefix)
	}
	suffix := ".tar.gz"
	if runtime.GOOS == "windows" {
		suffix = ".zip"
	}
	if !strings.HasSuffix(name, suffix) {
		t.Errorf("资产名 %q 应以 %q 结尾", name, suffix)
	}
	// frps 与 frpc 是同一个包：两边拼出来的文件名必须一致，
	// 不一致就说明有人只改了一处平台列表
	frpc, frpcErr := frpcName()
	if frpcErr != nil {
		t.Fatalf("frpcName() 出错: %v", frpcErr)
	}
	if frpc != name {
		t.Errorf("frps 与 frpc 的资产包名不一致: %q vs %q", name, frpc)
	}
}

// 已装好就直接返回路径，不能再走网络（这条在离线环境里也必须是绿的）
func TestFrpsReusesExistingFile(t *testing.T) {
	dir := setupFlareDir(t)
	dest := FrpsPath()
	want := filepath.Join(dir, "bin", frpsEntry())
	if dest != want {
		t.Fatalf("FrpsPath() = %q, 期望 %q", dest, want)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("fake-frps"), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := Frps()
	if err != nil {
		t.Fatalf("frps 已存在时不该再下载: %v", err)
	}
	if got != dest {
		t.Errorf("Frps() = %q, 期望 %q", got, dest)
	}
}

// 版本号只能有一个出处：安装脚本与本地下载都用 FrpVersion()
func TestFrpVersionExported(t *testing.T) {
	if FrpVersion() != frpVersion {
		t.Errorf("FrpVersion() = %q, 期望 %q", FrpVersion(), frpVersion)
	}
}
