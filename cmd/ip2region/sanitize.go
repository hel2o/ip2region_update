package main

import (
	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/regions"
)

// cleanGeo 去掉 GeoInfo 六段里的 CZ88 水印，返回是否有改动。
//
// parse 阶段已经清洗过源头，这里再兜一层：
// enrich / legacy / 自定义覆盖都可能从旧数据里把水印又带进来。
func cleanGeo(g *geo.GeoInfo) bool {
	before := *g
	g.Country = regions.StripWatermark(g.Country)
	g.Province = regions.StripWatermark(g.Province)
	g.City = regions.StripWatermark(g.City)
	g.County = regions.StripWatermark(g.County)
	g.Town = regions.StripWatermark(g.Town)
	g.ISP = regions.StripWatermark(g.ISP)
	return *g != before
}

// sanitizeSegments 就地清洗全部段，返回被改动的段数。
func sanitizeSegments(segs []geo.Segment) int {
	n := 0
	for i := range segs {
		if cleanGeo(&segs[i].Geo) {
			n++
		}
	}
	return n
}

// countWatermarks 统计还带 CZ88 水印的段数（verify 用来防回归）。
func countWatermarks(segs []geo.Segment) int {
	n := 0
	for _, s := range segs {
		if regions.HasWatermark(s.Geo.String()) {
			n++
		}
	}
	return n
}
