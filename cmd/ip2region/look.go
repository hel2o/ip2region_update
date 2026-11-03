package main

import (
	"fmt"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
)

func runLook(args []string) error {
	fs := newFlagSet("look", "从段表查询单个 IP 的归属")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("缺少 IP 参数，用法: look 183.3.149.185")
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
			fmt.Printf("%-16s %d-%d  %-60s [%s]\n",
				ipStr, sg.Start, sg.End, sg.Geo.String(), sg.Source)
		} else {
			fmt.Printf("%-16s 未命中\n", ipStr)
		}
	}
	return nil
}
