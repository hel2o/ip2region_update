package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQifuLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WAF 硬性要求：浏览器 UA + qifu 域 Referer，缺一个就 403
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Mozilla/5.0") {
			http.Error(w, "bad ua", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Referer"), "https://qifu.baidu.com") {
			http.Error(w, "bad referer", http.StatusForbidden)
			return
		}
		switch r.URL.Query().Get("ip") {
		case "180.101.49.11": // 省 + 市
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"country":"中国","province":"江苏省","city":"南京市","isp":"中国电信","scene":"IDC","query_ip":"180.101.49.11","version":"v4"}}`))
		case "202.108.22.5": // 直辖市：city 为 null，City 应回填成省级
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"country":"中国","province":"北京市","city":null,"isp":"中国联通","query_ip":"202.108.22.5","version":"v4"}}`))
		case "203.198.0.1": // 特别行政区
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"country":"中国","province":"香港特别行政区","city":null,"isp":"电讯盈科","query_ip":"203.198.0.1","version":"v4"}}`))
		case "8.8.8.8": // 境外
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"country":"美国","province":"","city":"","isp":"谷歌公司","query_ip":"8.8.8.8","version":"v4"}}`))
		case "1.2.3.4": // 非法/无数据：data 全 null
			_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"country":null,"province":null,"city":null,"isp":null,"query_ip":null,"version":null}}`))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	q := NewQifu(3*time.Second, WithQifuEndpoint(srv.URL), WithQifuRetries(0))
	ctx := context.Background()

	res, err := q.Lookup(ctx, ipOf("180.101.49.11"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Country != "中国" || res.Province != "江苏省" || res.City != "南京市" || res.ISP != "中国电信" {
		t.Fatalf("省市结果不符: %+v", res)
	}
	if res.County != "" {
		t.Fatalf("区县必须留空（qifu 给不出），实际 %q", res.County)
	}
	if res.Raw != "中国江苏省南京市 中国电信" {
		t.Fatalf("Raw 不符: %q", res.Raw)
	}

	// 直辖市：qifu 的 city 是 null，但库里语义是 省==市，必须回填
	res, err = q.Lookup(ctx, ipOf("202.108.22.5"))
	if err != nil || res.Province != "北京市" || res.City != "北京市" || res.ISP != "中国联通" {
		t.Fatalf("直辖市结果不符: %+v err=%v", res, err)
	}

	res, err = q.Lookup(ctx, ipOf("203.198.0.1"))
	if err != nil || res.Province != "香港特别行政区" || res.City != "香港特别行政区" {
		t.Fatalf("特区结果不符: %+v err=%v", res, err)
	}

	res, err = q.Lookup(ctx, ipOf("8.8.8.8"))
	if err != nil || res.Country != "美国" || res.Province != "" || res.City != "" || res.ISP != "谷歌公司" {
		t.Fatalf("境外结果不符: %+v err=%v", res, err)
	}

	if _, err = q.Lookup(ctx, ipOf("1.2.3.4")); !errors.Is(err, ErrNoData) {
		t.Fatalf("data 全 null 应返回 ErrNoData，实际 %v", err)
	}
	if _, err = q.Lookup(ctx, ipOf("9.9.9.9")); err == nil || IsCacheable(err) {
		t.Fatalf("5xx 不应被判为可缓存，实际 %v", err)
	}
}

func TestQifuBlockedAndEmptyBody(t *testing.T) {
	// 403 → ErrBlocked，且不重试
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer blocked.Close()

	q := NewQifu(3*time.Second, WithQifuEndpoint(blocked.URL), WithQifuRetries(2))
	start := time.Now()
	_, err := q.Lookup(context.Background(), ipOf("1.1.1.1"))
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("403 应返回 ErrBlocked，实际 %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("403 不应重试退避，耗时 %v", time.Since(start))
	}

	// HTTP 200 + 空 body → 瞬时错误（软限流），必须重试而不是当成功
	var hits int
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer empty.Close()

	q2 := NewQifu(3*time.Second, WithQifuEndpoint(empty.URL), WithQifuRetries(2))
	if _, err := q2.Lookup(context.Background(), ipOf("1.1.1.1")); err == nil {
		t.Fatal("空响应体不应被当成成功")
	}
	if hits != 3 {
		t.Fatalf("空响应体应重试 3 次，实际 %d 次", hits)
	}

	// 业务码非 200 → 瞬时错误
	badCode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":500,"message":"busy","data":null}`))
	}))
	defer badCode.Close()

	q3 := NewQifu(3*time.Second, WithQifuEndpoint(badCode.URL), WithQifuRetries(0))
	if _, err := q3.Lookup(context.Background(), ipOf("1.1.1.1")); err == nil {
		t.Fatal("业务码 500 不应被当成成功")
	}
}
