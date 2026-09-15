package ops

import "testing"

// 路由名的默认值来自域名，取错会让用户看到一条自己没起过名字的路由。
// 只取第一段：chat.example.com → chat
func TestRouteNameFromDomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"chat.example.com", "chat"},
		{"example.com", "example"},
		{"a.b.c.d", "a"},
		{"  web.example.com  ", "web"},
		// 没有点的输入（内网名、手滑）原样返回，总比返回空串让后面报错强
		{"localhost", "localhost"},
		{"", ""},
	}
	for _, c := range cases {
		if got := RouteNameFromDomain(c.in); got != c.want {
			t.Errorf("RouteNameFromDomain(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// 端口校验是唯一能挡住"手滑"的地方：越界的端口要等 cloudflared
// 或 frpc 起来才报错，那时前面的问题都白问了
func TestParsePortNumber(t *testing.T) {
	ok := []struct {
		in   string
		want int
	}{
		{"8080", 8080},
		{" 3000 ", 3000},
		{"1", 1},
		{"65535", 65535},
	}
	for _, c := range ok {
		got, err := ParsePortNumber(c.in)
		if err != nil {
			t.Errorf("ParsePortNumber(%q) 出错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePortNumber(%q) = %d, 期望 %d", c.in, got, c.want)
		}
	}

	for _, in := range []string{"", "0", "65536", "-1", "abc", "80.5", "80 80"} {
		if got, err := ParsePortNumber(in); err == nil {
			t.Errorf("ParsePortNumber(%q) = %d, 期望报错", in, got)
		}
	}
}

// 中继服务器地址必须带端口：frp 把 serverAddr 和 serverPort 当两个字段，
// 少了端口 frpc 起不来，而报错信息很难看出是地址写错了
func TestCheckRelayAddr(t *testing.T) {
	for _, in := range []string{"1.2.3.4:7000", "example.com:7000", " 1.2.3.4:7000 ", "[::1]:7000"} {
		if err := CheckRelayAddr(in); err != nil {
			t.Errorf("CheckRelayAddr(%q) 出错: %v", in, err)
		}
	}
	for _, in := range []string{"", "1.2.3.4", ":7000", "1.2.3.4:0", "1.2.3.4:70000", "1.2.3.4:abc"} {
		if err := CheckRelayAddr(in); err == nil {
			t.Errorf("CheckRelayAddr(%q) 应该报错", in)
		}
	}
}

// auth 串必须能切成"用户名:密码"两半，任一为空都要拦住
func TestParseAuth(t *testing.T) {
	user, pwd, err := ParseAuth("alice:s3cret")
	if err != nil || user != "alice" || pwd != "s3cret" {
		t.Errorf("ParseAuth 正常输入解析失败: %q %q %v", user, pwd, err)
	}
	// 密码里允许再出现冒号（按第一个冒号切）
	if _, pwd, err := ParseAuth("u:p:a:ss"); err != nil || pwd != "p:a:ss" {
		t.Errorf("ParseAuth 带冒号密码解析失败: %q %v", pwd, err)
	}
	for _, in := range []string{"", "nocolon", ":pwd", "user:", ":"} {
		if _, _, err := ParseAuth(in); err == nil {
			t.Errorf("ParseAuth(%q) 应该报错", in)
		}
	}
}
