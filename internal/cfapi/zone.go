package cfapi

import (
	"net/url"
	"strconv"
	"strings"
)

type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListZones 列出账户下所有域名（自动翻页）
func (c *Client) ListZones() ([]Zone, error) {
	q := url.Values{}
	q.Set("account.id", c.accountId)
	q.Set("per_page", "50")

	var all []Zone
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		var zones []Zone
		info, err := c.do(c.ctx, "GET", "/zones", q, nil, &zones)
		if err != nil {
			return nil, err
		}
		all = append(all, zones...)
		if info == nil || page >= info.TotalPages {
			break
		}
	}
	return all, nil
}

// FindZoneForDomain：把"一个完整域名"归到它所属的 Zone。
// 逻辑：域名要么等于 zone 名，要么以 "."+zone 名结尾。
// 必须这样匹配 example.co.uk 和 example.com 都是合法 zone，

func (c *Client) FindZoneForDomain(domain string) (*Zone, error) {
	zones, err := c.ListZones()
	if err != nil {
		return nil, err
	}
	//遍历所有的zone
	for i := range zones {
		z := zones[i]
		// 检查域名是否等于 zone 名称或以 zone 名称结尾
		if z.Name == domain || strings.HasSuffix(domain, "."+z.Name) {
			return &z, nil
		}
	}
	return nil, nil
}
