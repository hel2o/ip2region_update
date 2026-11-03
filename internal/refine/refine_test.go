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
			Geo: geo.GeoInfo{Country: "中国", Province: "天津市", City: "天津市", ISP: "腾讯云"}},
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
		if sg.Geo.Country == "中国" && sg.Geo.ISP != "腾讯云" {
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
