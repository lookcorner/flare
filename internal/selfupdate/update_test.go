package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 这些测试都不联网：自更新一旦跑歪，后果是"用户的 flare 被换成别的东西"，
// 所以纯逻辑（版本比较、资产名、地址拼装、没配仓库时报错）必须钉死

// withRepo 临时设置发布仓库，测试结束自动恢复。
// 用函数而不是常量：repo 是包级变量，注入方式是编译期 -X
func withRepo(t *testing.T, r string) {
	t.Helper()
	old := repo
	repo = r
	t.Cleanup(func() { repo = old })
}

// 版本比较是"要不要更新"的唯一判据：判错的方向有两个 ——
// 该更新时不说（用户停在旧版本）、不该更新时瞎更新（把用户的版本降回去）
func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.3.0", -1},
		{"1.3.0", "1.2.3", 1},
		{"1.2.3", "1.2.3", 0},
		// v 前缀可有可无：GitHub 的 tag 带 v，编译期注入的通常不带
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3", "v1.2.3", 0},
		{"v1.2.3", "v1.3.0", -1},
		// 按数字比而不是按字符串比：字符串比较会认为 "1.10.0" < "1.9.0"
		{"1.10.0", "1.9.0", 1},
		{"1.2", "1.2.0", 0},
		{"2.0", "1.9.9", 1},
		// 预发布版比同版本正式版小
		{"1.2.3", "1.2.3-rc1", 1},
		{"1.2.3-rc1", "1.2.3", -1},
		{"v1.2.3-rc1", "v1.2.3-rc2", -1},
		// dev / unknown / 空 不是版本号，一律算最小
		{"dev", "v1.0.0", -1},
		{"v1.0.0", "dev", 1},
		{"dev", "dev", 0},
		{"", "v1.0.0", -1},
		{"unknown", "1.0.0", -1},
	}
	for _, c := range cases {
		if got := compareVersion(c.a, c.b); got != c.want {
			t.Errorf("compareVersion(%q, %q) = %d, 期望 %d", c.a, c.b, got, c.want)
		}
		// 反向比较必须对称，否则"有没有新版"会随参数顺序变化
		if got := compareVersion(c.b, c.a); got != -c.want {
			t.Errorf("compareVersion(%q, %q) = %d, 期望 %d", c.b, c.a, got, -c.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.2.3", "v1.2.3", false},
		{"1.2.3", "v1.3.0", true},
		{"1.2.3", "v1.2.2", false},
		// 源码直接编译（dev）时，任何正式发布都算新，否则开发者永远收不到提示
		{"dev", "v0.1.0", true},
		{"", "v1.0.0", true},
		// 没查到版本号（空串）不能当成"有新版本"，否则会去下载一个空地址
		{"1.2.3", "", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, 期望 %v", c.current, c.latest, got, c.want)
		}
	}
}

// 资产名要和发布流水线产出的文件名逐字一致：差一个字符就是 404，
// 而 404 在用户看来只是"更新失败"，很难自己查出是命名不对
func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
	}{
		{"darwin", "arm64", "flare_darwin_arm64.tar.gz"},
		{"darwin", "amd64", "flare_darwin_amd64.tar.gz"},
		{"linux", "amd64", "flare_linux_amd64.tar.gz"},
		{"linux", "arm64", "flare_linux_arm64.tar.gz"},
		{"windows", "amd64", "flare_windows_amd64.zip"},
		{"windows", "arm64", "flare_windows_arm64.zip"},
	}
	for _, c := range cases {
		got, err := assetName(c.goos, c.goarch)
		if err != nil {
			t.Errorf("assetName(%s, %s) 出错: %v", c.goos, c.goarch, err)
			continue
		}
		if got != c.want {
			t.Errorf("assetName(%s, %s) = %q, 期望 %q", c.goos, c.goarch, got, c.want)
		}
	}

	// 没有发布包的平台要明确报错，而不是拼出一个必然 404 的名字
	for _, c := range [][2]string{{"plan9", "amd64"}, {"linux", "386"}, {"windows", "386"}, {"darwin", "386"}} {
		if got, err := assetName(c[0], c[1]); err == nil {
			t.Errorf("assetName(%s, %s) = %q, 期望报错", c[0], c[1], got)
		}
	}
}

func TestBinaryEntry(t *testing.T) {
	if got := binaryEntry("windows"); got != "flare.exe" {
		t.Errorf("binaryEntry(windows) = %q, 期望 flare.exe", got)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := binaryEntry(goos); got != "flare" {
			t.Errorf("binaryEntry(%s) = %q, 期望 flare", goos, got)
		}
	}
}

func TestAssetURL(t *testing.T) {
	withRepo(t, "acme/flare")
	got := assetURL("v1.2.3", "flare_darwin_arm64.tar.gz")
	want := "https://github.com/acme/flare/releases/download/v1.2.3/flare_darwin_arm64.tar.gz"
	if got != want {
		t.Errorf("assetURL = %q, 期望 %q", got, want)
	}
}

// 没注入仓库地址时必须立刻报错并说清怎么修。
// 关键在于"不联网、不做任何破坏性动作"——连不上 GitHub 的环境里
// 也不该因为自更新而卡住或改坏本机文件
func TestNoRepoConfigured(t *testing.T) {
	withRepo(t, "")

	if _, err := LatestVersion(); err == nil {
		t.Error("repo 为空时 LatestVersion 应该报错")
	} else if !strings.Contains(err.Error(), "配置发布仓库") || !strings.Contains(err.Error(), "-ldflags") {
		t.Errorf("错误信息没有说清原因和修法: %v", err)
	}

	if err := Update("v1.2.3"); err == nil {
		t.Error("repo 为空时 Update 应该报错")
	} else if !strings.Contains(err.Error(), "配置发布仓库") || !strings.Contains(err.Error(), "-ldflags") {
		t.Errorf("错误信息没有说清原因和修法: %v", err)
	}
}

// 版本号是空串时也要拦住：那会拼出一个 releases/download//... 的地址
func TestUpdateRejectsEmptyVersion(t *testing.T) {
	withRepo(t, "acme/flare")
	if err := Update("  "); err == nil {
		t.Error("版本号为空时 Update 应该报错")
	}
}

// 替换自身是自更新里唯一会动用户文件的步骤，用两个临时文件把成功路径走一遍。
// Windows 走的是另一条分支（运行中的 exe 不能直接覆盖），这里跳过
func TestReplaceSelfSwapsBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 走 swapOnWindows 分支")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "flare")
	newBin := filepath.Join(dir, "flare-new")
	if err := os.WriteFile(exe, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBin, []byte("new"), 0755); err != nil {
		t.Fatal(err)
	}

	leftover, err := replaceSelf(exe, newBin)
	if err != nil {
		t.Fatalf("replaceSelf 出错: %v", err)
	}
	if leftover != "" {
		t.Errorf("非 Windows 平台不该有残留文件: %q", leftover)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("替换后内容 = %q, 期望 new", got)
	}
	// 新文件必须是被改名走的：留在磁盘上会让用户下次更新时以为"又下了一份"
	if _, err := os.Stat(newBin); !os.IsNotExist(err) {
		t.Errorf("临时文件应该已被改名到目标位置（err=%v）", err)
	}
}

// 失败时绝不能把旧版本弄丢：替换不成，用户的 flare 还得能跑
func TestReplaceSelfKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "flare")
	if err := os.WriteFile(exe, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(dir, "not-downloaded")
	if _, err := replaceSelf(exe, missing); err == nil {
		t.Fatal("新文件不存在时 replaceSelf 应该报错")
	} else if !strings.Contains(err.Error(), exe) {
		t.Errorf("错误信息应带上级可执行文件路径，便于用户手动处理: %v", err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("失败后旧版本被改坏了: %q", got)
	}
}
