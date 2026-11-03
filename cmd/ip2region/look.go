package main

import (
	"fmt"
	"strings"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
	"github.com/hel2o/ip2region/internal/xdbmaker"
)

// runLook 查询单个 IP 的归属，调试用。
// 默认走 SQLite 殆表；指定 --xdb 时直接查 xdb 文件（走官方 searcher）。
func runLook(args []string) error {
	fs := newFlagSet("look", "查询单个 IP 的归属")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	xdb := fs.String("xdb", "", "直接查询 xdb 文件（优先于 --db）")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("缺少 IP 参数，用法: look 183.3.149.185")
	}

	if *xdb != "" {
		return lookXdb(*xdb, fs.Args())
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	segs, err := db.LoadSegments()
	if err != nil {
		return err
	}

	for _, ipStr := range fs.Args() {
		v, ok := geo.IP2Uint(ipStr)
		if !ok {
			return fmt.Errorf("非法 IPv4: %s", ipStr)
		}
		if sg := geo.Find(segs, v); sg != nil {
			fmt.Printf("%-16s %s-%s  %-60s [%s]\n",
				ipStr, geo.Uint2IP(sg.Start), geo.Uint2IP(sg.End), sg.Geo.String(), sg.Source)
		} else {
			fmt.Printf("%-16s 未命中\n", ipStr)
		}
	}
	return nil
}

// lookXdb 用官方 searcher 查询 xdb，region 按 6 段解析展示。
func lookXdb(path string, ips []string) error {
	s, err := xdbmaker.NewWithFileOnly(path)
	if err != nil {
		return fmt.Errorf("打开 xdb: %w", err)
	}
	defer s.Close()

	for _, ipStr := range ips {
		region, err := s.SearchByStr(ipStr)
		if err != nil {
			return fmt.Errorf("查询 %s: %w", ipStr, err)
		}
		fmt.Printf("%-16s %s\n", ipStr, strings.ReplaceAll(region, "|", " | "))
	}
	return nil
}
