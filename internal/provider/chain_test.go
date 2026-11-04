package provider

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider 用于测试回退逻辑。
type fakeProvider struct {
	name    string
	res     Result
	err     error
	calls   int
	byIP    map[uint32]Result
	errByIP map[uint32]error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Lookup(_ context.Context, ip uint32) (Result, error) {
	f.calls++
	if f.errByIP != nil {
		if err, ok := f.errByIP[ip]; ok {
			return Result{}, err
		}
	}
	if f.byIP != nil {
		if r, ok := f.byIP[ip]; ok {
			return r, nil
		}
	}
	return f.res, f.err
}

func TestChainFallback(t *testing.T) {
	ctx := context.Background()
	ip := ipOf("1.2.3.4")

	// 1. 主源成功 → 不碰备用源
	sec := &fakeProvider{name: "baidu", res: Result{Province: "备用省"}}
	c := NewChain(&fakeProvider{name: "qifu", res: Result{Province: "主省"}}, sec)
	res, err := c.Lookup(ctx, ip)
	if err != nil || res.Province != "主省" || sec.calls != 0 {
		t.Fatalf("主源成功时不应调备用源: res=%+v err=%v calls=%d", res, err, sec.calls)
	}

	// 2. 主源瞬时失败 → 用备用源
	sec = &fakeProvider{name: "baidu", res: Result{Province: "备用省"}}
	c = NewChain(&fakeProvider{name: "qifu", err: errors.New("超时")}, sec)
	res, err = c.Lookup(ctx, ip)
	if err != nil || res.Province != "备用省" || sec.calls != 1 {
		t.Fatalf("主源失败应回退备用源: res=%+v err=%v", res, err)
	}

	// 3. 主源无数据 → 备用源有数据，采用备用源
	sec = &fakeProvider{name: "baidu", res: Result{Province: "备用省"}}
	c = NewChain(&fakeProvider{name: "qifu", err: ErrNoData}, sec)
	res, err = c.Lookup(ctx, ip)
	if err != nil || res.Province != "备用省" {
		t.Fatalf("主源无数据应回退备用源: res=%+v err=%v", res, err)
	}
}

func TestChainErrorArbitration(t *testing.T) {
	ctx := context.Background()
	ip := ipOf("1.2.3.4")

	// 两边都无数据 → ErrNoData（可缓存）
	c := NewChain(&fakeProvider{name: "qifu", err: ErrNoData}, &fakeProvider{name: "baidu", err: ErrNoData})
	if _, err := c.Lookup(ctx, ip); !errors.Is(err, ErrNoData) {
		t.Fatalf("两边都无数据应为 ErrNoData，实际 %v", err)
	}
	if !IsCacheable(ErrNoData) {
		t.Fatal("ErrNoData 应可缓存")
	}

	// 主源无数据 + 备用源网络错误 → 仍按无数据缓存（避免重跑再打一遍）
	c = NewChain(&fakeProvider{name: "qifu", err: ErrNoData}, &fakeProvider{name: "baidu", err: errors.New("网络错误")})
	if _, err := c.Lookup(ctx, ip); !errors.Is(err, ErrNoData) {
		t.Fatalf("应为 ErrNoData，实际 %v", err)
	}

	// 只有备用源被封 → 不能向上抛 ErrBlocked（否则整轮白停）
	c = NewChain(&fakeProvider{name: "qifu", err: errors.New("超时")}, &fakeProvider{name: "baidu", err: ErrBlocked})
	if _, err := c.Lookup(ctx, ip); errors.Is(err, ErrBlocked) {
		t.Fatal("仅备用源被封不应中断整轮")
	}

	// 两边都被封 → ErrBlocked，上层停机
	c = NewChain(&fakeProvider{name: "qifu", err: ErrBlocked}, &fakeProvider{name: "baidu", err: ErrBlocked})
	if _, err := c.Lookup(ctx, ip); !errors.Is(err, ErrBlocked) {
		t.Fatalf("两边都被封应为 ErrBlocked，实际 %v", err)
	}
}

func TestChainPrimaryDownIsSticky(t *testing.T) {
	ctx := context.Background()
	prim := &fakeProvider{name: "qifu", err: ErrBlocked}
	sec := &fakeProvider{name: "baidu", res: Result{Province: "备用省"}}
	c := NewChain(prim, sec)

	if _, err := c.Lookup(ctx, ipOf("1.1.1.1")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Lookup(ctx, ipOf("1.1.1.2")); err != nil {
		t.Fatal(err)
	}
	// 主源被封一次后应永久让位，不再重试
	if prim.calls != 1 {
		t.Fatalf("主源被封后不应再被调用，实际调用 %d 次", prim.calls)
	}
	if sec.calls != 2 {
		t.Fatalf("备用源应被调用 2 次，实际 %d 次", sec.calls)
	}
}

func TestChainCacheNames(t *testing.T) {
	c := NewChain(&fakeProvider{name: "qifu"}, &fakeProvider{name: "baidu"})
	names := CacheNames(c)
	want := map[string]bool{"qifu+baidu": true, "qifu": true, "baidu": true}
	if len(names) != len(want) {
		t.Fatalf("缓存名数量不符: %v", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("意外的缓存名 %q", n)
		}
	}

	// 单 provider 退化行为
	single := &fakeProvider{name: "amap"}
	if got := CacheNames(single); len(got) != 1 || got[0] != "amap" {
		t.Fatalf("单源 CacheNames 不符: %v", got)
	}
}
