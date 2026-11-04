package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/regions"
)

// QifuEndpoint 是百度企服 IP 画像接口地址（独立成变量，便于测试替换）。
const QifuEndpoint = "https://qifu.baidu.com/api/v1/ip-portrait/brief-info"

// qifuReferer 是 WAF 要求的 Referer，必须是 qifu.baidu.com 域。
const qifuReferer = "https://qifu.baidu.com/"

// qifuUA 必须是浏览器 UA，curl/xx 这类会被 openresty 直接 403。
const qifuUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0"

// Qifu 走百度企服的 IP 画像接口：
//
//	GET https://qifu.baidu.com/api/v1/ip-portrait/brief-info?ip=<ip>
//
// 无需 AK、无需 Cookie（2026-11 实测 Acs-Token 与 Cookie 均可省略），
// 只要浏览器 UA + qifu 域 Referer 即可。返回结构化字段：
//
//	country / province / city / isp / scene / risk_score / security_risks
//
// 相比 opendata 的 "广东省广州市 电信" 一个串，这里省了解析，且额外带风险标签。
// 精度同样是省+市（city 常为 null，尤其直辖市/境外），区镇给不出。
//
// 限流实测（2026-11-04，约 6.3 万次请求）：无配额、无 429、不封 IP；
// 恒速 40 QPS 连跑 5 分钟零错误。超 ~60 QPS 持续时会出现尾部延迟爆炸
// 和服务端背压，偶发 25s 超时。生产建议控在 20~30 QPS。
type Qifu struct {
	client   *http.Client
	endpoint string
	retries  int
}

// QifuOption 配置项。
type QifuOption func(*Qifu)

// WithQifuEndpoint 替换接口地址（测试用）。
func WithQifuEndpoint(u string) QifuOption {
	return func(q *Qifu) { q.endpoint = u }
}

// WithQifuRetries 设置瞬时错误重试次数（不含限流/无数据）。
func WithQifuRetries(n int) QifuOption {
	return func(q *Qifu) { q.retries = n }
}

// NewQifu 创建 qifu provider。
func NewQifu(timeout time.Duration, opts ...QifuOption) *Qifu {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	q := &Qifu{
		endpoint: QifuEndpoint,
		retries:  2,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 60 * time.Second}).DialContext,
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Name 返回 provider 名（用于缓存表 provider 字段）。
func (q *Qifu) Name() string { return "qifu" }

// Short 返回 source 标记用的短名。
func (q *Qifu) Short() string { return "qifu" }

// Lookup 查询单个 IP。
func (q *Qifu) Lookup(ctx context.Context, ip uint32) (Result, error) {
	u := q.endpoint + "?" + url.Values{"ip": {geo.Uint2IP(ip)}}.Encode()

	var lastErr error
	for attempt := 0; attempt <= q.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(time.Duration(300<<uint(attempt-1)) * time.Millisecond):
			}
		}

		res, err := q.once(ctx, u)
		if err == nil {
			return res, nil
		}
		if IsCacheable(err) || err == ErrBlocked {
			return Result{}, err
		}
		lastErr = err
	}
	return Result{}, fmt.Errorf("qifu 查询失败: %w", lastErr)
}

// qifuData 是 data 字段。全部可能为 null，统一用值类型接收（null → 零值）。
type qifuData struct {
	Country   string `json:"country"`
	Province  string `json:"province"`
	City      string `json:"city"`
	ISP       string `json:"isp"`
	Scene     string `json:"scene"`
	Company   string `json:"company"`
	RiskScore string `json:"risk_score"`
	HitRisk   int    `json:"hit_risk_num"`
	QueryIP   string `json:"query_ip"`
	Version   string `json:"version"`
}

// qifuResp 是接口返回体。data 可能为 null，故用指针。
type qifuResp struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	TraceID string    `json:"traceId"`
	Data    *qifuData `json:"data"`
}

// once 执行一次请求并解析。
func (q *Qifu) once(ctx context.Context, reqURL string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", qifuUA)
	req.Header.Set("Referer", qifuReferer)
	req.Header.Set("Accept", "*/*")

	resp, err := q.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("请求 qifu: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden:
		return Result{}, ErrBlocked
	case resp.StatusCode != http.StatusOK:
		return Result{}, fmt.Errorf("qifu HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("读 qifu 响应: %w", err)
	}
	// 软限流会返回 HTTP 200 + 空 body，只看状态码会误判成成功。
	if strings.TrimSpace(string(body)) == "" {
		return Result{}, fmt.Errorf("qifu 返回空响应体（疑似软限流）")
	}

	var r qifuResp
	if err := json.Unmarshal(body, &r); err != nil {
		return Result{}, fmt.Errorf("解析 qifu 响应: %w（body=%s）", err, truncate(string(body), 200))
	}
	if r.Code != 200 {
		return Result{}, fmt.Errorf("qifu 业务码 %d: %s", r.Code, r.Message)
	}
	// 非法 IP 也回 code=200，但 data 全 null（query_ip 为空）——判为无数据。
	if r.Data == nil || strings.TrimSpace(r.Data.QueryIP) == "" {
		return Result{}, ErrNoData
	}
	return r.Data.toResult(), nil
}

// toResult 把 qifu 的结构化字段转成统一 Result。
//
// 省市走 regions.ParseLocation 复用既有归一规则（直辖市的 City 回填成省级、
// 运营商名归一成 "中国电信" 这类规范名），保证与 opendata 源写库的形态一致。
// 区县一律留空：qifu 给不出区镇，乱填会污染缓存。
func (d *qifuData) toResult() Result {
	res := Result{
		Country: strings.TrimSpace(d.Country),
		ISP:     normISP(d.ISP),
		Raw:     d.raw(),
	}
	if res.Country != "中国" {
		res.Province = strings.TrimSpace(d.Province)
		res.City = strings.TrimSpace(d.City)
		return res
	}

	prov := strings.TrimSpace(d.Province)
	loc := regions.ParseLocation("中国" + prov + strings.TrimSpace(d.City))
	res.Country = "中国"
	res.Province = loc.Province
	res.City = loc.City
	// 省名不在内置表里时退回原始值，别把省丢了。
	if res.Province == "" {
		res.Province = prov
	}
	return res
}

// raw 拼一个可读的原始串，便于排查（形如 "中国北京市 中国联通"）。
func (d *qifuData) raw() string {
	geo := strings.TrimSpace(d.Country + strings.TrimSpace(d.Province) + strings.TrimSpace(d.City))
	isp := strings.TrimSpace(d.ISP)
	switch {
	case geo == "" && isp == "":
		return ""
	case isp == "":
		return geo
	case geo == "":
		return isp
	default:
		return geo + " " + isp
	}
}

// normISP 归一运营商名，并过滤掉 qifu 用 "未知" 表示的无值。
func normISP(s string) string {
	s = strings.TrimSpace(s)
	switch s {
	case "", "未知", "null", "-", "--":
		return ""
	}
	if canon, ok := regions.NormalizeISP(s); ok {
		return canon
	}
	return s
}
