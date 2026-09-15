package netutil

import "testing"

func TestExtractPort(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://localhost:3000", "3000"},
		{"https://localhost:8443", "8443"},
		{"http://127.0.0.1:8080", "8080"},
		{"localhost:3000", "3000"},            // 没带协议头也要能取
		{"http://localhost:3000/api", "3000"}, // 带路径
		{"tcp://10.0.0.5:22", "22"},
		{"http://localhost", ""},       // 没写端口
		{"http://localhost:abc", ""},   // 端口不是数字，不能拿它去拨
		{"http://localhost:99999", ""}, // 超出范围
		{"http_status:404", ""},        // 这不是地址
		{"", ""},
		{"  http://localhost:3000  ", "3000"}, // 前后空白
	}
	for _, c := range cases {
		if got := ExtractPort(c.in); got != c.want {
			t.Errorf("ExtractPort(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}
