// Package log 是 flare 自己的日志：一份写终端，一份落文件。
//
// 为什么不能只用 fmt.Println：
//  1. up / relay up 是"后台启动"，父进程一退出，子进程的 stdout/stderr 就没了接收方。
//     要事后回答"昨晚隧道为什么断了"，必须把子进程输出也落到文件里。
//  2. 排查问题需要时间戳和级别：谁在什么时候干了什么。
//
// 日志目录：<数据目录>/logs/，三个文件各管一段（flare 自己的操作、cloudflared、frpc）。
package log

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flare/internal/config"
)

// 级别：数字越大越重要。低于当前级别的日志直接丢掉
const (
	LevelDebug = iota
	LevelInfo
	LevelWarn
	LevelError
)

func levelName(l int) string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	}
	return "?"
}

// ParseLevel：把 FLARE_LOG_LEVEL 的值（debug/info/warn/error）转成级别数字
func ParseLevel(s string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, true
	case "info":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error":
		return LevelError, true
	}
	return LevelInfo, false
}

// Dir 日志目录（默认 ~/.flare/logs；便携模式下就在 exe 旁边）
func Dir() string { return filepath.Join(config.Dir(), "logs") }

// Path 某个组件的日志文件路径：flare / cloudflared / frpc
func Path(name string) string { return filepath.Join(Dir(), name+".log") }

// Open 打开（不存在就创建）组件的日志文件，追加写。
// 子进程的 stdout/stderr 直接指到这个文件。权限 0600：
// 日志里会出现域名、隧道 ID、请求错误等，不该让同机器的其他用户读
func Open(name string) (*os.File, error) {
	if err := os.MkdirAll(Dir(), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(Path(name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}

// Logger：终端 + 文件双写。mu 串行化写入，
// 否则多个 goroutine（比如 fast 模式嗅探 URL 的那个）同时写会串行错乱
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	file  *os.File
	level int
}

var std = &Logger{out: os.Stderr, level: LevelInfo}

// Setup 打开数据目录下的日志文件并接上标准记录器。
// 失败不致命：日志写不了不该拦住任何命令，退化成"只写终端"即可。
// 日志级别由环境变量 FLARE_LOG_LEVEL 控制（debug/info/warn/error）
func Setup(name string) error {
	f, err := Open(name)
	std.mu.Lock()
	defer std.mu.Unlock()
	std.file = f
	if lv, ok := ParseLevel(os.Getenv("FLARE_LOG_LEVEL")); ok {
		std.level = lv
	}
	return err
}

// Close 关闭文件句柄（进程退出前调用，保证最后几行落盘）
func Close() {
	std.mu.Lock()
	defer std.mu.Unlock()
	if std.file != nil {
		std.file.Close()
		std.file = nil
	}
}

// SetOutput 换终端输出目标（测试用）
func SetOutput(w io.Writer, level int) {
	std.mu.Lock()
	defer std.mu.Unlock()
	std.out = w
	std.level = level
}

// formatLine 拼一行带时间戳和级别的日志
func formatLine(level int, format string, args ...any) string {
	return fmt.Sprintf("%s [%-5s] %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), levelName(level), fmt.Sprintf(format, args...))
}

func logf(level int, format string, args ...any) {
	std.mu.Lock()
	defer std.mu.Unlock()
	if level < std.level {
		return
	}
	// 固定宽度的级别名 + 毫秒时间戳，方便肉眼对齐、也方便 grep
	line := formatLine(level, format, args...)
	if std.out != nil {
		io.WriteString(std.out, line)
	}
	if std.file != nil {
		std.file.WriteString(line)
	}
}

func Debugf(format string, args ...any) { logf(LevelDebug, format, args...) }
func Infof(format string, args ...any)  { logf(LevelInfo, format, args...) }
func Warnf(format string, args ...any)  { logf(LevelWarn, format, args...) }
func Errorf(format string, args ...any) { logf(LevelError, format, args...) }

// Auditf 只写文件、不写终端：用于"记一笔档案"类的日志（比如每次执行了哪条命令）。
// 终端里重复显示用户刚敲过的命令没有价值，但日志文件里需要它
func Auditf(format string, args ...any) {
	std.mu.Lock()
	defer std.mu.Unlock()
	if std.file == nil {
		return
	}
	std.file.WriteString(formatLine(LevelInfo, format, args...))
}

// Tail 取某个组件日志的最后 n 行（n <= 0 表示不截断）。
// 只读文件末尾 64KB：日志可能有几十兆，没必要整份读进内存
func Tail(name string, n int) ([]string, error) {
	f, err := os.Open(Path(name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const chunk = 64 * 1024
	size, start := info.Size(), int64(0)
	if size > chunk {
		start = size - chunk
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	// 从中间切进来时第一行多半是半行，丢掉
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// PrintTail 把日志末尾几行打到终端。
// 后台进程启动失败时原因就在它自己的日志里，直接摆出来，别让用户再敲一次命令去查
func PrintTail(name string, n int) {
	lines, err := Tail(name, n)
	if err != nil || len(lines) == 0 {
		return
	}
	std.mu.Lock()
	defer std.mu.Unlock()
	if std.out == nil {
		return
	}
	fmt.Fprintf(std.out, "--- %s 日志末尾 ---\n", name)
	for _, l := range lines {
		fmt.Fprintln(std.out, l)
	}
	fmt.Fprintf(std.out, "--- 完整日志: %s ---\n", Path(name))
}

// Follow 盯着日志文件，把新增内容写到 w，直到 stop 被关闭（等价 tail -f）。
// 用轮询而不是 inotify：三个平台一套代码，日志这点延迟无所谓
func Follow(name string, w io.Writer, stop <-chan struct{}) error {
	var offset int64
	for {
		if f, err := os.Open(Path(name)); err == nil {
			if info, err := f.Stat(); err == nil {
				// 文件被清空或重建时 size 会变小，此时得从头读
				if info.Size() < offset {
					offset = 0
				}
				if info.Size() > offset {
					if _, err := f.Seek(offset, io.SeekStart); err == nil {
						n, _ := io.Copy(w, f) // 普通文件读到末尾会立刻返回 EOF，不会卡住
						offset += n
					}
				}
			}
			f.Close()
		}
		select {
		case <-stop:
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}
