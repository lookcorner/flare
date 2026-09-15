package log

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// config.Dir() 用 sync.Once 缓存，必须在本包第一次调用它之前设好 FLARE_DIR。
// log 包目前只有这一个测试文件，顺序有保证
func setupTemp(t *testing.T) {
	t.Helper()
	t.Setenv("FLARE_DIR", t.TempDir())
}

// 日志要同时写到终端和文件，且带时间戳+级别（不然没法回答"什么时候出的问题"）
func TestWritesToTerminalAndFile(t *testing.T) {
	setupTemp(t)
	var term bytes.Buffer
	SetOutput(&term, LevelDebug)
	t.Cleanup(func() { SetOutput(os.Stderr, LevelInfo) })

	if err := Setup("flare"); err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	Infof("隧道已启动 %s", "PID 123")
	Close()

	if !strings.Contains(term.String(), "隧道已启动 PID 123") {
		t.Errorf("终端缺少日志内容: %q", term.String())
	}
	data, err := os.ReadFile(Path("flare"))
	if err != nil {
		t.Fatalf("读日志文件失败: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "隧道已启动 PID 123") {
		t.Errorf("日志文件缺少内容:\n%s", got)
	}
	if !strings.Contains(got, "[INFO ]") {
		t.Errorf("日志应带级别标记，实际:\n%s", got)
	}
	// 时间戳：形如 2026-09-14 01:32:14.455
	if len(got) < 23 || got[4] != '-' || got[13] != ':' {
		t.Errorf("日志应带时间戳，实际首行: %q", firstLine(got))
	}
}

// 级别过滤：低于门槛的日志直接丢，别把 INFO 级别塞满
func TestLevelFiltering(t *testing.T) {
	setupTemp(t)
	var term bytes.Buffer
	SetOutput(&term, LevelWarn)
	t.Cleanup(func() { SetOutput(os.Stderr, LevelInfo) })

	Setup("flare")
	Debugf("调试信息")
	Infof("普通信息")
	Warnf("警告信息")
	Errorf("错误信息")
	Close()

	got := term.String()
	if strings.Contains(got, "调试信息") || strings.Contains(got, "普通信息") {
		t.Errorf("低级别日志不该输出:\n%s", got)
	}
	if !strings.Contains(got, "警告信息") || !strings.Contains(got, "错误信息") {
		t.Errorf("高级别日志应该输出:\n%s", got)
	}
}

// 日志是追加而不是覆盖：重启一次就把上次的记录冲掉，等于没有历史
func TestAppendsAcrossRuns(t *testing.T) {
	setupTemp(t)
	var term bytes.Buffer
	SetOutput(&term, LevelInfo)
	t.Cleanup(func() { SetOutput(os.Stderr, LevelInfo) })

	Setup("flare")
	Infof("第一次运行")
	Close()
	Setup("flare")
	Infof("第二次运行")
	Close()

	lines, err := Tail("flare", 0)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "第一次运行") || !strings.Contains(joined, "第二次运行") {
		t.Errorf("两次运行的日志都该在文件里:\n%s", joined)
	}
}

// Tail 只取最后 n 行
func TestTailKeepsLastLines(t *testing.T) {
	setupTemp(t)
	var term bytes.Buffer
	SetOutput(&term, LevelInfo)
	t.Cleanup(func() { SetOutput(os.Stderr, LevelInfo) })

	Setup("flare")
	for _, s := range []string{"第一行", "第二行", "第三行", "第四行"} {
		Infof("%s", s)
	}
	Close()

	lines, err := Tail("flare", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("应取 2 行，实际 %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "第三行") || !strings.Contains(lines[1], "第四行") {
		t.Errorf("最后两行应是第三/四行，实际: %v", lines)
	}
}

// 文件不存在时 Tail 报错而不是 panic：日志还没产生是正常情况
func TestTailMissingFile(t *testing.T) {
	setupTemp(t)
	if _, err := Tail("frpc", 10); err == nil {
		t.Error("文件不存在时应返回错误")
	}
}

// Follow：先补上已有内容，再跟着新写入的内容走
func TestFollowStreamsAppendedLines(t *testing.T) {
	setupTemp(t)
	SetOutput(&bytes.Buffer{}, LevelInfo)
	t.Cleanup(func() { SetOutput(os.Stderr, LevelInfo) })

	Setup("flare")
	Infof("已有的一行")
	Close()

	var out bytes.Buffer
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		Follow("flare", &out, stop)
		close(done)
	}()

	// 给 Follow 一点时间读到已有内容，然后追加新内容
	waitFor(t, &out, "已有的一行")
	f, err := Open("flare")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("新追加的一行\n")
	f.Close()
	waitFor(t, &out, "新追加的一行")

	close(stop)
	<-done
}

// waitFor：轮询等待 out 里出现 want（超时 3 秒即判定失败）
func waitFor(t *testing.T, out *bytes.Buffer, want string) {
	t.Helper()
	for i := 0; i < 60; i++ { // 60 * 50ms = 3s
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("等待 %q 超时，实际内容: %q", want, out.String())
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
