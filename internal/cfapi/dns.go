package cfapi

import (
	"fmt"
	"net/http"
	"net/url"
)

type dnsRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CNAME 请求体（TTL=1 表示自动；Proxied=true 让流量走 CF 橙色云代理）
type cnameBody struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"` // true 表示开启代理（橙色云）
}

// FindDNSRecord：按完整域名查记录 ID；不存在返回空串（不是错误）。
// 拿到列表后逐个精确比对 name，杜绝"查到同名父域名"的错觉。
// 注意：查询失败必须把错误抛出去。吞掉错误返回空串 = 告诉调用方
// "云端没有这条记录"，接着它就会去新建一条重名记录，问题很难查。
func (c *Client) FindDNSRecord(zoneID, name string) (string, error) {
	q := url.Values{}
	q.Set("name", name)
	var recs []dnsRecord
	if _, err := c.do(c.ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records", q, nil, &recs); err != nil {
		return "", fmt.Errorf("查询 DNS 记录失败: %w", err)
	}
	for _, r := range recs {
		if r.Name == name {
			return r.ID, nil
		}
	}
	return "", nil
}

// CreateCNAME：建一条 CNAME，指向 content（即 <隧道ID>.cfargotunnel.com）。
// 这是"新建"端点：POST /zones/{zone}/dns_records。
// （PUT /zones/{zone}/dns_records/{id} 是整条替换，少了 id 的 PUT 匹配不到任何路由）
func (c *Client) CreateCNAME(zoneID, name, content string) (string, error) {
	body := cnameBody{Type: "CNAME", Name: name, Content: content, TTL: 1, Proxied: true}
	var r dnsRecord
	if _, err := c.do(c.ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", nil, body, &r); err != nil {
		return "", fmt.Errorf("创建 CNAME 记录失败: %w", err)
	}
	return r.ID, nil
}
func (c *Client) DeleteDNSRecord(zoneID, recordID string) error {
	if _, err := c.do(c.ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil, nil, nil); err != nil {
		return fmt.Errorf("删除 DNS 记录失败: %w", err)
	}
	return nil
}

// UpdateCNAME：记录已存在时改其目标（add 命令的"有则更新"分支）。
func (c *Client) UpdateCNAME(zoneID, recordID, name, target string) error {
	body := cnameBody{Type: "CNAME", Name: name, Content: target, TTL: 1, Proxied: true}
	if _, err := c.do(c.ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+recordID, nil, body, nil); err != nil {
		return fmt.Errorf("更新 CNAME 记录失败: %w", err)
	}
	return nil
}
