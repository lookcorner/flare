package cfapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

type Tunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// IngressRule：一行规则 = 哪个域名 → 转发到哪个本地服务
type IngressRule struct {
	Hostname string // 可为空（catch-all 规则）
	Service  string // 如 http://localhost:3000 或 http_status:404
}

// CreateTunnel：云端建一条隧道。ConfigSrc 固定 "cloudflare" =
// ingress 规则表存在云端（而不是本地配置文件），这也是 up 命令能
// 只凭一个 token 跑起来的前提。
func (c *Client) CreateTunnel(name string) (*Tunnel, error) {
	body := struct {
		Name      string `json:"name"`
		ConfigSrc string `json:"config_src"`
	}{
		Name:      name,
		ConfigSrc: "cloudflare",
	}
	var t Tunnel
	if _, err := c.do(c.ctx, http.MethodPost,
		"/accounts/"+c.accountId+"/cfd_tunnel", nil, body, &t); err != nil {
		return nil, fmt.Errorf("创建隧道失败: %w", err)
	}
	return &t, nil
}

// 列出所有的通道（自动翻页）
func (c *Client) ListTunnels() ([]Tunnel, error) {
	q := url.Values{}
	q.Set("per_page", "50")

	var all []Tunnel
	// 循环请求分页
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		var tunnels []Tunnel
		info, err := c.do(c.ctx, http.MethodGet,
			"/accounts/"+c.accountId+"/cfd_tunnel", q, nil, &tunnels)
		if err != nil {
			return nil, fmt.Errorf("列出隧道失败: %w", err)
		}
		all = append(all, tunnels...)
		// 检查是否有更多页面
		if info == nil || page >= info.TotalPages {
			break
		}
	}

	return all, nil
}

// 删除通道
func (c *Client) DeleteTunnel(tunnelID string) error {
	_, err := c.do(c.ctx, http.MethodDelete,
		"/accounts/"+c.accountId+"/cfd_tunnel/"+tunnelID, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("删除隧道失败: %w", err)
	}
	return nil
}

// GetTunnelToken：取运行凭据。cloudflared 拿着它 run，
// 就能自己从云端拉取这条隧道的 ingress 配置。
// 两个坑：① 这是 GET（写成 POST 会 405，而这个请求失败等于 create 半途而废）；
// ② 响应 result 是一个纯字符串，所以 out 用 *string。
func (c *Client) GetTunnelToken(tunnelID string) (string, error) {
	var token string
	_, err := c.do(c.ctx, http.MethodGet,
		"/accounts/"+c.accountId+"/cfd_tunnel/"+tunnelID+"/token", nil, nil, &token)
	if err != nil {
		return "", fmt.Errorf("获取隧道 token 失败: %w", err)
	}
	return token, nil
}

func (c *Client) PushIngressConfig(tunnelId string, rules []IngressRule) error {
	ingress := make([]map[string]string, 0, len(rules)+1)
	for _, rule := range rules {
		item := map[string]string{"service": rule.Service}
		if rule.Hostname != "" {
			item["hostname"] = rule.Hostname
		}
		ingress = append(ingress, item)
	}

	// 永远追加一条 catch-all：任何没匹配上的域名 → 404（不会误暴露其它服务）
	ingress = append(ingress, map[string]string{"service": "http_status:404"})
	body := map[string]any{
		"config": map[string]any{"ingress": ingress},
	}
	if _, err := c.do(c.ctx, http.MethodPut,
		"/accounts/"+c.accountId+"/cfd_tunnel/"+tunnelId+"/configurations",
		nil, body, nil); err != nil {
		return fmt.Errorf("推送 ingress 配置失败: %w", err)
	}
	return nil

}
