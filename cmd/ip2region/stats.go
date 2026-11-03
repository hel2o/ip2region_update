package main

import (
	"fmt"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
)

// runStats 输出归属库的字段覆盖率报表（IP 加权，不是段数加权）。
//
// 段数加权会骗人：一个段可能覆盖 400 万个 IP，也可能只有 1 个。
func runStats(args []string) error {
	fs := newFlagSet("stats", "输出归属库质量报表（IP 加权覆盖率）")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	_ = fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	t0 := time.Now()
	const lenExpr = "(end_ip - start_ip + 1)"

	var totalSeg, totalIP int64
	rows, err := db.QueryRows(`SELECT COUNT(*), COALESCE(SUM(` + lenExpr + `),0) FROM segments`)
	if err != nil {
		return err
	}
	totalSeg, totalIP = toI(rows[0][0]), toI(rows[0][1])

	fmt.Println("---- 归属库质量报表 ----")
	fmt.Printf("总段数              : %d\n", totalSeg)
	fmt.Printf("覆盖 IP             : %d\n", totalIP)

	rows, err = db.QueryRows(`SELECT
		COUNT(*), SUM(` + lenExpr + `),
		SUM(CASE WHEN province <> '' THEN ` + lenExpr + ` ELSE 0 END),
		SUM(CASE WHEN city <> '' THEN ` + lenExpr + ` ELSE 0 END),
		SUM(CASE WHEN county <> '' THEN ` + lenExpr + ` ELSE 0 END),
		SUM(CASE WHEN town <> '' THEN ` + lenExpr + ` ELSE 0 END),
		SUM(CASE WHEN isp <> '' THEN ` + lenExpr + ` ELSE 0 END)
		FROM segments WHERE country = '中国'`)
	if err != nil {
		return err
	}
	r := rows[0]
	cnSeg, cnIP := toI(r[0]), toI(r[1])

	pct := 0.0
	if totalIP > 0 {
		pct = float64(cnIP) / float64(totalIP) * 100
	}
	fmt.Printf("\n中国                : %d 段 / %.2f 亿 IP（占全域 %.1f%%）\n", cnSeg, float64(cnIP)/1e8, pct)
	fmt.Println("字段覆盖率（IP 加权）:")
	printCoverage("省", toI(r[2]), cnIP)
	printCoverage("市", toI(r[3]), cnIP)
	printCoverage("区县", toI(r[4]), cnIP)
	printCoverage("乡镇", toI(r[5]), cnIP)
	printCoverage("ISP", toI(r[6]), cnIP)

	fmt.Println("\n段来源分布（按段数）:")
	rows, err = db.QueryRows(`SELECT source, COUNT(*), SUM(` + lenExpr + `)
		FROM segments GROUP BY source ORDER BY 2 DESC LIMIT 8`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		fmt.Printf("  %-16s %8d 段  %14d IP\n", toS(row[0]), toI(row[1]), toI(row[2]))
	}

	fmt.Println("\n中国 IP 分布 Top10（按 IP 数）:")
	rows, err = db.QueryRows(`SELECT province, SUM(` + lenExpr + `)
		FROM segments WHERE country='中国' AND province <> ''
		GROUP BY province ORDER BY 2 DESC LIMIT 10`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		fmt.Printf("  %-16s %14d IP\n", toS(row[0]), toI(row[1]))
	}

	fmt.Println("\n最大的中国段 Top10（粗粒度残留检查）:")
	rows, err = db.QueryRows(`SELECT start_ip, end_ip, province, city, isp
		FROM segments WHERE country='中国' ORDER BY (end_ip - start_ip) DESC LIMIT 10`)
	if err != nil {
		return err
	}
	for _, row := range rows {
		start, end := uint32(toI(row[0])), uint32(toI(row[1]))
		fmt.Printf("  %-15s-%-15s %10d IP  %s|%s|%s\n",
			geo.Uint2IP(start), geo.Uint2IP(end), end-start+1, toS(row[2]), toS(row[3]), toS(row[4]))
	}

	fmt.Printf("\n耗时 %s\n", time.Since(t0).Round(time.Millisecond))
	return nil
}

func printCoverage(name string, covered, total int64) {
	p := 0.0
	if total > 0 {
		p = float64(covered) / float64(total) * 100
	}
	n := int(p / 100 * 30)
	bar := make([]rune, 30)
	for i := range bar {
		if i < n {
			bar[i] = '█'
		} else {
			bar[i] = '░'
		}
	}
	fmt.Printf("  %-4s %6.2f%%  %s  (%d IP)\n", name, p, string(bar), covered)
}

func toI(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		var n int64
		fmt.Sscan(x, &n)
		return n
	default:
		return 0
	}
}

func toS(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
