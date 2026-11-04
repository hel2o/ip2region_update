package refine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/provider"
	"github.com/hel2o/ip2region/internal/store"
)

// fakeProvider 按 IP 高低返回不同城市，模拟"一个大段跨多个城市"。
type fakeProvider struct {
	split uint32
	a, b  provider.Result
	calls int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Lookup(_ context.Context, ip uint32) (provider.Result, error) {
	f.calls++
	if ip < f.split {
		return f.a, nil
	}
	return f.b, nil
}

func openTempDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRefineSplitsBigSegment(t *testing.T) {
	db := openTempDB(t)
	segs := []geo.Segment{
		{Start: 0, End: 0x00FFFFFF, Geo: geo.UnknownGeo, Source: "fill"},
		{Start: 0x01000000, End: 0x0100FFFF, Source: "qqwry",
			Geo: geo.GeoInfo{Country: "中国", Province: "天津市", City: "天津市", ISP: "中国联通"}},
		{Start: 0x01010000, End: 0xFFFFFFFF, Geo: geo.UnknownGeo, Source: "fill"},
	}
	if err := db.SaveSegments(segs); err != nil {
		t.Fatal(err)
	}

	fp := &fakeProvider{
		split: 0x01008000,
		a:     provider.Result{Province: "北京市", City: "北京市", Raw: "北京市 电信"},
		b:     provider.Result{Province: "辽宁省", City: "大连市", Raw: "辽宁省大连市 电信"},
	}

	st, err := Run(context.Background(), db, fp, segs, Options{
		Threshold: 4096, MaxDepth: 2, QPS: 1000, Workers: 1, ProgressN: 1000,
	})
	if err != nil {
		t.Fatalf("refine 失败: %v", err)
	}
	if st.SplitSegs != 1 {
		t.Fatalf("应有 1 个段被细分，实际 %d（统计 %+v）", st.SplitSegs, st)
	}

	got, err := db.LoadSegments()
	if err != nil {
		t.Fatal(err)
	}
	if err := geo.Validate(got); err != nil {
		t.Fatalf("细分后段表不连续: %v", err)
	}

	// 首片应为北京，末片应为大连，且 ISP 保留腾讯云
	first := geo.Find(got, 0x01000000)
	last := geo.Find(got, 0x0100FFFF)
	if first == nil || first.Geo.Province != "北京市" {
		t.Fatalf("起始片应是北京市，实际 %+v", first)
	}
	if last == nil || last.Geo.Province != "辽宁省" || last.Geo.City != "大连市" {
		t.Fatalf("末尾片应是大连市，实际 %+v", last)
	}
	for _, sg := range got {
		if sg.Geo.Country == "中国" && sg.Geo.ISP != "中国联通" {
			t.Fatalf("ISP 不该被覆盖: %+v", sg)
		}
	}
	if fp.calls == 0 {
		t.Fatal("没有发起探测")
	}
}

func TestRefineLeavesUniformSegmentAlone(t *testing.T) {
	db := openTempDB(t)
	segs := []geo.Segment{
		{Start: 0, End: 0x00FFFFFF, Geo: geo.UnknownGeo, Source: "fill"},
		{Start: 0x01000000, End: 0x0100FFFF, Source: "qqwry",
			Geo: geo.GeoInfo{Country: "中国", Province: "北京市", City: "北京市", ISP: "中国电信"}},
		{Start: 0x01010000, End: 0xFFFFFFFF, Geo: geo.UnknownGeo, Source: "fill"},
	}
	if err := db.SaveSegments(segs); err != nil {
		t.Fatal(err)
	}

	fp := &fakeProvider{
		split: 0,
		a:     provider.Result{Province: "北京市", City: "北京市", Raw: "北京市 电信"},
		b:     provider.Result{Province: "北京市", City: "北京市", Raw: "北京市 电信"},
	}
	st, err := Run(context.Background(), db, fp, segs, Options{
		Threshold: 4096, MaxDepth: 2, QPS: 1000, Workers: 1, ProgressN: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.SplitSegs != 0 || st.Confirmed != 1 {
		t.Fatalf("三点一致应整段确认且不细分: %+v", st)
	}
}

func TestRefineCloudSegmentNotOverridden(t *testing.T) {
	db := openTempDB(t)
	segs := []geo.Segment{
		{Start: 0, End: 0x00FFFFFF, Geo: geo.UnknownGeo, Source: "fill"},
		{Start: 0x01000000, End: 0x0100FFFF, Source: "qqwry",
			Geo: geo.GeoInfo{Country: "中国", Province: "广东省", City: "广州市", ISP: "腾讯云"}},
		{Start: 0x01010000, End: 0xFFFFFFFF, Geo: geo.UnknownGeo, Source: "fill"},
	}
	if err := db.SaveSegments(segs); err != nil {
		t.Fatal(err)
	}

	fp := &fakeProvider{
		split: 0x01008000,
		a:     provider.Result{Province: "北京市", City: "北京市"},
		b:     provider.Result{Province: "吉林省", City: "长春市"},
	}
	if _, err := Run(context.Background(), db, fp, segs, Options{
		Threshold: 4096, MaxDepth: 1, QPS: 1000, Workers: 1, ProgressN: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadSegments()
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range got {
		if sg.Geo.Country != "中国" {
			continue
		}
		if sg.Geo.Province != "广东省" || sg.Geo.City != "广州市" {
			t.Fatalf("云类段不该被百度覆盖: %+v", sg)
		}
	}
}

func TestApplyToKeepsCountyAndFillsISP(t *testing.T) {
	old := geo.GeoInfo{Country: "中国", Province: "广东省", City: "广州", County: "番禺区", Town: "钟村"}
	out := applyTo(provider.Result{Province: "广东省", City: "广州市", ISP: "中国联通"}, old)
	if out.County != "番禺区" || out.Town != "钟村" {
		t.Fatalf("区镇必须保留: %+v", out)
	}
	if out.ISP != "中国联通" {
		t.Fatalf("空 ISP 应被填充: %+v", out)
	}
}

// copyProvider 按 IP 区间返回不同城市，用于验证递归收敛。
// ranges 必须按 start 升序。
type copyProvider struct {
	ranges []rangeGeo
	calls  map[uint32]int
}

type rangeGeo struct {
	start, end uint32
	res        provider.Result
}

func (p *copyProvider) Name() string { return "copy" }

func (p *copyProvider) Lookup(_ context.Context, ip uint32) (provider.Result, error) {
	if p.calls == nil {
		p.calls = map[uint32]int{}
	}
	p.calls[ip]++
	for _, r := range p.ranges {
		if ip >= r.start && ip <= r.end {
			return r.res, nil
		}
	}
	return provider.Result{Raw: "中国"}, nil
}

// TestRefineConvergesToBoundary 验证"首中一致、尾不同"时只往右半收敛，
// 且能把边界定位到 MinSpan 以内。
func TestRefineConvergesToBoundary(t *testing.T) {
	db := openTempDB(t)
	// 一个 /16：前 3/4 是北京，后 1/4 是大连
	const segStart, segEnd = 0x01000000, 0x0100FFFF
	const boundary = 0x0100C000 // 3/4 处
	segs := []geo.Segment{
		{Start: 0, End: segStart - 1, Geo: geo.UnknownGeo, Source: "fill"},
		{Start: segStart, End: segEnd, Source: "qqwry",
			Geo: geo.GeoInfo{Country: "中国", Province: "天津市", City: "天津市", ISP: "中国联通"}},
		{Start: segEnd + 1, End: 0xFFFFFFFF, Geo: geo.UnknownGeo, Source: "fill"},
	}
	if err := db.SaveSegments(segs); err != nil {
		t.Fatal(err)
	}

	p := &copyProvider{ranges: []rangeGeo{
		{segStart, boundary - 1, provider.Result{Province: "北京市", City: "北京市", Raw: "北京市 电信"}},
		{boundary, segEnd, provider.Result{Province: "辽宁省", City: "大连市", Raw: "辽宁省大连市 电信"}},
	}}

	st, err := Run(context.Background(), db, p, segs, Options{
		Threshold: 4096, MinSpan: 256, QPS: 10000, Workers: 1, ProgressN: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.LoadSegments()
	if err != nil {
		t.Fatal(err)
	}
	var bj, dl []geo.Segment
	for _, sg := range got {
		if sg.Start < segStart || sg.End > segEnd {
			continue
		}
		switch sg.Geo.Province {
		case "北京市":
			bj = append(bj, sg)
		case "辽宁省":
			dl = append(dl, sg)
		}
	}
	if len(bj) == 0 || len(dl) == 0 {
		t.Fatalf("应同时出现北京和大连两片，实际 北京%d 大连%d", len(bj), len(dl))
	}
	// 边界精度：北京片的最大 IP 与真实边界相差不超过 MinSpan
	lastBJ := bj[len(bj)-1].End
	diff := int64(boundary) - int64(lastBJ+1)
	if diff < 0 {
		diff = -diff
	}
	if diff > 256 {
		t.Fatalf("边界定位误差 %d 个 IP，超过 MinSpan=256（北京末 IP=%x，真实边界=%x）",
			diff, lastBJ, boundary)
	}
	// 收敛应该比均匀采样省请求：/16 均匀到 /24 需要 256 点
	if st.Requests > 64 {
		t.Fatalf("收敛用 %d 次请求，超过预期的 64 次", st.Requests)
	}
	t.Logf("细分 %d 段，请求 %d 次，缓存 %d 次，边界误差 %d IP",
		st.SplitSegs, st.Requests, st.CacheHits, diff)
}
