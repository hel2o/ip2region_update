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

	"github.com/hel2o/ip2region/internal/provider"
	"github.com/hel2o/ip2region/internal/refine"
	"github.com/hel2o/ip2region/internal/store"
)

// runRefine 对超过阈值的大段做二分收敛细分。
// 必须在 enrich 之后跑：enrich 负责覆盖全部段（/24 去重 + 中点探测），
// refine 只针对大段补充多探点，把被错误放大到几百万 IP 的归属切开。
func runRefine(args []string) error {
	fs := newFlagSet("refine", "对大段做二分收敛细分（enrich 之后执行）")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	threshold := fs.Int("threshold", 4096, "只处理长度 >= 该值的段（4096 = /20）")
	minSpan := fs.Int("min-span", 256, "收敛粒度，到这个 IP 数就停（256 = /24）")
	depth := fs.Int("depth", 20, "递归深度安全阀（正常不会触发）")
	segProbes := fs.Int("seg-probes", 128, "单个段最多探测几次，防止复杂段烧请求")
	qps := fs.Int("qps", 20, "全局请求速率上限")
	workers := fs.Int("workers", 4, "并发段数")
	limit := fs.Int("limit", 0, "最多请求数（0=不限）")
	dry := fs.Bool("dry-run", false, "只统计不写库")
	onlyEmpty := fs.Bool("only-empty", false, "只处理省市为空的段")
	progressN := fs.Int("progress", 500, "每 N 个段打进度")
	report := fs.String("report", "build/refine-report.tsv", "细分明细输出（空则不写）")
	timeout := fs.Duration("timeout", 10*time.Second, "单请求超时")
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
	if len(segs) == 0 {
		return fmt.Errorf("段表为空，请先执行 parse")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := provider.NewBaidu(*timeout)
	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}

	st, err := refine.Run(ctx, db, p, segs, refine.Options{
		Threshold:  *threshold,
		MinSpan:    *minSpan,
		MaxDepth:   *depth,
		SegProbes:  *segProbes,
		QPS:        *qps,
		Workers:    *workers,
		Limit:      *limit,
		DryRun:     *dry,
		OnlyEmpty:  *onlyEmpty,
		ProgressN:  *progressN,
		ReportFile: *report,
	})

	fmt.Println("---- refine 统计 ----")
	fmt.Printf("处理大段            : %d\n", st.BigSegments)
	fmt.Printf("三点一致整段确认    : %d\n", st.Confirmed)
	fmt.Printf("发生细分的段        : %d（新增 %d 段）\n", st.SplitSegs, st.NewPieces)
	fmt.Printf("请求 / 缓存命中     : %d / %d\n", st.Requests, st.CacheHits)
	fmt.Printf("  无数据            : %d\n", st.NoData)
	fmt.Printf("  失败              : %d\n", st.Errors)
	fmt.Printf("耗时                : %s\n", st.Elapsed.Round(time.Millisecond))

	if errors.Is(err, provider.ErrBlocked) {
		log.Printf("被限流，已停止；缓存已保留，稍后重跑继续")
		return nil
	}
	if errors.Is(err, context.Canceled) {
		log.Printf("已中断，缓存已保留，可重跑继续")
		return nil
	}
	return err
}
