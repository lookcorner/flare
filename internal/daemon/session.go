package daemon

import (
	"bufio"
	"flare/internal/authproxy"
	"flare/internal/config"
	"flare/internal/download"
	"flare/internal/log"
	"flare/internal/ops"
	"flare/internal/relay"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FastSession 管理一次快速穿透会话（桌面 GUI 用）。
//
// CLI 的 StartFast / StartFastWithAuth / StartRelayQuick 都是前台阻塞式：
// 进程不死函数不返回，停止靠 Ctrl+C——桌面应用里没有 Ctrl+C，
// 所以需要"启动后拿到句柄、随时 Stop"的形态。
type FastSession struct {
	cmd      *exec.Cmd
	done     chan error // 进程退出时收到结果（先做完清理才发）
	stopOnce sync.Once
}

// Done 返回一个 channel：进程退出（自然死亡或被 Stop）时收到退出结果。
// 收到即代表清理已完成（网关已停、临时配置已删、日志文件已关）。
func (s *FastSession) Done() <-chan error { return s.done }

// Stop 先给进程发中断信号让它优雅退出，3 秒还不死就强杀。
// 进程已自然死亡时直接返回它的退出结果。
func (s *FastSession) Stop() error {
	s.stopOnce.Do(func() {
		if s.cmd.Process != nil {
			processInterrupt(s.cmd.Process.Pid)
		}
	})
	select {
	case err := <-s.done:
		return err
	case <-time.After(3 * time.Second):
		if s.cmd.Process != nil {
			processKill(s.cmd.Process.Pid)
		}
		return <-s.done
	}
}

// wait 在后台等进程退出，跑完 cleanup 再向 done 汇报。
// 清理放这里而不是 Stop 里：进程自己崩掉时（没人调 Stop）同样要收拾干净
func (s *FastSession) wait(cleanup func()) {
	err := s.cmd.Wait()
	if cleanup != nil {
		cleanup()
	}
	s.done <- err
}

// StartFastTunnelSession：cloudflare 临时域名穿透的会话版（对应 flare fast [--auth]）。
// onURL 在 cloudflared 分配到 trycloudflare.com 域名时回调一次；
// onLine 回调它的每一行输出（GUI 日志流）。两个回调都可传 nil。
func StartFastTunnelSession(port, username, password string, onURL, onLine func(string)) (*FastSession, error) {
	// 与 up 共用同一把锁：cloudflared 同一时刻只能有一个实例
	if Running() {
		return nil, fmt.Errorf("cloudflared 已在运行，请先停止")
	}
	bin, err := download.Download()
	if err != nil {
		return nil, fmt.Errorf("下载 cloudflared 失败: %w", err)
	}

	// 与 StartFastWithAuth 同一套：可选先架一层登录网关，cloudflared 指向网关端口
	var proxy *authproxy.Proxy
	target := port
	if username != "" || password != "" {
		proxy, err = authproxy.New(authproxy.Config{
			Username:   username,
			Password:   password,
			TargetPort: port,
			SigningKey: authproxy.RandomKey(),
			CookieTTL:  24 * time.Hour,
		})
		if err != nil {
			return nil, fmt.Errorf("启动鉴权代理失败: %w", err)
		}
		if err := proxy.Start(); err != nil {
			return nil, err
		}
		target = strconv.Itoa(proxy.ListenPort())
	}

	cmd := exec.Command(bin, "tunnel", "--config", fastConfigPath(), "--url", "http://localhost:"+target)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		if proxy != nil {
			proxy.Stop()
		}
		return nil, err
	}
	cmd.Stdout = io.Discard // GUI 无终端可直通；cloudflared 的 stdout 本来就没几行
	logFile, err := log.Open("cloudflared")
	if err != nil {
		if proxy != nil {
			proxy.Stop()
		}
		return nil, fmt.Errorf("打开日志文件失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		if proxy != nil {
			proxy.Stop()
		}
		return nil, fmt.Errorf("启动 cloudflared 失败: %w", err)
	}

	s := &FastSession{cmd: cmd, done: make(chan error, 1)}
	go scanForUrl(stderr, logFile, onURL, onLine)
	go s.wait(func() {
		logFile.Close()
		if proxy != nil {
			proxy.Stop()
		}
	})
	return s, nil
}

// StartFastRelaySession：中继模式快速穿透的会话版（对应 flare fast --relay）。
// 与 CLI 版的差别只在"不阻塞"：规则仍是临时一条（本地端口 = 远端端口），
// 配置仍写 frpc-quick.toml，进程退出时才删除。
// 返回值附带公网入口地址（server:port）——中继模式地址启动时就知道，不用等分配。
func StartFastRelaySession(port, proto string, onLine func(string)) (*FastSession, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	if cfg.Relay.Server == "" {
		return nil, "", fmt.Errorf("未配置中继服务器，请先在「端口中继」页配置服务器地址")
	}
	portNum, err := ops.ParsePortNumber(port)
	if err != nil {
		return nil, "", err
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	if proto != "tcp" && proto != "udp" {
		return nil, "", fmt.Errorf("协议只能是 tcp 或 udp，收到: %q", proto)
	}
	bin, err := download.Frpc()
	if err != nil {
		return nil, "", fmt.Errorf("下载 frpc 失败: %w", err)
	}

	quickCfg := config.RelayConfig{
		Server: cfg.Relay.Server,
		Token:  cfg.Relay.Token,
		Rules: []config.RelayRule{{
			Name:       "quick",
			Proto:      proto,
			LocalPort:  portNum,
			RemotePort: portNum,
		}},
	}
	tmpCfg := filepath.Join(config.Dir(), "frpc-quick.toml")
	if err := relay.GenerateFrpcConfigTo(&quickCfg, tmpCfg); err != nil {
		return nil, "", err
	}
	logFile, err := log.Open("frpc")
	if err != nil {
		os.Remove(tmpCfg)
		return nil, "", fmt.Errorf("打开日志文件失败: %w", err)
	}

	// frpc 输出同时进日志文件和 GUI 日志流：io.Pipe 一对，scanner 按行回调。
	// 没订阅也要读空——pipe 不消费会堵死子进程输出
	pr, pw := io.Pipe()
	cmd := exec.Command(bin, "-c", tmpCfg)
	w := io.MultiWriter(logFile, pw)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		logFile.Close()
		os.Remove(tmpCfg)
		return nil, "", fmt.Errorf("启动 frpc 失败: %w", err)
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			if onLine != nil {
				onLine(sc.Text())
			}
		}
	}()

	host, _, herr := net.SplitHostPort(cfg.Relay.Server)
	if herr != nil {
		host = cfg.Relay.Server
	}
	target := fmt.Sprintf("%s:%d", host, portNum)

	s := &FastSession{cmd: cmd, done: make(chan error, 1)}
	go s.wait(func() {
		pw.Close()
		pr.Close()
		logFile.Close()
		os.Remove(tmpCfg)
	})
	return s, target, nil
}
