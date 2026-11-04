package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	path := "data/ip2region.db"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	var segs, caches, empty int
	_ = db.QueryRow("SELECT COUNT(*) FROM segments").Scan(&segs)
	_ = db.QueryRow("SELECT COUNT(*) FROM ip_cache").Scan(&caches)
	_ = db.QueryRow("SELECT COUNT(*) FROM ip_cache WHERE province='' AND city='' AND isp=''").Scan(&empty)
	fmt.Printf("segments=%d ip_cache=%d 空结果=%d\n", segs, caches, empty)

	rows, err := db.Query("SELECT provider, COUNT(*) FROM ip_cache GROUP BY provider ORDER BY 2 DESC")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p string
			var n int
			_ = rows.Scan(&p, &n)
			fmt.Printf("  provider=%-12s %d\n", p, n)
		}
	}

	var cls, city, isp int
	_ = db.QueryRow("SELECT COUNT(*) FROM segments WHERE country='中国'").Scan(&cls)
	_ = db.QueryRow("SELECT COUNT(*) FROM segments WHERE country='中国' AND city<>''").Scan(&city)
	_ = db.QueryRow("SELECT COUNT(*) FROM segments WHERE country='中国' AND isp<>''").Scan(&isp)
	fmt.Printf("中国段=%d 其中有市=%d 有ISP=%d\n", cls, city, isp)
}
