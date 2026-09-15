package download

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 这些测试都不联网：手工造出与 GitHub 发布包结构相同的压缩包，
// 验证 install 能从里面取出可执行文件、并且失败时不留半截文件。

// makeTgz 造一个 tar.gz：内部条目的路径形如 frp_0.71.0_linux_amd64/frpc（带一层目录，贴近真实发布包）
func makeTgz(t *testing.T, entry, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte(content)
	if err := tw.WriteHeader(&tar.Header{
		Name: dirNameFor(entry) + "/" + entry,
		Mode: 0755,
		Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeZip 造一个 zip（Windows 发布包的结构）
func makeZip(t *testing.T, entry, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(dirNameFor(entry) + "/" + entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dirNameFor(entry string) string { return "frp_0.71.0_test" }

// errReader 模拟"下到一半断了"
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset by peer") }

func TestInstallTgz(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "bin", "frpc")
	if err := InstallArchive(bytes.NewReader(makeTgz(t, "frpc", "fake-frpc")), dest, "frp_0.71.0_linux_amd64.tar.gz", "frpc"); err != nil {
		t.Fatalf("install(tgz) 出错: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake-frpc" {
		t.Errorf("内容 = %q, 期望 %q", got, "fake-frpc")
	}
	// 必须是可执行的，否则 frpc 拉不起来
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Errorf("权限 = %v, 缺少执行位", info.Mode())
	}
}

func TestInstallZip(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "bin", "frpc.exe")
	if err := InstallArchive(bytes.NewReader(makeZip(t, "frpc.exe", "fake-frpc-exe")), dest, "frp_0.71.0_windows_amd64.zip", "frpc.exe"); err != nil {
		t.Fatalf("install(zip) 出错: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake-frpc-exe" {
		t.Errorf("内容 = %q, 期望 %q", got, "fake-frpc-exe")
	}
}

func TestInstallPlainBinary(t *testing.T) {
	// cloudflared 在 Linux 上是裸二进制，没有压缩包
	dest := filepath.Join(t.TempDir(), "bin", "cloudflared")
	if err := InstallArchive(bytes.NewReader([]byte("fake-cloudflared")), dest, "cloudflared-linux-amd64", "cloudflared"); err != nil {
		t.Fatalf("install(裸二进制) 出错: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fake-cloudflared" {
		t.Errorf("内容 = %q, 期望 %q", got, "fake-cloudflared")
	}
}

// 下到一半失败：dest 绝不能出现文件（否则下次运行会把它当成"已装好"）
func TestInstallFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bin", "frpc")
	if err := InstallArchive(errReader{}, dest, "frp_0.71.0_linux_amd64.tar.gz", "frpc"); err == nil {
		t.Fatal("期望出错，实际成功")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest 不应存在，Stat 结果: %v", err)
	}
	// 临时文件也要清干净，别在 bin 目录里留垃圾
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("bin 目录残留文件: %s", e.Name())
	}
}

// 压缩包里没有目标文件 → 报错，且不留文件
func TestInstallTgzMissingEntry(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "bin", "frpc")
	err := InstallArchive(bytes.NewReader(makeTgz(t, "frps", "x")), dest, "frp_0.71.0_linux_amd64.tar.gz", "frpc")
	if err == nil {
		t.Fatal("期望报错，实际成功")
	}
}
