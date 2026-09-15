package relay

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"flare/internal/config"
)

// listen 起一个本地监听器，模拟"有服务在监听"，返回端口和关闭函数。
// 后台 accept 并立刻关掉连接：不 accept 的话连接会堆在 backlog 里，
// 后面几次探测就测不到"能连上"这件事
func listen(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起本地监听器失败: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // 监听器已关闭
			}
			conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { ln.Close() }
}

// closedPort 返回一个确定没人监听的端口：先占住、拿到端口号、再放开
func closedPort(t *testing.T) int {
	t.Helper()
	port, closeLn := listen(t)
	closeLn()
	return port
}

// 端口有人听 → LocalOK
func TestCheckRulePortReachable(t *testing.T) {
	port, closeLn := listen(t)
	defer closeLn()

	rc := checkRule(config.RelayRule{Name: "web", Proto: "tcp", LocalPort: port, RemotePort: 0}, "127.0.0.1:7000")
	if !rc.LocalOK {
		t.Fatalf("端口 %d 有监听，LocalOK 应为 true，实际: %+v", port, rc)
	}
	if rc.LocalErr != "" {
		t.Errorf("探测成功时不该有 LocalErr，实际: %q", rc.LocalErr)
	}
}

// 端口没人听 → LocalOK=false，且原因里要带上地址（"未监听"要能指出是哪个地址）
func TestCheckRulePortUnreachable(t *testing.T) {
	port := closedPort(t)

	rc := checkRule(config.RelayRule{Name: "db", Proto: "tcp", LocalPort: port}, "127.0.0.1:7000")
	if rc.LocalOK {
		t.Fatalf("端口 %d 没人监听，LocalOK 应为 false", port)
	}
	if !strings.Contains(rc.LocalErr, strconv.Itoa(port)) {
		t.Errorf("LocalErr 应指出端口 %d，实际: %q", port, rc.LocalErr)
	}
}

// 自定义 LocalIP 也要生效（--ip 192.168.x.x 这类规则不能悄悄测到回环上去）
func TestCheckRuleCustomLocalIP(t *testing.T) {
	port, closeLn := listen(t)
	defer closeLn()

	rc := checkRule(config.RelayRule{Name: "web", Proto: "tcp", LocalIP: "127.0.0.1", LocalPort: port}, "127.0.0.1:7000")
	if !rc.LocalOK {
		t.Fatalf("LocalIP=127.0.0.1 且端口 %d 有监听，LocalOK 应为 true，实际: %+v", port, rc)
	}
}

// 远端穿透端口用的是同一套判定：服务器上的端口开着 = RemoteOK
func TestCheckRuleRemotePort(t *testing.T) {
	remotePort, closeLn := listen(t)
	defer closeLn()

	rc := checkRule(config.RelayRule{Name: "ssh", Proto: "tcp", LocalPort: closedPort(t), RemotePort: remotePort}, "127.0.0.1:7000")
	if !rc.RemoteOK {
		t.Errorf("服务器 %d 端口有监听，RemoteOK 应为 true，实际: %+v", remotePort, rc)
	}

	dead := closedPort(t)
	rc = checkRule(config.RelayRule{Name: "ssh", Proto: "tcp", LocalPort: closedPort(t), RemotePort: dead}, "127.0.0.1:7000")
	if rc.RemoteOK {
		t.Errorf("远端端口 %d 没人监听，RemoteOK 应为 false", dead)
	}
	if rc.RemoteErr == "" {
		t.Error("远端不通时 RemoteErr 应说明原因，实际为空")
	}
}

// RemotePort=0（由 frps 分配）没有可探测的目标：不算通，也不算断
func TestCheckRuleRemotePortAutoAssigned(t *testing.T) {
	port, closeLn := listen(t)
	defer closeLn()

	rc := checkRule(config.RelayRule{Name: "web", Proto: "tcp", LocalPort: port, RemotePort: 0}, "127.0.0.1:7000")
	if !rc.LocalOK {
		t.Fatalf("LocalOK 应为 true，实际: %+v", rc)
	}
	if !rc.RemoteOK && rc.RemoteErr != "" {
		t.Errorf("RemotePort=0 时不该报远端错误，实际: %q", rc.RemoteErr)
	}
}

// udp 规则不做 TCP 探测：底下的 tcp 监听器在不在都不该影响结论
func TestCheckRuleUDPNotProbed(t *testing.T) {
	port, closeLn := listen(t)
	defer closeLn()

	rc := checkRule(config.RelayRule{Name: "game", Proto: "udp", LocalPort: port, RemotePort: 19132}, "127.0.0.1:7000")
	if !rc.Skipped {
		t.Errorf("udp 规则应标为未探测，实际: %+v", rc)
	}
	if rc.LocalOK || rc.RemoteOK {
		t.Errorf("udp 规则不该给出通/断结论，实际: %+v", rc)
	}
}

// Check：服务器状态、frpc 状态、规则筛选和统计口径
func TestCheckStatsAndFilter(t *testing.T) {
	serverPort, closeServer := listen(t)
	defer closeServer()
	svcPort, closeSvc := listen(t)
	defer closeSvc()

	cfg := &config.RelayConfig{
		Server: "127.0.0.1:" + strconv.Itoa(serverPort),
		Rules: []config.RelayRule{
			{Name: "web", Proto: "tcp", LocalPort: svcPort},               // 通
			{Name: "db", Proto: "tcp", LocalPort: closedPort(t)},          // 断（本地服务没起）
			{Name: "game", Proto: "udp", LocalPort: 19132, RemotePort: 1}, // 跳过
		},
	}

	got := Check(cfg, "", FrpcState{Running: true, PID: 4242})
	if !got.ServerOK {
		t.Errorf("服务器端口有监听，ServerOK 应为 true，实际: %+v", got)
	}
	if got.Total != 3 || got.Passed != 1 || got.Failed != 1 || got.Skipped != 1 {
		t.Errorf("统计 = 总 %d / 通 %d / 断 %d / 跳过 %d，期望 3/1/1/1",
			got.Total, got.Passed, got.Failed, got.Skipped)
	}
	if !got.FrpcRunning || got.FrpcPID != 4242 {
		t.Errorf("frpc 状态应原样带进结果，实际运行=%v PID=%d", got.FrpcRunning, got.FrpcPID)
	}

	// 指定规则名：只测这一条
	one := Check(cfg, "web", FrpcState{})
	if one.Total != 1 || one.Passed != 1 || len(one.Rules) != 1 || one.Rules[0].Name != "web" {
		t.Errorf("按名字筛选后应只剩 web 一条，实际: %+v", one.Rules)
	}
	if one.FrpcRunning {
		t.Error("FrpcState 传什么就是什么，不该自己探测")
	}

	// 名字对不上：Check 返回空列表（提示由 cmd 层负责，那里能说得更具体）
	none := Check(cfg, "nope", FrpcState{})
	if none.Total != 0 || len(none.Rules) != 0 {
		t.Errorf("规则名不存在时不该有结果，实际: %+v", none.Rules)
	}
}

// 没配服务器时不做探测，也不能把 Server 字段丢掉（报错文案要用它）
func TestCheckWithoutServer(t *testing.T) {
	got := Check(&config.RelayConfig{}, "", FrpcState{})
	if got.Server != "" || got.ServerOK {
		t.Errorf("未配服务器时不该报可达，实际: %+v", got)
	}
	if got.Total != 0 {
		t.Errorf("没有规则时 Total 应为 0，实际: %d", got.Total)
	}
}
