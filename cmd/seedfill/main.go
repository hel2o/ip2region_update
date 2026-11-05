// seedfill 用上一轮库回填当前段表的空省市。
//
// 用途：enrich 的 -seed-db 是在流水线内自动做的；这个命令用于两种情况：
//   - 跑完才发现回填没生效（旧版本二进制）
//   - 想对历史库做一次性修补
//
//	go run ./cmd/seedfill -db data/ip2region.db -seed build/backup/prev.db
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/hel2o/ip2region/internal/enrich"
	"github.com/hel2o/ip2region/internal/store"
)

func main() {
	dbPath := flag.String("db", "data/ip2region.db", "当前库")
	seed := flag.String("seed", "build/backup/prev.db", "上一轮库（提供省市）")
	flag.Parse()

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开库: %v", err)
	}
	defer db.Close()
	segs, err := db.LoadSegments()
	if err != nil {
		log.Fatalf("读段表: %v", err)
	}
	ups, fp, fc, err := enrich.SeedFromDB(segs, *seed)
	if err != nil {
		log.Fatalf("回填: %v", err)
	}
	for i := 0; i < len(ups); i += 5000 {
		end := i + 5000
		if end > len(ups) {
			end = len(ups)
		}
		if err := db.UpdateSegments(ups[i:end]); err != nil {
			log.Fatalf("写入: %v", err)
		}
	}
	fmt.Printf("回填完成：补省 %d 段，补市 %d 段，共写入 %d 条\n", fp, fc, len(ups))
}
