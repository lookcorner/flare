package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDir(t *testing.T) {
	got := Dir()
	t.Logf("查看got 返回的值：%s", got)
	exe, err := os.Executable()
	t.Logf("查看调用os.Executable返回的值: %s", exe)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Dir(exe)
	t.Logf("查看filepath的值 %s", want)
	if got == "" {
		t.Errorf("Dir() = %q, want non-empty", got)
	}

	t.Logf("Dir() = %q (exe = %q)", got, want) // 用 t.Logf 看值,不用 fmt.Printf
}

func TestPath(t *testing.T) {
	got := Path()
	t.Logf("查看path返回的是什么值：%s", got)

}
