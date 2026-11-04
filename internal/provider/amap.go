package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
)

// amapBaseURL 独立成变量，方便测试替换。
var amapBaseURL = "https://restapi.amap.com/v3/ip"

// Amap 是高德开放平台的 IP 定位接口。
//
// 它只用于补百度没给省市的缺口（见 enrich 的 Fallback 机制），
// 不承担主力查询——百度对这个接口没有配额，高德有月度配额。
//
// 接口：GET https://restapi.amap.com/v3/ip?ip=<ip>&key=<key>
// 返回：province / city / adcode / rectangle，精度到市级。
//
// 已知限制（文档明写）：
//   - 只支持 IPv4，国外 IP 返回空
//   - 局域网 IP 返回 province="局域网"
//   - 直辖市 province 与 city 同名（北京市/北京市）
type Amap struct {
	key    string
	client *http.Client
}

// NewAmap 创建高德 provider。key 为空时返回 nil（调用方据此判断是否启用）。
func NewAmap(key string, timeout time.Duration) *Amap {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Amap{
		key: strings.TrimSpace(key),
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}
}

// Name 返回 provider 标识，用于缓存区分。
func (a *Amap) Name() string { return "amap" }

type amapResp struct {
	Status   string          `json:"status"`
	Info     string          `json:"info"`
	InfoCode string          `json:"infocode"`
	Province json.RawMessage `json:"province"` // 空值时是 [] 而不是 ""
	City     json.RawMessage `json:"city"`
	Adcode   json.RawMessage `json:"adcode"`
}

// Lookup 查询单个 IP 的归属。
func (a *Amap) Lookup(ctx context.Context, ip uint32) (Result, error) {
	q := url.Values{}
	q.Set("ip", geo.Uint2IP(ip))
	q.Set("key", a.key)

	u := amapBaseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "ip2region/1.0")

	resp, err := a.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	// 高德的配额错误是 200 + status=0，必须看 body 而不是状态码
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
			return Result{}, fmt.Errorf("%w: HTTP %d", ErrBlocked, resp.StatusCode)
		}
		return Result{}, fmt.Errorf("高德返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var r amapResp
	if err := json.Unmarshal(body, &r); err != nil {
		return Result{}, fmt.Errorf("解析高德响应: %w（body=%s）", err, truncate(string(body), 200))
	}
	if r.Status != "1" {
		// 10003/10044 等是配额或权限问题，直接判封禁避免继续烧
		if r.InfoCode == "10003" || r.InfoCode == "10044" || r.InfoCode == "10004" {
			return Result{}, fmt.Errorf("%w: %s(%s)", ErrBlocked, r.Info, r.InfoCode)
		}
		return Result{}, fmt.Errorf("高德错误 %s(%s)", r.Info, r.InfoCode)
	}

	prov := cleanAmapField(rawString(r.Province))
	city := cleanAmapField(rawString(r.City))

	res := Result{Province: prov, City: city}
	if prov != "" || city != "" {
		res.Country = "中国"
	}
	raw := strings.TrimSpace(prov + city)
	if raw == "" {
		raw = "（高德无数据）"
	}
	res.Raw = raw
	return res, nil
}

// rawString 把 json.RawMessage 里真正的字符串取出来。
// 高德在"无数据"时给的可能是 []、null 或空数组，统一返回空串。
func rawString(r json.RawMessage) string {
	s := strings.TrimSpace(string(r))
	if len(s) < 2 || s[0] != '"' {
		return ""
	}
	var out string
	if err := json.Unmarshal(r, &out); err != nil {
		return ""
	}
	return out
}

// cleanAmapField 处理高德返回的特殊值。
func cleanAmapField(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "[]", "null", "局域网":
		return ""
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
