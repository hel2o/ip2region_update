package geo

import (
	"strings"
	"testing"
)

func seg(start, end uint32, s string) Segment {
	g, err := ParseGeo(s)
	if err != nil {
		panic(err)
	}
	return Segment{Start: start, End: end, Geo: g}
}

func TestNormalizeFillsGapsAndClips(t *testing.T) {
	in := []Segment{
		seg(10, 20, "中国|广东省|广州市|番禺区|钟村|中国电信"),
		seg(15, 30, "中国|广东省|广州市|天河区||中国电信"), // 与上段重叠
		seg(50, 60, "美国|||"), // 中间留空洞
	}
	got := Normalize(in)
	if err := Validate(got); err != nil {
		t.Fatalf("Normalize 结果未通过校验: %v", err)
	}
	if got[0].Start != 0 || got[0].Geo != UnknownGeo {
		t.Fatalf("首段应为 0.0.0.0 起的填充段，实际 %+v", got[0])
	}
	if CountIPs(got) != 1<<32 {
		t.Fatalf("覆盖 IP 数应为 2^32，实际 %d", CountIPs(got))
	}
	// 重叠按先到先得：10..20 保持番禺区，21..30 才轮到天河区
	if f := Find(got, 17); f == nil || f.Geo.County != "番禺区" {
		t.Fatalf("17 应保留番禺区，实际 %+v", f)
	}
	if f := Find(got, 25); f == nil || f.Geo.County != "天河区" {
		t.Fatalf("25 应落在天河区，实际 %+v", f)
	}
}

func TestApplyOverlayWins(t *testing.T) {
	base := Normalize([]Segment{seg(0, 0xFFFFFFFF, "中国|广东省|广州市|番禺区||中国电信")})
	over := []Segment{seg(100, 199, "中国|广东省|广州市|钟村||中国移动")}
	got := Apply(base, over)
	if err := Validate(got); err != nil {
		t.Fatalf("Apply 结果未通过校验: %v", err)
	}
	cases := map[uint32]string{
		99:  "中国电信",
		100: "中国移动",
		199: "中国移动",
		200: "中国电信",
	}
	for ip, wantISP := range cases {
		f := Find(got, ip)
		if f == nil || f.Geo.ISP != wantISP {
			t.Fatalf("%d 的 ISP 应为 %s，实际 %+v", ip, wantISP, f)
		}
	}
	if CountIPs(got) != 1<<32 {
		t.Fatalf("覆盖 IP 数应保持不变，实际 %d", CountIPs(got))
	}
}

func TestApplyOverlaySpanningMultipleBaseSegments(t *testing.T) {
	base := Normalize([]Segment{
		seg(0, 99, "中国|广东省|广州市|||中国电信"),
		seg(100, 199, "中国|广东省|深圳市|||中国电信"),
		seg(200, 299, "中国|广东省|东莞市|||中国电信"),
	})
	over := []Segment{seg(50, 250, "中国|广东省|广州市|番禺区|钟村|中国电信")}
	got := Apply(base, over)
	if err := Validate(got); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	for _, ip := range []uint32{50, 100, 200, 250} {
		f := Find(got, ip)
		if f == nil || f.Geo.Town != "钟村" {
			t.Fatalf("%d 应被覆盖为钟村，实际 %+v", ip, f)
		}
	}
	f := Find(got, 251)
	if f == nil || f.Geo.City != "东莞市" {
		t.Fatalf("251 应回落到东莞市，实际 %+v", f)
	}
}

func TestCompactMergesIdenticalNeighbours(t *testing.T) {
	s := "中国|广东省|广州市|番禺区|钟村|中国电信"
	in := []Segment{seg(0, 9, s), seg(10, 19, s), seg(20, 29, "中国|广东省|广州市|天河区||中国电信")}
	got := Compact(in)
	if len(got) != 2 {
		t.Fatalf("应合并为 2 段，实际 %d 段: %+v", len(got), got)
	}
	if got[0].Start != 0 || got[0].End != 19 {
		t.Fatalf("合并后首段应为 0-19，实际 %d-%d", got[0].Start, got[0].End)
	}
}

func TestValidateRejectsHoles(t *testing.T) {
	bad := []Segment{
		{Start: 0, End: 10},
		{Start: 12, End: MaxIP},
	}
	err := Validate(bad)
	if err == nil || !strings.Contains(err.Error(), "缝隙") {
		t.Fatalf("应报告缝隙，实际: %v", err)
	}
}

func TestGeoStringShape(t *testing.T) {
	g := GeoInfo{Country: "中国", Province: "广东省", City: "广州市", County: "番禺区", Town: "钟村", ISP: "中国电信"}
	want := "中国|广东省|广州市|番禺区|钟村|中国电信"
	if got := g.String(); got != want {
		t.Fatalf("String 输出不符:\n got=%s\nwant=%s", got, want)
	}
	back, err := ParseGeo(want)
	if err != nil {
		t.Fatal(err)
	}
	if back != g {
		t.Fatalf("解析回环失败:\n got=%+v\nwant=%+v", back, g)
	}
}

func TestSanitizeStripsPipeAndStar(t *testing.T) {
	g := GeoInfo{Country: "中国", Province: "*", City: "a|b", ISP: " 中国电信 "}
	got := g.String()
	if strings.Count(got, "|") != FieldCount-1 {
		t.Fatalf("竖线数量被污染: %s", got)
	}
	if strings.Contains(got, "*") {
		t.Fatalf("星号未清理: %s", got)
	}
}
