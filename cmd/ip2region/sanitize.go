package main

import (
	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/regions"
)

// cleanGeo 去掉 GeoInfo 六段里的水印和重复层级，返回是否有改动。
//
// parse 阶段已经清洗过水印，这里再兜一层：
// enrich / legacy / 自定义覆盖都可能从旧数据里把水印又带进来。
//
// 重复层级的处理规则是「同名时清掉市」：
//   - 直辖市/特别行政区：省=北京市、市=北京市 → 市是冗余（行政区划上就不存在地级市）
//   - 市与区县同名（洛杉矶市 / 洛杉矶县、东莞不设区）：保留更精确的区县
//
// 省和区县都不动，因为它们一个是最粗粒度、一个是最细粒度，清掉丢的信息更多。
func cleanGeo(g *geo.GeoInfo) bool {
	before := *g
	g.Country = regions.StripWatermark(g.Country)
	g.Province = regions.StripWatermark(g.Province)
	g.City = regions.StripWatermark(g.City)
	g.County = regions.StripWatermark(g.County)
	g.Town = regions.StripWatermark(g.Town)
	g.ISP = regions.StripWatermark(g.ISP)

	if g.City != "" && (g.City == g.Province || g.City == g.County) {
		g.City = ""
	}
	// 区县与省同名（"德国|汉堡|汉堡|汉堡" 这种三层全同名）时，省优先
	if g.County != "" && g.County == g.Province {
		g.County = ""
	}
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
