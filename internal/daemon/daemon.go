package daemon

import (
	"flare/internal/config"
	"flare/internal/download"
	"flare/internal/log"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// pidFilePath 获取cloudflared pid的路径
func pidFilePath() string {
	return pidFile("cloudflared.pid")
}

// pidFile 拼出数据目录下某个 PID 文件的路径。
// 每个后台进程一个文件（cloudflared 一个、frpc 一个），互不干扰、可同时运行
func pidFile(name string) string {
	return filepath.Join(config.Dir(), name)
}

// writePid 写 cloudflared 的 PID 文件
func writePid(pid int) error {
	return writePidFile("cloudflared.pid", pid)
}

// writePidFile 写 PID 文件。0600 权限：防其他用户读到 PID 乱杀进程
func writePidFile(name string, pid int) error {
	// 1. 确保数据目录存在（比如 ~/.flare）
	//    MkdirAll = 逐级建目录，已存在不报错。0700 = 只有自己可读写进入
	err := os.MkdirAll(config.Dir(), 0700)
	if err != nil {
		return err
	}
	// 2. 核心：往 ~/.flare/cloudflared.pid 写入内容
	//    0600 = 只有文件属主可读写，防止别的用户看到 PID 乱杀进程
	err = os.WriteFile(pidFile(name), []byte(strconv.Itoa(pid)), 0600)
	if err != nil {
		return err
	}
	return nil
}

// readPid 读 cloudflared 的 PID
func readPid() (int, error) {
	return readPidFile("cloudflared.pid")
}

// readPidFile 读文件并转 int。文件不存在/内容坏 返回错误
func readPidFile(name string) (int, error) {
	data, err := os.ReadFile(pidFile(name))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// Running：判断"是否在运行" = PID 文件在 + 进程真活着。两步缺一即假。
func Running() bool {
	return procAlive("cloudflared.pid")
}

// procAlive 是 Running 的通用版：所有"靠 PID 文件记账"的后台进程都用它判断
func procAlive(name string) bool {
	pid, err := readPidFile(name)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("Running Error :%s", err)
		}
		return false
	}
	return processRunning(pid)
}

// PID：当前运行 PID（没用时返回 0）
func Pid() int {
	pid, _ := readPid()
	return pid
}

// startupGrace：后台进程"算启动成功"的观察窗口。
// 最常见的失败（token 不对、服务器地址不对）都会在启动瞬间退出，
// 等一小会儿再看它还在不在，比直接报一句"已启动"诚实的多。
const startupGrace = time.Second

// waitStartup 等 startupGrace，返回（进程是否还活着, 退出时的错误）。
//
// 为什么用 cmd.Wait() 而不是 processRunning(pid)：没人 Wait 的子进程会变成"僵尸"——
// 进程表里还占着条目、其实已经结束了，kill(pid, 0) 对僵尸照样返回成功，
// 用它判断会把"启动即失败"误报成"已启动"。Wait 才是真的等它结束。
func waitStartup(cmd *exec.Cmd) (bool, error) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return false, err
	case <-time.After(startupGrace):
		return true, nil
	}
}

// Start：后台启动 cloudflared（token 模式）。
// token 模式下 cloudflared 自己去云端拉配置，本地不需要任何配置文件。
func Start(token string) error {
	bin, err := download.Download()
	if err != nil {
		return err
	}
	if Running() {
		return fmt.Errorf("cloudflared 已在运行") // 防重复启动：先问 PID 文件
	}
	// 后台进程的输出交给日志文件：父进程一退出，终端就没人接收了，
	// 而"昨晚隧道为什么断了"这类问题只能靠日志回答
	logFile, err := log.Open("cloudflared")
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer logFile.Close() // 子进程已经拿到自己那份 fd，这里关掉不影响它

	cmd := exec.Command(bin, "tunnel", "--protocol", "http2", "run", "--token", token)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	log.Debugf("启动 cloudflared: %s", bin)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 cloudflared 失败: %w", err)
	}
	// 先落盘 PID（后续 down 靠它找进程），再确认它没秒退
	if err := writePid(cmd.Process.Pid); err != nil {
		return fmt.Errorf("写 PID 文件失败: %w", err)
	}
	if alive, waitErr := waitStartup(cmd); !alive {
		os.Remove(pidFilePath())
		log.Errorf("cloudflared 启动后立即退出: %v", waitErr)
		log.PrintTail("cloudflared", 20) // 原因就在日志里，直接摆给用户看
		if waitErr != nil {
			return fmt.Errorf("cloudflared 启动后立即退出（%v），详情见 %s", waitErr, log.Path("cloudflared"))
		}
		return fmt.Errorf("cloudflared 启动后立即退出，详情见 %s", log.Path("cloudflared"))
	}
	log.Infof("cloudflared 已启动 (PID: %d)", cmd.Process.Pid)
	fmt.Printf("日志: %s（flare log cloudflared -f 可实时查看）\n", log.Path("cloudflared"))
	return nil

}

// Stop：优雅停止。读 PID → kill → 删 PID 文件。
func Stop() error {
	pid, err := readPid()
	if err != nil {
		return fmt.Errorf("Stop cloudflared Error 失败：%w", err)

	}
	// 进程自己先崩了的情形：只剩一个过期的 PID 文件。
	// 不清掉它的话，之后每次 stop 都会对着空气发信号、永远报错
	if !processRunning(pid) {
		os.Remove(pidFilePath())
		return fmt.Errorf("cloudflared 已不在运行（PID %d 已消失），已清理 PID 文件", pid)
	}

	if err := processKill(pid); err != nil {
		return fmt.Errorf("停止 cloudflared 失败: %w", err)
	}
	//移除掉停止的pid文件 = 释放"正在运行"的声明
	os.Remove(pidFilePath())
	// 用日志输出，而不是再 fmt.Println 一句：带时间戳的记录只在日志里出现一次
	log.Infof("cloudflared 已停止 (PID: %d)", pid)
	return nil
}
