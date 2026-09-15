package cmd

import (
	"reflect"
	"strings"
	"testing"
)

// 凭据绝不能进日志：日志会长期留在磁盘上，还可能被贴进 issue。
// 两种写法都要打码：--token xxx 和 --token=xxx
func TestRedactArgs(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"relay", "init", "1.2.3.4:7000", "--token", "s3cret"}, []string{"relay", "init", "1.2.3.4:7000", "--token", "******"}},
		{[]string{"relay", "init", "1.2.3.4:7000", "--token=s3cret"}, []string{"relay", "init", "1.2.3.4:7000", "--token=******"}},
		{[]string{"fast", "3000", "--auth", "alice:pw"}, []string{"fast", "3000", "--auth", "******"}},
		{[]string{"init", "--token", "t", "--account", "acct"}, []string{"init", "--token", "******", "--account", "acct"}},
		{[]string{"add", "web", "3000", "--domain", "web.example.com"}, []string{"add", "web", "3000", "--domain", "web.example.com"}},
		// 敏感旗标在末尾、后面没有值：不能越界，也不能把旗标本身吞掉
		{[]string{"up", "--token"}, []string{"up", "--token"}},
	}
	for _, c := range cases {
		got := redactArgs(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("redactArgs(%v) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

// 打码后的结果里不能残留凭据原文（这是这个函数唯一真正重要的事）
func TestRedactArgsLeaksNothing(t *testing.T) {
	secrets := []string{"tok_live_abcdef123456", "hunter2"}
	args := []string{"relay", "init", "1.2.3.4:7000", "--token", secrets[0], "fast", "3000", "--auth", "alice:" + secrets[1]}
	got := redactArgs(args)
	for _, s := range got {
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Errorf("打码后仍残留凭据 %q: %v", secret, got)
			}
		}
	}
}
