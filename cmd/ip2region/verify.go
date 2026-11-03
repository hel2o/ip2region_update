package main

import (
	"fmt"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
)

// runVerify 校验段表质量。阶段 1 校验 SQLite 段表；
// txt 模式与 golden xdb 对拍在阶段 2/5 接入。
func runVerify(args []string) error {
	fs := newFlagSet("verify", "校验段表连续性与全覆盖")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
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
	return nil
}
