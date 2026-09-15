package cfapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 这套测试拿一台"假 Cloudflare"（httptest）把 cfapi 发出的每个请求钉死：
// 方法、路径、请求体。
//
// 为什么值得这么干：这一层的错误编译期完全看不出来——把 GET 写成 POST、
// 把新建用的 POST 写成缺 id 的 PUT，都是只能带着真 token 打真 API 才暴露的问题
// （表现就是 create 最后一步失败、add 建不出新记录）。有假服务器之后，
// 这些都能在本地零成本回归。

type call struct {
	Method string
	Path   string
	Query  string
	Body   []byte
}

type stub struct {
	*httptest.Server
	mu    sync.Mutex
	calls []call
	// route 返回 true 表示这个请求已被处理
	route func(w http.ResponseWriter, r *http.Request, body []byte) bool
}

// startStub 起假服务器，并把包内 baseURL 指过去（测试结束后自动还原）
func startStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.calls = append(s.calls, call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
		route := s.route
		s.mu.Unlock()

		if route == nil || !route(w, r, body) {
			t.Errorf("假服务器没准备这条路由: %s %s", r.Method, r.URL.Path)
			writeAPIError(w, http.StatusNotFound, 10000, "no stub route")
		}
	}))
	old := baseURL
	baseURL = s.URL
	t.Cleanup(func() {
		baseURL = old
		s.Close()
	})
	return s
}

// lastCall / callsTo 便于断言"发了什么请求"
func (s *stub) callsTo(method, path string) []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []call
	for _, c := range s.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// 写一个成功信封：{"success":true,"result":...}
func writeResult(w http.ResponseWriter, result any) {
	raw, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"success":true,"result":`+string(raw)+`}`)
}

// 带分页信息的列表信封
func writeList(w http.ResponseWriter, result any, info resultInfo) {
	raw, _ := json.Marshal(result)
	inf, _ := json.Marshal(info)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"success":true,"result":`+string(raw)+`,"result_info":`+string(inf)+`}`)
}

// 失败信封：Cloudflare 的错误长这样
func writeAPIError(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, `{"success":false,"errors":[{"code":`+itoa(code)+`,"message":`+quote(msg)+`}],"result":null}`)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func newTestClient() *Client { return New("test-token", "acct-1") }

// 请求头里必须带上 token：漏了会被 Cloudflare 判 10000 未授权
func TestAuthHeaderSent(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, 期望 Bearer test-token", got)
		}
		writeResult(w, "tok-abc")
		return true
	}
	if _, err := newTestClient().GetTunnelToken("tun-1"); err != nil {
		t.Fatal(err)
	}
}

// 回归测试①：token 端点在 Cloudflare 只有 GET，写成 POST 会 405——
// 而 create 是在建完隧道之后才取 token 的，所以这个错会让 create 必然半途而废
func TestGetTunnelTokenUsesGet(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		writeResult(w, "eyJhIjoi...token")
		return true
	}
	tok, err := newTestClient().GetTunnelToken("tun-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "eyJhIjoi...token" {
		t.Errorf("token = %q", tok)
	}
	got := s.callsTo(http.MethodGet, "/accounts/acct-1/cfd_tunnel/tun-1/token")
	if len(got) != 1 {
		t.Fatalf("应发 1 次 GET token 请求，实际 %d 次；全部请求: %+v", len(got), s.calls)
	}
	if n := len(s.callsTo(http.MethodPost, "/accounts/acct-1/cfd_tunnel/tun-1/token")); n != 0 {
		t.Errorf("不该用 POST 取 token，实际发了 %d 次", n)
	}
}

// 隧道的 config_src 必须是 cloudflare：这样 ingress 规则才存在云端，
// up 才能只凭一个 token 跑起来
func TestCreateTunnelSendsCloudflareConfigSrc(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		var got struct {
			Name      string `json:"name"`
			ConfigSrc string `json:"config_src"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		if got.Name != "my-tunnel" || got.ConfigSrc != "cloudflare" {
			t.Errorf("请求体 = %+v, 期望 name=my-tunnel config_src=cloudflare", got)
		}
		writeResult(w, Tunnel{ID: "tun-1", Name: "my-tunnel"})
		return true
	}
	tun, err := newTestClient().CreateTunnel("my-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if tun.ID != "tun-1" || tun.Name != "my-tunnel" {
		t.Errorf("返回的隧道 = %+v", tun)
	}
	if n := len(s.callsTo(http.MethodPost, "/accounts/acct-1/cfd_tunnel")); n != 1 {
		t.Errorf("应发 1 次 POST 建隧道，实际 %d 次", n)
	}
}

// FindZoneForDomain：完整域名要归到正确的 Zone（example.com 与 sub.example.com 都算）
func TestFindZoneForDomain(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.URL.Path != "/zones" {
			return false
		}
		// 必须按账户过滤，否则会拿到别人的域名
		if q := r.URL.Query().Get("account.id"); q != "acct-1" {
			t.Errorf("account.id = %q, 期望 acct-1", q)
		}
		writeList(w, []Zone{{ID: "z-example", Name: "example.com"}, {ID: "z-other", Name: "other.net"}},
			resultInfo{Page: 1, PerPage: 50, TotalPages: 1, TotalCount: 2})
		return true
	}
	z, err := newTestClient().FindZoneForDomain("web.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if z == nil || z.ID != "z-example" {
		t.Fatalf("Zone = %+v, 期望 z-example", z)
	}
	// 不在账户下的域名：返回 nil 而不是瞎猜一个 Zone
	z2, err := newTestClient().FindZoneForDomain("nope.test")
	if err != nil {
		t.Fatal(err)
	}
	if z2 != nil {
		t.Errorf("不该匹配到 Zone，实际 %+v", z2)
	}
}

// 回归测试②：新建 DNS 记录是 POST /zones/{zone}/dns_records。
// 之前写成了 PUT 且带个没人认得的结尾斜杠，结果是 add 建不出新记录
func TestCreateCNAMEUsesPostWithoutTrailingSlash(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		writeResult(w, dnsRecord{ID: "rec-1", Name: "web.example.com"})
		return true
	}
	id, err := newTestClient().CreateCNAME("z-example", "web.example.com", "tun-1.cfargotunnel.com")
	if err != nil {
		t.Fatal(err)
	}
	if id != "rec-1" {
		t.Errorf("记录 ID = %q", id)
	}
	const wantPath = "/zones/z-example/dns_records"
	if n := len(s.callsTo(http.MethodPost, wantPath)); n != 1 {
		t.Fatalf("应发 1 次 POST %s，实际 %d 次；全部请求: %+v", wantPath, n, s.calls)
	}
	if n := len(s.callsTo(http.MethodPut, wantPath+"/")); n != 0 {
		t.Errorf("不该用带结尾斜杠的 PUT 新建记录，实际 %d 次", n)
	}
	c := s.callsTo(http.MethodPost, wantPath)[0]
	var body cnameBody
	json.Unmarshal(c.Body, &body)
	if body.Type != "CNAME" || body.Name != "web.example.com" ||
		body.Content != "tun-1.cfargotunnel.com" || !body.Proxied || body.TTL != 1 {
		t.Errorf("请求体 = %+v", body)
	}
}

// 已存在记录时用 PUT + 记录 ID 改目标（add 的幂等分支）
func TestUpdateCNAMEUsesPutWithRecordID(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		writeResult(w, dnsRecord{ID: "rec-9"})
		return true
	}
	if err := newTestClient().UpdateCNAME("z-example", "rec-9", "web.example.com", "tun-2.cfargotunnel.com"); err != nil {
		t.Fatal(err)
	}
	calls := s.callsTo(http.MethodPut, "/zones/z-example/dns_records/rec-9")
	if len(calls) != 1 {
		t.Fatalf("应发 1 次 PUT /zones/z-example/dns_records/rec-9，实际 %d 次；全部请求: %+v", len(calls), s.calls)
	}
	var body cnameBody
	json.Unmarshal(calls[0].Body, &body)
	if body.Content != "tun-2.cfargotunnel.com" {
		t.Errorf("目标未更新，请求体 = %+v", body)
	}
}

func TestDeleteDNSRecord(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		writeResult(w, map[string]string{"id": "rec-1"})
		return true
	}
	if err := newTestClient().DeleteDNSRecord("z-example", "rec-1"); err != nil {
		t.Fatal(err)
	}
	if n := len(s.callsTo(http.MethodDelete, "/zones/z-example/dns_records/rec-1")); n != 1 {
		t.Errorf("应发 1 次 DELETE，实际 %d 次", n)
	}
}

// 查记录命中/未命中：未命中是空串 + nil（不是错误），调用方据此决定新建还是更新
func TestFindDNSRecordFoundAndMissing(t *testing.T) {
	s := startStub(t)
	found := false
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if q := r.URL.Query().Get("name"); q != "web.example.com" {
			t.Errorf("name 查询参数 = %q", q)
		}
		if found {
			writeResult(w, []dnsRecord{{ID: "rec-1", Name: "web.example.com"}})
		} else {
			// Cloudflare 可能返回"父域名"的记录，必须精确比对，不能拿它当命中
			writeResult(w, []dnsRecord{{ID: "rec-parent", Name: "example.com"}})
		}
		return true
	}
	id, err := newTestClient().FindDNSRecord("z-example", "web.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("名字不完全一致时不该命中，实际 %q", id)
	}
	found = true
	id, err = newTestClient().FindDNSRecord("z-example", "web.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if id != "rec-1" {
		t.Errorf("应命中 rec-1，实际 %q", id)
	}
}

// 回归测试③：查询失败必须报错。
// 之前这里 return "", nil，等于告诉调用方"云端没有这条记录"，
// 于是 add 会去新建一条重名记录
func TestFindDNSRecordPropagatesError(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		writeAPIError(w, http.StatusForbidden, 9109, "Unauthorized to access zone")
		return true
	}
	_, err := newTestClient().FindDNSRecord("z-example", "web.example.com")
	if err == nil {
		t.Fatal("查询失败时应返回错误，实际 nil")
	}
	if !strings.Contains(err.Error(), "Unauthorized to access zone") {
		t.Errorf("错误信息里应带上 Cloudflare 的说明，实际: %v", err)
	}
}

// ingress 推送：规则按本地顺序翻译，最后必须补一条 catch-all，
// 且 catch-all 不能带 hostname（带了就变成"某个域名"而不是兜底）
func TestPushIngressConfig(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		writeResult(w, nil)
		return true
	}
	rules := []IngressRule{
		{Hostname: "web.example.com", Service: "http://localhost:3000"},
		{Hostname: "api.example.com", Service: "http://localhost:8080"},
	}
	if err := newTestClient().PushIngressConfig("tun-1", rules); err != nil {
		t.Fatal(err)
	}
	calls := s.callsTo(http.MethodPut, "/accounts/acct-1/cfd_tunnel/tun-1/configurations")
	if len(calls) != 1 {
		t.Fatalf("应发 1 次 PUT configurations，实际 %d 次；全部请求: %+v", len(calls), s.calls)
	}
	var got struct {
		Config struct {
			Ingress []map[string]string `json:"ingress"`
		} `json:"config"`
	}
	if err := json.Unmarshal(calls[0].Body, &got); err != nil {
		t.Fatal(err)
	}
	ing := got.Config.Ingress
	if len(ing) != 3 {
		t.Fatalf("应有 2 条规则 + 1 条兜底，实际 %d: %+v", len(ing), ing)
	}
	if ing[0]["hostname"] != "web.example.com" || ing[0]["service"] != "http://localhost:3000" {
		t.Errorf("第一条规则 = %+v", ing[0])
	}
	last := ing[len(ing)-1]
	if _, ok := last["hostname"]; ok {
		t.Errorf("兜底规则不该带 hostname，实际 %+v", last)
	}
	if last["service"] != "http_status:404" {
		t.Errorf("兜底规则应是 http_status:404，实际 %+v", last)
	}
}

// 删隧道 + 分页列隧道（ListTunnels 目前没有命令用它，
// 但分页逻辑容易写错，这里一并钉住）
func TestListTunnelsPaginatesAndDeleteTunnel(t *testing.T) {
	s := startStub(t)
	page1 := []Tunnel{{ID: "tun-1", Name: "a"}, {ID: "tun-2", Name: "b"}}
	page2 := []Tunnel{{ID: "tun-3", Name: "c"}}
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.Method == http.MethodDelete && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tun-1" {
			writeResult(w, nil)
			return true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel" {
			switch r.URL.Query().Get("page") {
			case "1":
				writeList(w, page1, resultInfo{Page: 1, PerPage: 2, TotalPages: 2, TotalCount: 3})
			case "2":
				writeList(w, page2, resultInfo{Page: 2, PerPage: 2, TotalPages: 2, TotalCount: 3})
			default:
				t.Errorf("意外的 page 参数: %q", r.URL.Query().Get("page"))
			}
			return true
		}
		return false
	}

	all, err := newTestClient().ListTunnels()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("应翻页拿全 3 条隧道，实际 %d: %+v", len(all), all)
	}
	if n := len(s.callsTo(http.MethodGet, "/accounts/acct-1/cfd_tunnel")); n != 2 {
		t.Errorf("应请求 2 页，实际 %d 次", n)
	}

	if err := newTestClient().DeleteTunnel("tun-1"); err != nil {
		t.Fatal(err)
	}
	if n := len(s.callsTo(http.MethodDelete, "/accounts/acct-1/cfd_tunnel/tun-1")); n != 1 {
		t.Errorf("应发 1 次 DELETE 隧道，实际 %d 次", n)
	}
}

// Cloudflare 用 HTTP 200 + success:false 报错的情况也要当失败处理，
// 而不是"解析 result 失败"这种让人摸不着头脑的错
func TestAPIErrorSurfaced(t *testing.T) {
	s := startStub(t)
	s.route = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		writeAPIError(w, http.StatusOK, 1056, "tunnel already exists")
		return true
	}
	_, err := newTestClient().CreateTunnel("dup")
	if err == nil {
		t.Fatal("success=false 时应报错")
	}
	if !strings.Contains(err.Error(), "1056") || !strings.Contains(err.Error(), "tunnel already exists") {
		t.Errorf("错误信息应包含 code 与 message，实际: %v", err)
	}
}
