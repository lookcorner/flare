package authproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 这一套测试覆盖的是 flare 的 --auth 密码保护网关：
// 没有 Cookie → 登录页；密码错 → 打回；密码对 → 发签名 Cookie；带对的 Cookie → 真正转发到本地服务。
// 全程走 127.0.0.1，不需要外网。

// startTarget 起一个"被保护的真实服务"，返回它的端口（正好就是 authproxy 需要的 TargetPort）
func startTarget(t *testing.T) string {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-From", "target")
		io.WriteString(w, "hello from real service")
	}))
	t.Cleanup(target.Close)

	u, err := url.Parse(target.URL) // http://127.0.0.1:PORT
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// newProxy 起一个网关，返回它的 base URL
func newProxy(t *testing.T, targetPort string, ttl time.Duration) string {
	t.Helper()
	p, err := New(Config{
		Username:   "alice",
		Password:   "s3cret",
		TargetPort: targetPort,
		SigningKey: RandomKey(),
		CookieTTL:  ttl,
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := p.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { p.Stop() })
	return "http://127.0.0.1:" + strconv.Itoa(p.ListenPort())
}

// 没登录就访问 → 应该看到登录页，而不是本地服务
func TestServesLoginPageWhenUnauthenticated(t *testing.T) {
	base := newProxy(t, startTarget(t), time.Hour)

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d, 期望 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "身份验证") {
		t.Errorf("应返回登录页，实际内容:\n%s", body)
	}
	if resp.Header.Get("X-From") == "target" {
		t.Error("未登录却拿到了真实服务的内容")
	}
}

// 密码错 → 打回登录页并带 ?error=1，且不发 Cookie
func TestLoginWithWrongPassword(t *testing.T) {
	base := newProxy(t, startTarget(t), time.Hour)

	// 用不跟随重定向的 client，才能看到 303 本身
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(base+loginPath, url.Values{"username": {"alice"}, "password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("状态码 = %d, 期望 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/?error=1" {
		t.Errorf("Location = %q, 期望 /?error=1", loc)
	}
	if len(resp.Cookies()) != 0 {
		t.Error("密码错误时不该发 Cookie")
	}
}

// 密码对 → 发签名 Cookie，带着它能拿到真实服务的内容
func TestLoginAndAccessProtectedService(t *testing.T) {
	base := newProxy(t, startTarget(t), time.Hour)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(base+loginPath, url.Values{"username": {"alice"}, "password": {"s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("状态码 = %d, 期望 303", resp.StatusCode)
	}
	var auth *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			auth = c
		}
	}
	if auth == nil {
		t.Fatal("登录成功后应发 __flare_auth Cookie")
	}
	if !auth.HttpOnly || !auth.Secure {
		t.Errorf("Cookie 应同时是 HttpOnly 和 Secure，实际 HttpOnly=%v Secure=%v", auth.HttpOnly, auth.Secure)
	}
	if !strings.Contains(auth.Value, ".") {
		t.Errorf("Cookie 值应是 payload.签名 的格式，实际 %q", auth.Value)
	}

	// Cookie 是 Secure 的（浏览器只在 https 下回传），测试里走的是 http，
	// 所以手动把它放进请求头，模拟"https 下浏览器自动回传"的行为
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.AddCookie(auth)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)

	if resp2.Header.Get("X-From") != "target" {
		t.Errorf("带有效 Cookie 时应转发到真实服务，实际内容:\n%s", body)
	}
	if !strings.Contains(string(body), "hello from real service") {
		t.Errorf("应拿到真实服务内容，实际:\n%s", body)
	}
}

// 伪造/篡改的 Cookie 必须被拒（签名校验）
func TestTamperedCookieRejected(t *testing.T) {
	base := newProxy(t, startTarget(t), time.Hour)

	cases := map[string]string{
		"没签名":    "alice:1",
		"改了用户名":  "mallory:1.abcd",
		"没有这种格式": "garbage",
		"签名对不上":  "alice:ffffffffffffffff.deadbeef",
	}
	for name, value := range cases {
		req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: value})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("X-From") == "target" {
			t.Errorf("%s：伪造的 Cookie %q 竟然通过了", name, value)
		}
		if !strings.Contains(string(body), "身份验证") {
			t.Errorf("%s：应回到登录页", name)
		}
	}
}

// 过期的 Cookie 必须被拒：CookieTTL 到点就作废（哪怕签名是对的）
func TestExpiredCookieRejected(t *testing.T) {
	// TTL 给 10 毫秒，登录后睡 30 毫秒，签名仍然有效但已过期
	base := newProxy(t, startTarget(t), 10*time.Millisecond)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(base+loginPath, url.Values{"username": {"alice"}, "password": {"s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(resp.Cookies()) == 0 {
		t.Fatal("登录成功后应有 Cookie")
	}
	auth := resp.Cookies()[0]

	time.Sleep(30 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.AddCookie(auth)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if resp2.Header.Get("X-From") == "target" {
		t.Errorf("过期的 Cookie 不该放行，实际内容:\n%s", body)
	}
}

// 登录页里引用的路径要和代码里的常量一致，否则表单会 POST 到不存在的地址
func TestLoginPageFormActionMatchesConstant(t *testing.T) {
	if !strings.Contains(string(loginHtml), `action="`+loginPath+`"`) {
		t.Errorf("login.html 的表单地址与 loginPath(%q) 不一致", loginPath)
	}
}
