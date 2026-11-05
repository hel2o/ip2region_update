package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
	"github.com/hel2o/ip2region/internal/xdbmaker"
)

// runBuild 把 SQLite 段表导出为官方 maker 兼容的源 txt，再生成 xdb。
//
// 源 txt 行格式: 起始IP|结束IP|国|省|市|区|镇|ISP（region 固定 6 段）。
// maker 不限制 region 字段数，但查询侧必须按 6 段自行拆分。
func runBuild(args []string) error {
	fs := newFlagSet("build", "把段表导出 txt 并生成 xdb")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	from := fs.String("from", "db", "数据来源: db（导出段表）| txt（用 merge 的产物）")
	src := fs.String("src", "build/ip2region.txt", "源 txt（--from txt 时为输入，--from db 时为导出路径）")
	out := fs.String("out", "build/ip2region.xdb", "输出 xdb")
	policy := fs.String("policy", "vector", "索引策略: vector | btree")
	version := fs.String("version", "", "版本标记（写入 255.255.255.255，空=自动用当前时间）")
	_ = fs.Parse(args)

	switch *from {
	case "db":
		db, err := store.Open(*dbPath)
		if err != nil {
			return err
		}
		segs, err := db.LoadSegments()
		db.Close()
		if err != nil {
			return err
		}
		if len(segs) == 0 {
			return fmt.Errorf("段表为空，请先执行 parse")
		}
		log.Printf("载入段表: %d 段", len(segs))
		if n := sanitizeSegments(segs); n > 0 {
			log.Printf("清洗字段（水印/重复层级）: %d 段", n)
		}

		// 版本探针：255.255.255.255 写成 版本|YYYYMMDD|时间，加载端据此识别库版本。
		// txt 路径（merge 产物）已在 merge 阶段注入；这里覆盖 -from db 的直出路径。
		ver := *version
		if ver == "" {
			ver = time.Now().Format("2006-01-02 15:04")
		}
		if v, ok := injectVersionMarker(segs, ver); ok {
			segs = v
			log.Printf("版本标记: 255.255.255.255 → %s", ver)
		}

		if err := os.MkdirAll(filepath.Dir(*src), 0o755); err != nil {
			return err
		}
		nLines, err := exportTxt(segs, *src)
		if err != nil {
			return err
		}
		log.Printf("导出源 txt: %s (%d 行)", *src, nLines)
	case "txt":
		if _, err := os.Stat(*src); err != nil {
			return fmt.Errorf("源 txt 不可用（先跑 merge）: %w", err)
		}
		log.Printf("使用源 txt: %s", *src)
	default:
		return fmt.Errorf("非法 --from: %s（支持 db / txt）", *from)
	}

	idxPolicy, err := xdbmaker.IndexPolicyFromString(*policy)
	if err != nil {
		return err
	}

	t0 := time.Now()
	maker, err := xdbmaker.NewMaker(idxPolicy, *src, *out, nil)
	if err != nil {
		return fmt.Errorf("创建 maker: %w", err)
	}
	if err := maker.Init(); err != nil {
		return fmt.Errorf("maker init: %w", err)
	}
	if err := maker.Start(); err != nil {
		return fmt.Errorf("maker start: %w", err)
	}
	if err := maker.End(); err != nil {
		return fmt.Errorf("maker end: %w", err)
	}

	fi, err := os.Stat(*out)
	if err != nil {
		return err
	}
	log.Printf("生成 xdb: %s (%.1f MB, 耗时 %s)", *out, float64(fi.Size())/1024/1024, time.Since(t0).Round(time.Millisecond))
	return nil
}

// exportTxt 把段表写成 maker 可读的源 txt，返回行数。
func exportTxt(segs []geo.Segment, path string) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<20)
	n := 0
	for _, s := range segs {
		if _, err := fmt.Fprintf(w, "%s|%s|%s\n",
			geo.Uint2IP(s.Start), geo.Uint2IP(s.End), s.Geo.String()); err != nil {
			return n, err
		}
		n++
	}
	return n, w.Flush()
}
