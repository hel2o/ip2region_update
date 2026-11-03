package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/qqwry"
	"github.com/hel2o/ip2region/internal/regions"
	"github.com/hel2o/ip2region/internal/store"
)

// recordToSegment 把纯真原始记录转成 6 段 GeoInfo 段。
//
// 规则（阶段 0 定稿）：
//   - 国家串交给 regions.Split 拆 省/市/区县/乡镇
//   - 地区串若能识别为运营商 → ISP；否则并入乡镇（纯真的地区串常是子位置）
//   - 保留地址类串原样放进 Country，不拆
func recordToSegment(r qqwry.Record) geo.Segment {
	var g geo.GeoInfo
	switch {
	case isReserved(r.Country):
		g = geo.GeoInfo{Country: r.Country, ISP: "保留地址"}
	default:
		p := regions.Split(r.Country)
		g = geo.GeoInfo{
			Country:  p.Country,
			Province: p.Province,
			City:     p.City,
			County:   p.County,
			Town:     p.Town,
		}
		if isp, ok := regions.NormalizeISP(r.Area); ok {
			g.ISP = isp
		} else if r.Area != "" {
			// 地区串不是运营商：中国 IP 视为乡镇级补充，境外视为组织名
			if p.Country == "中国" {
				if g.Town == "" {
					g.Town = r.Area
				} else {
					g.Town = g.Town + "/" + r.Area
				}
			} else {
				g.ISP = r.Area
			}
		}
	}
	return geo.Segment{Start: r.Start, End: r.End, Geo: g, Source: "qqwry"}
}

func isReserved(s string) bool {
	for _, k := range []string{"保留地址", "局域网", "纯真网络", "IANA", "APNIC", "CNNIC", "RIPE", "ARIN"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func runParse(args []string) error {
	fs := newFlagSet("parse", "解析 qqwry.dat 并落库")
	src := fs.String("src", "data/qqwry.dat", "qqwry.dat 路径")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	_ = fs.Parse(args)

	start := time.Now()
	log.Printf("读取 %s ...", *src)
	records, err := qqwry.ParseFile(*src)
	if err != nil {
		return err
	}
	log.Printf("解析出 %d 条原始记录，耗时 %v", len(records), time.Since(start))

	segs := make([]geo.Segment, 0, len(records))
	for _, r := range records {
		sg := recordToSegment(r)
		if sg.Invalid() {
			return fmt.Errorf("非法区间 %d-%d (%s)", sg.Start, sg.End, sg.Geo.String())
		}
		segs = append(segs, sg)
	}

	log.Printf("规范化 ...")
	norm := geo.Normalize(segs)
	norm = geo.Compact(norm)
	if err := geo.Validate(norm); err != nil {
		return fmt.Errorf("规范化结果校验失败: %w", err)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.SaveSegments(norm); err != nil {
		return err
	}
	if err := db.SetMeta("qqwry_source", *src); err != nil {
		return err
	}
	if err := db.SetMeta("parsed_at", time.Now().Format(time.RFC3339)); err != nil {
		return err
	}

	printParseStats(norm, time.Since(start))
	return nil
}

func printParseStats(segs []geo.Segment, elapsed time.Duration) {
	var (
		cn        int // 中国
		cnIP      uint64
		cnNoProv  int // 中国但拆不出省（质检指标）
		noISP     int // 中国但无 ISP
		filled    int // 规范化时填充的缝隙段
		provCount = map[string]int{}
	)
	for _, s := range segs {
		switch {
		case s.Source == "fill":
			filled++
		case s.Geo.Country == "中国":
			cn++
			cnIP += s.Len()
			if s.Geo.Province == "" {
				cnNoProv++
			} else {
				provCount[s.Geo.Province]++
			}
			if s.Geo.ISP == "" {
				noISP++
			}
		}
	}
	fmt.Println("---- parse 统计 ----")
	fmt.Printf("规范化后段数        : %d\n", len(segs))
	fmt.Printf("覆盖 IP 总数        : %d (应等于 2^32)\n", geo.CountIPs(segs))
	fmt.Printf("中国段              : %d（约 %.2f 亿个 IP）\n", cn, float64(cnIP)/1e8)
	fmt.Printf("  拆不出省          : %d (%.2f%%)\n", cnNoProv, pct(cnNoProv, cn))
	fmt.Printf("  无 ISP            : %d (%.2f%%)\n", noISP, pct(noISP, cn))
	fmt.Printf("缝隙填充段          : %d\n", filled)
	fmt.Printf("耗时                : %v\n", elapsed)
	if len(provCount) > 0 {
		fmt.Println("省份分布（前 10）:")
		for i := 0; i < 10; i++ {
			best, n := "", 0
			for p, c := range provCount {
				if c > n {
					best, n = p, c
				}
			}
			if best == "" {
				break
			}
			fmt.Printf("  %-14s %d\n", best, n)
			delete(provCount, best)
		}
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}
