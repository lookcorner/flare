package cfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// baseURL 是 API 根地址。写成变量而不是常量，是为了测试能把它指向一台
// 假的 Cloudflare 服务器（httptest）——这一层的方法/路径写错编译期看不出来，
// 只能靠测试钉住。
var baseURL = "https://api.cloudflare.com/client/v4"

type Client struct {
	token     string
	accountId string
	hc        *http.Client
	ctx       context.Context
}

// 响应字段
type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"` // 原始字节，稍后按端点类型解码
	ResultInfo *resultInfo     `json:"result_info"`
}

// 分页信息，仅列表端点返回；非列表端点为 nil。
// 这四个字段逐字对应 Cloudflare 的 result_info：翻页只用 TotalPages，
// 其余三个留着是为了让这个结构体如实反映接口返回的格式（读代码时不用再翻文档）
type resultInfo struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_count"`
}

// 响应错误字段
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// 创建客户端。所有方法共享这一个实例（连接可复用）
func New(token, accountId string) *Client {
	return &Client{
		token:     token,
		accountId: accountId,
		hc:        &http.Client{Timeout: 30 * time.Second},
		ctx:       context.Background(),
	}
}

// do 是唯一的 HTTP 出口。六个参数对应 curl 的六个可变部分：
//
//	method: GET/POST/PUT/DELETE   path: URL 路径（可含 /accounts/%s 这种占位）
//	query: 查询参数（可为 nil）    body: 请求体对象（可为 nil）
//	out:   把 result 解码到哪个指针（可为 nil，如 DELETE）
//
// 返回分页信息，仅列表端点非 nil；调用方不需要可忽略
func (c *Client) do(ctx context.Context, methond, path string, query url.Values, body, out any) (*resultInfo, error) {
	u := baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewBuffer(buf)
	}
	req, err := http.NewRequestWithContext(ctx, methond, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close() // 单请求场景，defer 没问题
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// var env envelope
	env := new(envelope)
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("HTTP %d 响应无法解析: %v", resp.StatusCode, err)
	}
	if !env.Success {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
		}
		return nil, fmt.Errorf("Cloudflare API 错误: %s", strings.Join(msgs, "; "))
	}
	// success == true：把 result 解到调用方指定的结构
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("解析 result 失败: %w", err)
		}
	}
	return env.ResultInfo, nil
}
