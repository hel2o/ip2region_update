package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/legacy"
	"github.com/hel2o/ip2region/internal/store"
)

// runLegacy 把旧版 xdb 里更详细的境外数据补进当前库。
//
// 背景：新版 qqwry（2026-10）把境外记录大幅简化了，同一个 IP 从
// "日本|Shimane|松江市|||Enecom,Inc." 变成只有 "日本|Energia通讯"。
// 旧库那批省市信息不是数据源能再拿到的，只能从旧库里搬。
//
// 规则刻意保守：
//   - 只补空字段，绝不覆盖新库已有的值
//   - 默认只处理境外段（-include-cn 可放开，但新库国内数据明显更全，不建议）
//   - 补完会按旧库的边界切分原段，保证每片带自己那片的归属
func runLegacy(args []string) error {
	fs := newFlagSet("legacy", "用旧版 xdb 补当前库缺失的境外省市")
	dbPath := fs.String("db", "data/ip2region.db", "当前 SQLite 路径")
	xdbPath := fs.String("xdb", "build/compare/old.xdb", "旧版 xdb 路径")
	includeCN := fs.Bool("include-cn", false, "也处理中国大陆段（默认只补境外）")
	dry := fs.Bool("dry-run", false, "只统计不写库")
	_ = fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	t0 := time.Now()
	recs, err := legacy.Load(*xdbPath)
	if err != nil {
		return fmt.Errorf("读取旧库: %w", err)
	}
	idx := legacy.NewIndex(recs)
	log.Printf("旧库加载: %d 段（%s）", idx.Len(), time.Since(t0).Round(time.Millisecond))

	segs, err := db.LoadSegments()
	if err != nil {
		return err
	}
	log.Printf("当前库: %d 段", len(segs))

	var (
		touched, filledProv, filledCity, filledCounty, filledTown, filledISP int
		filledIP                                                             int64
		splitSegs                                                            int
	)
	t0 = time.Now()

	for i := range segs {
		sg := &segs[i]
		if !*includeCN && sg.Geo.Country == "中国" {
			continue
		}
		// 已完整就不动（省和市都有值说明够用了）
		if sg.Geo.Province != "" && sg.Geo.City != "" {
			continue
		}
		slices := idx.Range(sg.Start, sg.End)
		if len(slices) == 0 {
			continue
		}

		// 按旧库边界切，每片各自补空
		var pieces []geo.Segment
		changed := false
		for _, sl := range slices {
			g := sg.Geo
			old := sl.Geo()
			// 旧库是早年的纯真转出来的，境外字段里夹着 "CZ88.NET" 水印，
			// 不清掉就会被当成省市搬进新库
			cleanGeo(&old)
			before := g
			fillEmpty(&g, old)
			if g != before {
				changed = true
			}
			src := sg.Source
			if g != before {
				src = enrichTag(src, "+legacy")
			}
			pieces = append(pieces, geo.Segment{Start: sl.Start, End: sl.End, Geo: g, Source: src})
		}
		if !changed {
			continue
		}

		touched++
		filledIP += int64(sg.End) - int64(sg.Start) + 1
		for _, p := range pieces {
			if p.Geo.Province != sg.Geo.Province {
				filledProv++
			}
			if p.Geo.City != sg.Geo.City {
				filledCity++
			}
			if p.Geo.County != sg.Geo.County {
				filledCounty++
			}
			if p.Geo.Town != sg.Geo.Town {
				filledTown++
			}
			if p.Geo.ISP != sg.Geo.ISP {
				filledISP++
			}
		}

		if len(pieces) > 1 {
			splitSegs++
		}
		if *dry {
			continue
		}
		if len(pieces) == 1 && pieces[0].Start == sg.Start && pieces[0].End == sg.End {
			p := pieces[0]
			up := store.SegmentUpdate{Start: sg.Start, Source: p.Source}
			if p.Geo.Province != sg.Geo.Province {
				v := p.Geo.Province
				up.Province = &v
			}
			if p.Geo.City != sg.Geo.City {
				v := p.Geo.City
				up.City = &v
			}
			if p.Geo.County != sg.Geo.County {
				v := p.Geo.County
				up.County = &v
			}
			if p.Geo.Town != sg.Geo.Town {
				v := p.Geo.Town
				up.Town = &v
			}
			if p.Geo.ISP != sg.Geo.ISP {
				v := p.Geo.ISP
				up.ISP = &v
			}
			if err := db.UpdateSegments([]store.SegmentUpdate{up}); err != nil {
				return fmt.Errorf("更新段 %s: %w", geo.Uint2IP(sg.Start), err)
			}
			continue
		}
		if err := db.ReplaceSegment(sg.Start, sg.End, pieces); err != nil {
			return fmt.Errorf("切分段 %s: %w", geo.Uint2IP(sg.Start), err)
		}
	}

	fmt.Println("---- legacy 补丁统计 ----")
	fmt.Printf("处理的段        : %d（其中按旧库边界切开 %d 段）\n", touched, splitSegs)
	fmt.Printf("涉及 IP         : %d\n", filledIP)
	fmt.Printf("补省 / 补市     : %d / %d\n", filledProv, filledCity)
	fmt.Printf("补区县 / 补镇   : %d / %d\n", filledCounty, filledTown)
	fmt.Printf("补 ISP          : %d\n", filledISP)
	fmt.Printf("耗时            : %s\n", time.Since(t0).Round(time.Millisecond))
	if *dry {
		fmt.Println("（dry-run，未写库）")
	}
	return nil
}

// fillEmpty 只补空字段，不覆盖任何已有值。
func fillEmpty(g *geo.GeoInfo, old geo.GeoInfo) {
	if g.Country == "" {
		g.Country = old.Country
	}
	if g.Province == "" {
		g.Province = old.Province
	}
	if g.City == "" {
		g.City = old.City
	}
	if g.County == "" {
		g.County = old.County
	}
	if g.Town == "" {
		g.Town = old.Town
	}
	if g.ISP == "" {
		g.ISP = old.ISP
	}
}

// enrichTag 与 enrich.TagSource 同语义（避免 cmd 层引入循环依赖）。
func enrichTag(src, tag string) string {
	for i := 0; i+len(tag) <= len(src); i++ {
		if src[i:i+len(tag)] == tag {
			return src
		}
	}
	return src + tag
}

var _ = os.Stat
