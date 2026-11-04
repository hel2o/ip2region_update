package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/regions"
	"github.com/hel2o/ip2region/internal/store"
	"github.com/hel2o/ip2region/internal/xdbmaker"
)

// runVerify 校验段表质量：
//   - 基础校验：SQLite 段表连续性 / 全覆盖 / 归属块统计
//   - golden 对拍（--golden 指向存在的文件时）：随机抽样 N 个 IP，
//     以 国家 + 省（短名）粒度对比新旧 xdb，输出一致率与分歧样例
func runVerify(args []string) error {
	fs := newFlagSet("verify", "校验段表连续性/全覆盖，并与 golden xdb 抽样对拍")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	golden := fs.String("golden", "testdata/golden/ip2region_20250721_qqwry.xdb", "golden xdb 路径（不存在则跳过对拍）")
	n := fs.Int("n", 20000, "对拍抽样 IP 数")
	sample := fs.Bool("sample", true, "打印抽样查询结果")
	_ = fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	segs, err := db.LoadSegments()
	if err != nil {
		return err
	}
	if err := geo.Validate(segs); err != nil {
		return fmt.Errorf("校验失败: %w", err)
	}

	fmt.Println("---- verify 通过 ----")
	fmt.Printf("段数                : %d\n", len(segs))
	fmt.Printf("覆盖 IP 总数        : %d\n", geo.CountIPs(segs))

	prev := segs[0].Geo
	blocks := 1
	for _, s := range segs[1:] {
		if s.Geo != prev {
			blocks++
			prev = s.Geo
		}
	}
	fmt.Printf("归属块数            : %d\n", blocks)

	// 纯真免费版的水印不该出现在成品里，出现了说明有路径绕过清洗
	if n := countWatermarks(segs); n > 0 {
		fmt.Printf("!! CZ88 水印残留    : %d 段（merge 时会自动清除，建议重跑 merge+build）\n", n)
	}
	// 省==市 在段表里是正常状态（直辖市/特区，enrich 比层级时要用），
	// 去重发生在 merge 出口，所以这里只提示不告警。
	if n := countDuplicateLevels(segs); n > 0 {
		fmt.Printf("段表同名层级        : %d 段（直辖市/特区等，merge 输出时自动去重）\n", n)
	}

	if *sample {
		testIPs := []string{
			"0.0.0.1", "1.0.1.0", "8.8.8.8", "114.114.114.114",
			"183.3.149.185", "223.5.5.5", "255.255.255.254",
		}
		fmt.Println("抽样:")
		for _, ipStr := range testIPs {
			v, ok := geo.IP2Uint(ipStr)
			if !ok {
				continue
			}
			sg := geo.Find(segs, v)
			if sg == nil {
				fmt.Printf("  %-16s 未命中（不应出现）\n", ipStr)
				continue
			}
			fmt.Printf("  %-16s %-16s %-60s [%s]\n",
				ipStr, fmt.Sprintf("%s-%s", geo.Uint2IP(sg.Start), geo.Uint2IP(sg.End)),
				sg.Geo.String(), sg.Source)
		}
	}

	if fi, err := os.Stat(*golden); err == nil && !fi.IsDir() {
		return compareGolden(segs, *golden, *n)
	}
	fmt.Println("（未找到 golden xdb，跳过对拍）")
	return nil
}

// normGolden 把 golden 的原始 region 串归一成 (国家, 省短名)。
// golden 串形如 "国家–省–市–区|组织"，如 "中国–山东–济南|教育网/山东女子学院"、
// "中国|CNNIC权威云解析"（无省市）、"俄罗斯|Akamai节点"。
func normGolden(region string) (country, provShort string) {
	loc, _, _ := strings.Cut(region, "|")
	segs := strings.Split(loc, regions.Dash)
	if len(segs) == 0 || segs[0] == "" {
		return "", ""
	}
	if segs[0] == "中国" {
		if len(segs) > 1 {
			return "中国", segs[1]
		}
		return "中国", ""
	}
	if _, ok := regions.ProvinceFromShort(segs[0]); ok {
		return "中国", segs[0]
	}
	return segs[0], ""
}

func compareGolden(segs []geo.Segment, goldenPath string, n int) error {
	gs, err := xdbmaker.NewWithFileOnly(goldenPath)
	if err != nil {
		return fmt.Errorf("打开 golden xdb: %w", err)
	}
	defer gs.Close()

	fmt.Printf("\n---- golden 对拍（%d 个随机 IP）----\n", n)
	var (
		matchCountry, matchProv, bothCN int
		provFilled, provConflict        int // 旧空→新有值 / 两边都有值但不同
		mismatches, provDiffs           []string
	)
	for i := 0; i < n; i++ {
		ip := rand.Uint32()
		sg := geo.Find(segs, ip)
		if sg == nil {
			return fmt.Errorf("抽样 IP %s 未命中段表（全覆盖被破坏）", geo.Uint2IP(ip))
		}
		gRegion, err := gs.Search(ip)
		if err != nil {
			return fmt.Errorf("golden 查询 %s: %w", geo.Uint2IP(ip), err)
		}

		gCountry, gProv := normGolden(gRegion)
		mCountry, mProv := sg.Geo.Country, regions.ProvinceShort(sg.Geo.Province)

		cOK := mCountry == gCountry
		pOK := mCountry == "中国" && gCountry == "中国" && mProv == gProv
		if cOK {
			matchCountry++
		}
		if cOK && pOK {
			matchProv++
		}
		if mCountry == "中国" && gCountry == "中国" {
			bothCN++
			switch {
			case pOK:
			case gProv == "" && mProv != "":
				provFilled++ // 旧库空、我们补上了 —— 这是改进，不是分歧
			case gProv != "" && mProv == "":
				provDiffs = append(provDiffs, fmt.Sprintf(
					"%-15s 新省: <空>  旧省: %-12q 旧串: %s", geo.Uint2IP(ip), gProv, gRegion))
			default:
				provConflict++
				if len(provDiffs) < 15 {
					provDiffs = append(provDiffs, fmt.Sprintf(
						"%-15s 新省: %-12q 旧省: %-12q 旧串: %s", geo.Uint2IP(ip), mProv, gProv, gRegion))
				}
			}
		}
		if !cOK && len(mismatches) < 10 {
			mismatches = append(mismatches, fmt.Sprintf(
				"%-15s 新: %-12s 旧: %s", geo.Uint2IP(ip), sg.Geo.Country, gRegion))
		}
	}

	fmt.Printf("国家一致率          : %.2f%%\n", pct2(matchCountry, n))
	fmt.Printf("省级一致率          : %.2f%%（按双方均判为中国的 %d 个 IP）\n", pct2(matchProv, bothCN), bothCN)
	fmt.Printf("  其中 旧库为空/新库补上: %d（%.2f%%，属改进）\n", provFilled, pct2(provFilled, bothCN))
	fmt.Printf("  其中 双方都有值但不同  : %d（%.2f%%，需人工抽查）\n", provConflict, pct2(provConflict, bothCN))
	if len(provDiffs) > 0 {
		fmt.Println("省级样例（前 15）:")
		for _, m := range provDiffs {
			fmt.Println("  " + m)
		}
	}
	if len(mismatches) > 0 {
		fmt.Println("国家分歧样例（前 10）:")
		for _, m := range mismatches {
			fmt.Println("  " + m)
		}
	}
	return nil
}

func pct2(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}
