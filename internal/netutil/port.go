// Package netutil 放"地址/端口"这类小工具。
//
// 单独成包的原因：这个函数原本在 cmd/up.go 和 internal/daemon/diagnose.go 各有一份，
// 而 cmd 里那份切错了（见下），于是同一条路由在"启动鉴权网关"和"诊断"两个场景下
// 对同一个 service 字符串得出了不同结论。一份实现 + 一份测试，才不会再有第二次。
package netutil

import (
	"net"
	"strconv"
	"strings"
)

// ExtractPort 从 service 字符串里取端口："http://localhost:3000" → "3000"。
// 取不到（没写端口、端口不是数字、超出 1~65535）时返回空串。
//
// 坑：service 里有两个冒号（协议分隔符 + host/port 分隔符），
// 所以必须先剥协议头再按 host:port 拆。直接找第一个冒号会得到 "//localhost:3000"。
func ExtractPort(service string) string {
	s := strings.TrimSpace(service)
	for _, prefix := range []string{"https://", "http://", "tcp://", "udp://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	// 带路径的写法（http://localhost:3000/api）只取 host:port 部分
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	// cloudflared 的 ingress 里有一种不是地址的 service 写法：http_status:404（兜底规则）。
	// 它的意思是"直接回一个状态码"，不是"转发到 404 端口"
	if strings.HasPrefix(s, "http_status:") {
		return ""
	}
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		return ""
	}
	// SplitHostPort 只看"冒号后面非空"，不校验内容（"http_status:404" 也能过）。
	// 这里补数字与范围校验：拿一个假端口去拨，只会凭空造出一条"未监听"
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return ""
	}
	return port
}
