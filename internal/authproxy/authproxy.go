package authproxy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// go:embed：把同目录 login.html 编译进二进制

//go:embed login.html
var loginHtml []byte

const cookieName = "__flare_auth"
const loginPath = "/___auth/login" // 特殊路径：正常服务永远不会用到

// RandomKey：32 字节加密级随机数（crypto/rand）
func RandomKey() []byte {
	key := make([]byte, 32)
	rand.Read(key) // 读失败时 panic——签名密钥生成失败绝不能静默继续
	return key
}

// 鉴权代理配置
type Config struct {
	Username   string
	Password   string
	TargetPort string // 真实服务端口（如 "3000"）
	SigningKey []byte // Cookie 签名密钥
	CookieTTL  time.Duration
}

// 反向代理配置
type Proxy struct {
	cfg      Config
	listener net.Listener
	server   *http.Server
	reverse  *httputil.ReverseProxy
}

func New(cfg Config) (*Proxy, error) {
	if cfg.CookieTTL == 0 {
		cfg.CookieTTL = 24 * time.Hour //零值即默认
	}
	target, _ := url.Parse("http://127.0.0.1:" + cfg.TargetPort)

	//net.Listen("127.0.0.1:0")：端口写 0 = 让操作系统分配一个空闲端口，
	// 永不冲突。真实项目也可从 targetPort+1 逐个试听以让端口可预期——殊途同归
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听失败: %w", err)
	}
	p := &Proxy{
		cfg:      cfg,
		listener: ln,
		reverse:  httputil.NewSingleHostReverseProxy(target),
	}
	p.server = &http.Server{Handler: p}
	return p, nil

}

// ListenPort：外部（cloudflared）要知道往哪个端口转发
func (p *Proxy) ListenPort() int {
	return p.listener.Addr().(*net.TCPAddr).Port
}

func (p *Proxy) Start() error {
	go p.server.Serve(p.listener) // 非阻塞：goroutine 里跑 HTTP 服务
	return nil
}

func (p *Proxy) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx) // 优雅关闭：等正在处理的请求完成
}

// ServeHTTP：网关的全部决策逻辑。四个分支的顺序是有讲究的：
// 方法名必须是 ServeHTTP，大小写是接口契约的一部分（HTTP 全大写）
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//WebSocket 升级请求直接透传
	if isWebSocket(r) {
		p.reverse.ServeHTTP(w, r)
		return
	}
	//登录表单的 POST → 校验账号密码，签发 Cookie 或打回
	if r.Method == http.MethodPost && r.URL.Path == loginPath {
		p.handleLogin(w, r)
		return
	}
	//带有效 Cookie → 放行到真实服务（请求转发给搬运工）
	if p.checkAuth(r) {
		p.reverse.ServeHTTP(w, r)
		return
	}
	// 其余一切 → 返回登录页（静态资源、直接敲 IP 的、带错 Cookie 的）
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(loginHtml)
}

// handleLogin：校验表单里的账号密码，正确则签发签名 Cookie 并跳回首页
func (p *Proxy) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username") //post 表单
	password := r.FormValue("password")
	// 不匹配：跳回首页并带?error=1(登录页 JS 读到就显示红字"用户名或密码错误")
	if username != p.cfg.Username || password != p.cfg.Password {
		http.Redirect(w, r, "/?error=1", http.StatusSeeOther)
		return
	}

	// 匹配：构造payload = 用户名:过期时间(十六进制)，再过签名
	expiry := time.Now().Add(p.cfg.CookieTTL).Unix()
	payload := fmt.Sprintf("%s:%x", username, expiry)
	value := payload + "." + signPayload(p.cfg.SigningKey, payload)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/", // 全站生效
		MaxAge:   int(p.cfg.CookieTTL.Seconds()),
		HttpOnly: true,                 // JS 读不到（防 XSS 窃取）
		Secure:   true,                 // 仅 HTTPS 发送（防明文截获）
		SameSite: http.SameSiteLaxMode, // 防 CSRF：跨站请求不带 Cookie
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (p *Proxy) checkAuth(r *http.Request) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false //没有cookie
	}
	// 格式：payload.sig。用最后一个 "." 切分（payload 里理论上可能含 "."）
	dot := strings.LastIndex(cookie.Value, ".")
	if dot < 0 {
		return false
	}
	payload, sig := cookie.Value[:dot], cookie.Value[dot+1:]
	// 重算签名并比对
	if signPayload(p.cfg.SigningKey, payload) != sig {
		return false
	}
	// 解析过期时间（hex 编码），未过期才算通过
	colon := strings.LastIndex(payload, ":")
	if colon < 0 {
		return false
	}
	expiry, err := strconv.ParseInt(payload[colon+1:], 16, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expiry

}

// signPayload：HMAC-SHA256 签名 → hex 字符串
func signPayload(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)

	mac.Write([]byte(payload))

	return hex.EncodeToString(mac.Sum(nil))
}

// isWebSocket：检测 Upgrade: websocket 请求头
func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
