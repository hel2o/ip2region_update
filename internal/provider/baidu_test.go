package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
)

func TestBaiduLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource_id") != "6006" {
			t.Errorf("缺少 resource_id: %s", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("query") {
		case "183.3.149.185":
			_, _ = w.Write([]byte(`{"status":"0","data":[{"location":"广东省广州市 电信"}]}`))
		case "8.8.8.8":
			_, _ = w.Write([]byte(`{"status":"0","data":[{"location":"美国"}]}`))
		case "39.136.55.14":
			_, _ = w.Write([]byte(`{"status":"0","data":[{"location":"中国 移动"}]}`))
		case "1.2.3.4":
			_, _ = w.Write([]byte(`{"status":"0","data":[]}`))
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	b := NewBaidu(3*time.Second, WithBaiduEndpoint(srv.URL), WithBaiduRetries(0))
	ctx := context.Background()

	res, err := b.Lookup(ctx, ipOf("183.3.149.185"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Country != "中国" || res.Province != "广东省" || res.City != "广州市" || res.ISP != "中国电信" {
		t.Fatalf("结果不符: %+v", res)
	}

	res, err = b.Lookup(ctx, ipOf("8.8.8.8"))
	if err != nil || res.Country != "美国" || res.Province != "" {
		t.Fatalf("境外结果不符: %+v err=%v", res, err)
	}

	res, err = b.Lookup(ctx, ipOf("39.136.55.14"))
	if err != nil || res.Country != "中国" || res.Province != "" || res.ISP != "中国移动" {
		t.Fatalf("仅国家+ISP 结果不符: %+v err=%v", res, err)
	}

	if _, err = b.Lookup(ctx, ipOf("1.2.3.4")); !errors.Is(err, ErrNoData) {
		t.Fatalf("空数据应返回 ErrNoData，实际 %v", err)
	}
	if _, err = b.Lookup(ctx, ipOf("9.9.9.9")); err == nil || IsCacheable(err) {
		t.Fatalf("5xx 不应被判为可缓存，实际 %v", err)
	}
}

func TestBaiduBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "too many", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	b := NewBaidu(3*time.Second, WithBaiduEndpoint(srv.URL), WithBaiduRetries(2))
	start := time.Now()
	_, err := b.Lookup(context.Background(), ipOf("1.1.1.1"))
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("429 应返回 ErrBlocked，实际 %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("429 不应重试退避，耗时 %v", time.Since(start))
	}
}

func ipOf(s string) uint32 { return geo.MustIP2Uint(s) }
