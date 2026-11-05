package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hel2o/ip2region/internal/enrich"
	"github.com/hel2o/ip2region/internal/geocn"
	"github.com/hel2o/ip2region/internal/ispip"
	"github.com/hel2o/ip2region/internal/provider"
	"github.com/hel2o/ip2region/internal/store"
)

func runEnrich(args []string) error {
	fs := newFlagSet("enrich", "调百度接口校正省市与 ISP（只补空/纠省市，不动区镇）")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	qps := fs.Int("qps", 100, "全局请求速率上限（自适应模式下是天花板）")
	workers := fs.Int("workers", 16, "并发数")
	limit := fs.Int("limit", 0, "最多请求数（0=不限，试跑用 200）")
	mask := fs.String("mask", "/24", "按此网段去重: /24 | /16 | none")
	dry := fs.Bool("dry-run", false, "只统计不写库")
	timeout := fs.Duration("timeout", 10*time.Second, "单请求超时")
	progressN := fs.Int("progress", 2000, "每 N 条打进度")
	conflictFile := fs.String("conflicts", "build/enrich-conflicts.tsv", "冲突样例输出（空则不写）")
	override := fs.Bool("override", true, "用百度覆盖已有省市（默认开启，传 -override=false 关掉）")
	ispDir := fs.String("ispip", "data/ispip", "运营商网段表目录（空串=不用）")
	geocnPath := fs.String("geocn", "data/geocn/GeoCN.mmdb", "GeoCN 离线库路径（空串=不补区县）")
	amapKey := fs.String("amap-key", os.Getenv("AMAP_KEY"), "高德 Web 服务 Key（只用于补百度缺失的省市，留空则不启用）")
	primary := fs.String("primary", "qifu", "主查询源: qifu（默认，失败自动回退 opendata）| opendata")
	refresh := fs.Bool("refresh", false, "刷新模式：忽略换源前的历史缓存，全部重新查询（本次写入的缓存仍可断点续跑）")
	adaptive := fs.Bool("adaptive", true, "自适应限速：延迟劣化/出错自动退避，健康时爬升（-qps 为上限）")
	probeStep := fs.Int("probe-step", 256, "每多少 IP 布一个探测点（0=每段只探中点）")
	probeCap := fs.Int("probe-cap", 1024, "每个段最多几个探测点（0=不封顶）")
	minPiece := fs.Int("min-piece", 2048, "GeoCN 切片小于该值就与相邻片合并（0=不合并）")
	probeJitter := fs.Float64("probe-jitter", 0.6, "探测点在片内的随机偏移比例 0~1（0=固定取中点/结果可复现；默认 0.6 每次采样不同）")
	probeSeed := fs.Int64("probe-seed", 0, "抖动种子（0=每次运行随机；给固定值可复现同一次采样）")
	seedDB := fs.String("seed-db", "", "上一轮库路径：enrich 前按其回填空省市（全量重建时防止丢历史积累）")
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

	var ispTables *ispip.Table
	if *ispDir != "" {
		if _, statErr := os.Stat(*ispDir); statErr == nil {
			ispTables, err = ispip.LoadDir(*ispDir)
			if err != nil {
				return err
			}
			log.Printf("运营商网段表: %d 个文件 / %d 条 CIDR / 合并后 %d 段 / 覆盖 %.1f%% 全域",
				ispTables.Files, ispTables.Networks, ispTables.PieceCount(),
				float64(ispTables.Covered)/4294967296*100)
		} else {
			log.Printf("网段表目录 %s 不存在，跳过按网段判 ISP", *ispDir)
		}
	}

	var gcn *geocn.DB
	if *geocnPath != "" {
		codePath := filepath.Join(filepath.Dir(*geocnPath), "full.txt")
		if _, statErr := os.Stat(*geocnPath); statErr == nil {
			gcn, err = geocn.Open(*geocnPath, codePath)
			if err != nil {
				return err
			}
			defer gcn.Close()
			tIdx := time.Now()
			if err := gcn.BuildIndex(); err != nil {
				return fmt.Errorf("构建 GeoCN 区间索引: %w", err)
			}
			log.Printf("GeoCN 离线库已加载（编码 %d 条，区间索引 %d 条，%s）",
				gcn.CodeCount(), gcn.SpanCount(), time.Since(tIdx).Round(time.Millisecond))
		} else {
			log.Printf("GeoCN 库 %s 不存在，跳过补区县", *geocnPath)
		}
	}

	p, err := buildSource(*primary, *timeout)
	if err != nil {
		return err
	}

	var fb provider.Provider
	if a := provider.NewAmap(*amapKey, *timeout); a != nil {
		fb = a
		log.Printf("备用源已启用: 高德 IP 定位（仅在百度没给省市时调用，注意月度配额）")
	}

	// 刷新模式：只复用本次来源写入的缓存，历史上的旧源结果一律重查。
	var cacheNames []string
	if *refresh {
		cacheNames = []string{p.Name()}
		log.Printf("刷新模式：忽略 provider≠%q 的历史缓存，全部重新查询", p.Name())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll("build", 0o755); err != nil {
		return err
	}

	st, err := enrich.Run(ctx, db, p, segs, enrich.Options{
		QPS:          *qps,
		Adaptive:     *adaptive,
		Workers:      *workers,
		Limit:        *limit,
		Mask:         m,
		DryRun:       *dry,
		Timeout:      *timeout,
		ProgressN:    *progressN,
		ConflictFile: *conflictFile,
		Override:     *override,
		ISPTables:    ispTables,
		GeoCN:        gcn,
		Fallback:     fb,
		ProbeStep:    *probeStep,
		ProbeCap:     *probeCap,
		ProbeJitter:  *probeJitter,
		SeedDB:       *seedDB,
		ProbeSeed:    *probeSeed,
		MinPiece:     *minPiece,
		CacheNames:   cacheNames,
	})

	fmt.Println("---- enrich 统计 ----")
	fmt.Printf("中国段              : %d\n", st.Segments)
	fmt.Printf("切出片数            : %d（新增 %d 段）\n", st.Pieces, st.SplitSegs)
	fmt.Printf("去重探测点          : %d\n", st.Probes)
	fmt.Printf("缓存命中(免请求)    : %d\n", st.DBHits)
	fmt.Printf("真实请求            : %d\n", st.Requests)
	fmt.Printf("  无数据            : %d\n", st.NoData)
	fmt.Printf("  失败              : %d\n", st.Errors)
	fmt.Printf("修正省              : %d\n", st.ProvFixed)
	fmt.Printf("修正市              : %d\n", st.CityFixed)
	fmt.Printf("填充 ISP（查询源）  : %d\n", st.ISPFilled)
	fmt.Printf("补 ISP（网段表）    : %d\n", st.ISPFilledByNet)
	fmt.Printf("纠 ISP（网段表）    : %d\n", st.ISPFixedByNet)
	fmt.Printf("补区县（GeoCN）     : %d\n", st.CountyFilled)
	fmt.Printf("清空旧区县          : %d\n", st.CountyCleared)
	fmt.Printf("GeoCN 省市不一致放弃: %d\n", st.GeoCNConflict)
	fmt.Printf("ISP 冲突(仅记录)    : %d\n", st.Conflicts)
	fmt.Printf("耗时                : %s\n", st.Elapsed.Round(time.Millisecond))

	if errors.Is(err, provider.ErrBlocked) {
		return fmt.Errorf("被查询源限流/封禁，已停止。缓存已保留，稍后重跑会从缓存继续")
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
