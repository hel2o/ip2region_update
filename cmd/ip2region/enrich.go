package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hel2o/ip2region/internal/enrich"
	"github.com/hel2o/ip2region/internal/provider"
	"github.com/hel2o/ip2region/internal/store"
)

func runEnrich(args []string) error {
	fs := newFlagSet("enrich", "调百度接口校正省市与 ISP（只补空/纠省市，不动区镇）")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	qps := fs.Int("qps", 20, "全局请求速率上限")
	workers := fs.Int("workers", 8, "并发数")
	limit := fs.Int("limit", 0, "最多请求数（0=不限，试跑用 200）")
	mask := fs.String("mask", "/24", "按此网段去重: /24 | /16 | none")
	dry := fs.Bool("dry-run", false, "只统计不写库")
	timeout := fs.Duration("timeout", 10*time.Second, "单请求超时")
	progressN := fs.Int("progress", 2000, "每 N 条打进度")
	conflictFile := fs.String("conflicts", "build/enrich-conflicts.tsv", "冲突样例输出（空则不写）")
	_ = fs.Parse(args)

	m, err := parseMask(*mask)
	if err != nil {
		return err
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
	if len(segs) == 0 {
		return fmt.Errorf("段表为空，请先执行 parse")
	}

	p := provider.NewBaidu(*timeout)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}

	st, err := enrich.Run(ctx, db, p, segs, enrich.Options{
		QPS:          *qps,
		Workers:      *workers,
		Limit:        *limit,
		Mask:         m,
		DryRun:       *dry,
		Timeout:      *timeout,
		ProgressN:    *progressN,
		ConflictFile: *conflictFile,
	})

	fmt.Println("---- enrich 统计 ----")
	fmt.Printf("中国段              : %d\n", st.Segments)
	fmt.Printf("去重探测点          : %d\n", st.Probes)
	fmt.Printf("缓存命中(免请求)    : %d\n", st.DBHits)
	fmt.Printf("真实请求            : %d\n", st.Requests)
	fmt.Printf("  无数据            : %d\n", st.NoData)
	fmt.Printf("  失败              : %d\n", st.Errors)
	fmt.Printf("修正省              : %d\n", st.ProvFixed)
	fmt.Printf("修正市              : %d\n", st.CityFixed)
	fmt.Printf("填充 ISP            : %d\n", st.ISPFilled)
	fmt.Printf("ISP 冲突(仅记录)    : %d\n", st.Conflicts)
	fmt.Printf("耗时                : %s\n", st.Elapsed.Round(time.Millisecond))

	if errors.Is(err, provider.ErrBlocked) {
		return fmt.Errorf("被百度限流/封禁，已停止。缓存已保留，稍后重跑会从缓存继续")
	}
	if errors.Is(err, context.Canceled) {
		log.Printf("已中断，缓存已保留，可重跑继续")
		return nil
	}
	return err
}

// parseMask 解析网段掩码参数。
func parseMask(s string) (uint32, error) {
	switch s {
	case "/24":
		return 0xFFFFFF00, nil
	case "/16":
		return 0xFFFF0000, nil
	case "none", "":
		return 0xFFFFFFFF, nil
	default:
		return 0, fmt.Errorf("非法 mask: %s（支持 /24 /16 none）", s)
	}
}
