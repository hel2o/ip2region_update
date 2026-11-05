package enrich

import (
	"path/filepath"
	"testing"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
)

func mkDB(t *testing.T, path string, segs []geo.Segment) {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	if err := db.SaveSegments(segs); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func mkSeg(start, end uint32, prov, city, isp string) geo.Segment {
	return geo.Segment{
		Start: start, End: end, Source: "qqwry",
		Geo: geo.GeoInfo{Country: "中国", Province: prov, City: city, ISP: isp},
	}
}

func TestSeedFromDB(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.db")
	mkDB(t, oldPath, []geo.Segment{
		mkSeg(100, 199, "台湾省", "桃园市", "中华电信"),
		mkSeg(200, 299, "广东省", "深圳市", "中国电信"),
		mkSeg(300, 399, "浙江省", "杭州市", "中国电信"),
	})

	segs := []geo.Segment{
		mkSeg(100, 199, "台湾省", "", "中华电信"),    // 省在、市空 → 补市
		mkSeg(200, 299, "", "", "中国电信"),       // 省市都空 → 都补
		mkSeg(300, 399, "江苏省", "", "中国电信"),    // 省冲突 → 不动
		mkSeg(400, 499, "广东省", "广州市", "中国电信"), // 已有值 → 不动
	}
	ups, fp, fc, err := SeedFromDB(segs, oldPath)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if fp != 1 {
		t.Fatalf("应补省 1 段，实际 %d", fp)
	}
	if fc != 2 {
		t.Fatalf("应补市 2 段，实际 %d", fc)
	}
	if len(ups) != 2 {
		t.Fatalf("应产生 2 条落库更新，实际 %d", len(ups))
	}
	if segs[0].Geo.City != "桃园市" {
		t.Fatalf("台湾段市应补为桃园市，实际 %q", segs[0].Geo.City)
	}
	if segs[1].Geo.Province != "广东省" || segs[1].Geo.City != "深圳市" {
		t.Fatalf("空省市段应补为广东省/深圳市，实际 %q/%q", segs[1].Geo.Province, segs[1].Geo.City)
	}
	if segs[2].Geo.City != "" {
		t.Fatalf("省冲突段不应补市，实际 %q", segs[2].Geo.City)
	}
	if segs[3].Geo.City != "广州市" {
		t.Fatalf("已有值不应被改，实际 %q", segs[3].Geo.City)
	}
}
