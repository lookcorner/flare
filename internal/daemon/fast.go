package daemon

import (
	"bufio"
	"flare/internal/authproxy"
	"flare/internal/config"
	"flare/internal/download"
	"flare/internal/log"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

// 隔离配置文件
func fastConfigPath() string {
	p := filepath.Join(config.Dir(), "fast-config.yaml")
	if _, err := os.Stat(p); os.IsNotExist(err) {
		os.MkdirAll(config.Dir(), 0700)
		os.WriteFile(p, []byte("# flare fast mode - empty config\n"), 0600)
	}
	return p
}

// 嗅探输出
// scanForURL 循环读 stderr 的每一行：
//  1. 找到含 trycloudflare.com 的 URL → 回调 onURL（CLI 用法是打印出来）
//  2. 每行写进日志文件；onLine 非空时回调它（GUI 订阅日志流），
//     否则转发到 stderr（fast 是前台运行，终端会一直开着）
func scanForUrl(r io.Reader, tee io.Writer, onURL func(string), onLine func(string)) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "trycloudflare.com") {
			if url := extractURL(line); url != "" && onURL != nil {
				onURL(url)
			}
		}
		if onLine != nil {
			onLine(line)
		} else {
			fmt.Fprintln(os.Stderr, line) // 转发日志（不管是否含 URL）
		}
		if tee != nil {
			fmt.Fprintln(tee, line)
		}
	}
	// 循环结束 = 子进程退出、管道关闭。有错误（如超长行）会被 Scanner 记住：
	_ = scanner.Err()
}

// extractURL：从一整行日志里抠出 URL
// 按空白切词，找"以 http 开头且包含 trycloudflare.com"的那一个词。
func extractURL(line string) string {
	for _, part := range strings.Fields(line) {
		if strings.Contains(part, "trycloudflare.com") && strings.HasPrefix(part, "http") {
			return part
		}
	}
	return ""
}

// StartFast ：前台运行。此函数返回时 = 隧道已结束（用户按了 Ctrl+C 或进程崩了）。
func StartFast(port string) error {
	// 与 up 共用同一把锁：cloudflared 同一时刻只能有一个实例。
	// 先查再下载：已经在跑就没必要白等一次下载
	if Running() {
		return fmt.Errorf("cloudflared 已在运行，请先执行 flare down")
	}
	//每次运行先下载cloudflare,不会重复下载

	bin, err := download.Download()
	if err != nil {
		return fmt.Errorf("下载 cloudflared 失败: %w", err)
	}

	cmd := exec.Command(bin, "tunnel", "--config", fastConfigPath(), "--url", "http://localhost:"+port)
	stderr, err := cmd.StderrPipe() // ← 拿到 stderr 的读端
	if err != nil {
		return err

	}
	cmd.Stdout = os.Stdout // stdout 原样直通终端（很少内容）
	// 日志文件：前台跑着时你看着终端，但事后想查"刚才报了什么"就得靠它
	logFile, err := log.Open("cloudflared")
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer logFile.Close()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 cloudflared 失败: %w", err)
	}
	//管道必须被读：goroutine 专职嗅探（5.2）
	go scanForUrl(stderr, logFile, printFastURL, nil)
	//优雅退出三件套：监听信号 → 等进程退出 → 谁先到处理谁
	quick("cloudflared", cmd)
	return nil
}

// StartFastWithAuth：与 StartFast 几乎一样，只在中间多插一个登录网关
func StartFastWithAuth(port, username, pwd string) error {
	if Running() {
		return fmt.Errorf("cloudflared 已在运行，请先执行 flare down")
	}
	bin, err := download.Download()
	if err != nil {
		return err
	}
	//启动网关：监听 127.0.0.1:系统分配端口，转发到真实端口
	proxy, err := authproxy.New(authproxy.Config{
		Username:   username,
		Password:   pwd,
		TargetPort: port,
		SigningKey: authproxy.RandomKey(),
		CookieTTL:  24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("启动鉴权代理失败: %w", err)
	}

	if err := proxy.Start(); err != nil {
		return err
	}
	defer proxy.Stop()
	proxyPort := fmt.Sprintf("%d", proxy.ListenPort())
	fmt.Printf("鉴权网关已启动 127.0.0.1:%s → 127.0.0.1:%s\n", proxyPort, port)

	//cloudflared 的 --url 指向网关端口，而不是真实服务
	cmd := exec.Command(bin, "tunnel", "--config", fastConfigPath(),
		"--url", "http://localhost:"+proxyPort)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = os.Stdout
	logFile, err := log.Open("cloudflared")
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer logFile.Close()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 cloudflared 失败: %w", err)
	}
	go scanForUrl(stderr, logFile, printFastURL, nil)

	// 退出
	err = quick("cloudflared", cmd)
	if err != nil {
		return err
	}
	return nil
}

// printFastURL：CLI 版 fast 拿到临时域名时的报喜输出
func printFastURL(url string) {
	fmt.Printf("\n✔ 隧道已启动: %s\n\n", url)
}

// quick：前台等进程结束（Ctrl+C 或它自己退出）。
// name 只用于错误文案：这个函数同时给 cloudflared（fast）和 frpc（fast --relay）用，
// 把两者都写成 cloudflared 会让人以为是隧道出了问题
func quick(name string, cmd *exec.Cmd) error {
	sig := make(chan os.Signal, 1)   //信号通知通道,缓冲 1
	signal.Notify(sig, os.Interrupt) // Ctrl+C 转为通道消息。告诉系统:这些信号往 sig 里塞 主程序在这等 Ctrl+C
	done := make(chan error, 1)      // 进程退出通知通道
	go func() {
		// 阻塞……子进程结束 → 得到结果(如 nil 或 "exit status 1")
		// done <- result       // ② 把结果递进 done,告诉外面"我等到结果了"
		done <- cmd.Wait() //Wait：阻塞到子进程结束才返回
	}()
	// select 阻塞等待两个通道之一：用户按 Ctrl+C，还是进程自己死了？
	select {
	case <-sig: //用户要退出：
		processInterrupt(cmd.Process.Pid) //先让子进程收工
		<-done                            //再等它真正退出（清理完毕）
	case err := <-done: //进程自己死了：如实汇报
		if err != nil {
			return fmt.Errorf("%s 异常退出: %w", name, err)
		}

	}
	return nil
}
