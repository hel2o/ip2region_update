package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/regions"
)

// Baidu 走百度 opendata 的 IP 查询接口：
//
//	https://opendata.baidu.com/api.php?query=<ip>&co=&resource_id=6006&oe=utf8
//
// 免费、无需 AK。返回 data[0].location，形如 "广东省广州市 电信"。
// 精度到"省+市+ISP"（少数机房 IP 能到区），区镇给不出。
type Baidu struct {
	client   *http.Client
	endpoint string
	retries  int
}

// BaiduOption 配置项。
type BaiduOption func(*Baidu)

// WithBaiduEndpoint 替换接口地址（测试用）。
func WithBaiduEndpoint(u string) BaiduOption {
	return func(b *Baidu) { b.endpoint = u }
}

// WithBaiduRetries 设置瞬时错误重试次数（不含限流）。
func WithBaiduRetries(n int) BaiduOption {
	return func(b *Baidu) { b.retries = n }
}

// NewBaidu 创建百度 provider。
func NewBaidu(timeout time.Duration, opts ...BaiduOption) *Baidu {
	b := &Baidu{
		endpoint: "https://opendata.baidu.com/api.php",
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
		o(b)
	}
	return b
}

// Name 返回 provider 名（用于缓存表 provider 字段）。
func (b *Baidu) Name() string { return "baidu" }

// Short 返回 source 标记用的短名。
func (b *Baidu) Short() string { return "baidu" }

// Lookup 查询单个 IP。
func (b *Baidu) Lookup(ctx context.Context, ip uint32) (Result, error) {
	q := url.Values{}
	q.Set("query", geo.Uint2IP(ip))
	q.Set("co", "")
	q.Set("resource_id", "6006")
	q.Set("oe", "utf8")
	reqURL := b.endpoint + "?" + q.Encode()

	var lastErr error
	for attempt := 0; attempt <= b.retries; attempt++ {
		if attempt > 0 {
			// 瞬时错误退避：300ms, 600ms
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(time.Duration(300<<uint(attempt-1)) * time.Millisecond):
			}
		}

		res, err := b.once(ctx, reqURL)
		if err == nil {
			return res, nil
		}
		if IsCacheable(err) || err == ErrBlocked {
			return Result{}, err
		}
		lastErr = err
	}
	return Result{}, fmt.Errorf("baidu 查询失败: %w", lastErr)
}

// once 执行一次请求并解析。
func (b *Baidu) once(ctx context.Context, reqURL string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Referer", "https://www.baidu.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := b.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("请求百度: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden:
		return Result{}, ErrBlocked
	case resp.StatusCode >= 500:
		return Result{}, fmt.Errorf("百度 HTTP %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Result{}, fmt.Errorf("百度 HTTP %d", resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
		Data   []struct {
			Location string `json:"location"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Result{}, fmt.Errorf("解析百度响应: %w", err)
	}

	if len(body.Data) == 0 || body.Data[0].Location == "" {
		return Result{}, ErrNoData
	}

	loc := regions.ParseLocation(body.Data[0].Location)
	return Result{
		Country:  loc.Country,
		Province: loc.Province,
		City:     loc.City,
		County:   loc.County,
		ISP:      loc.ISP,
		Raw:      body.Data[0].Location,
	}, nil
}
