package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withAmapServer 用临时服务替换高德端点。
func withAmapServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	old := amapBaseURL
	amapBaseURL = srv.URL
	t.Cleanup(func() {
		amapBaseURL = old
		srv.Close()
	})
}

func TestAmapLookup(t *testing.T) {
	withAmapServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Errorf("key 未透传: %q", got)
		}
		switch r.URL.Query().Get("ip") {
		case "111.55.212.179":
			_, _ = w.Write([]byte(`{"status":"1","info":"OK","infocode":"10000","province":"广东省","city":"深圳市","adcode":"440300","rectangle":"1;2;3;4"}`))
		case "1.2.3.4":
			// 高德无数据时 province/city 返回空数组，不是空串
			_, _ = w.Write([]byte(`{"status":"1","info":"OK","infocode":"10000","province":[],"city":[],"adcode":[]}`))
		case "192.168.1.1":
			_, _ = w.Write([]byte(`{"status":"1","info":"OK","infocode":"10000","province":"局域网","city":[],"adcode":[]}`))
		default:
			_, _ = w.Write([]byte(`{"status":"0","info":"DAILY_QUERY_OVER_LIMIT","infocode":"10003"}`))
		}
	})

	a := NewAmap("test-key", 5*time.Second)
	if a == nil {
		t.Fatal("应创建成功")
	}

	// 正常：省市都拿到
	res, err := a.Lookup(context.Background(), ipOf("111.55.212.179"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Province != "广东省" || res.City != "深圳市" {
		t.Fatalf("解析错误: %+v", res)
	}
	if res.Country != "中国" {
		t.Fatalf("应带国家: %+v", res)
	}

	// 无数据：[] 不能被当成解析失败
	res, err = a.Lookup(context.Background(), ipOf("1.2.3.4"))
	if err != nil {
		t.Fatalf("空数组不该报错: %v", err)
	}
	if res.Province != "" || res.City != "" {
		t.Fatalf("应为空: %+v", res)
	}

	// 局域网
	res, err = a.Lookup(context.Background(), ipOf("192.168.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Province != "" {
		t.Fatalf("局域网不该当省份: %+v", res)
	}

	// 配额超限要判成 ErrBlocked，避免继续烧配额
	_, err = a.Lookup(context.Background(), ipOf("9.9.9.9"))
	if err == nil {
		t.Fatal("配额超限应报错")
	}
	if !strings.Contains(err.Error(), "blocked") && err.Error() == "" {
		t.Fatalf("应为 ErrBlocked: %v", err)
	}
}

func TestNewAmapEmptyKey(t *testing.T) {
	if NewAmap("", time.Second) != nil {
		t.Fatal("空 key 应返回 nil")
	}
	if NewAmap("   ", time.Second) != nil {
		t.Fatal("空白 key 应返回 nil")
	}
}
