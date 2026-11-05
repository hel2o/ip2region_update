package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// allStep 是流水线里的一步。
type allStep struct {
	name string
	run  func() error
}

// runAll 一键重建 xdb：把整条流水线按顺序跑一遍，任一步失败立即停。
//
//	1 fetch   下载 qqwry.dat + 运营商网段表 + GeoCN（失败只警告，用本地已有的）
//	2 parse   解开 qqwry，拆成 6 段骨架
//	3 legacy  用旧版 xdb 补境外缺失的省市（可选）
//	4 enrich  网段表定 ISP → 百度判省市 → GeoCN 补区县
//	5 refine  大段边界递归收敛
//	6 merge   叠加 data/custom/*.txt
//	7 build   生成 xdb
//	8 verify  校验全覆盖 + 与 golden 对拍
//	9 stats   字段覆盖率报表
//
// 加了 -only-custom 时只跑 6~7 两步：段表原样复用，不下载、不增强、不收敛。
// 只改了 data/custom/*.txt 的场合必须走这个模式，否则 parse 会重灌段表，
// 把百度/GeoCN 的增强结果洗掉。
//
// 重复跑很便宜：enrich/refine 的请求缓存按 /24 存在 SQLite 里，
// 数据没变时第二次跑几乎零请求。
func runAll(args []string) error {
	fs := newFlagSet("all", "一键重建 xdb（fetch → parse → legacy → enrich → refine → merge → build → verify → stats）")

	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	txtOut := fs.String("txt", "build/ip2region.txt", "中间产物 txt 路径")
	xdbOut := fs.String("out", "build/ip2region.xdb", "输出 xdb 路径")
	legacyXdb := fs.String("legacy-xdb", "build/compare/old.xdb", "旧版 xdb（用作境外省市补充，文件不存在则跳过）")
	amapKey := fs.String("amap-key", os.Getenv("AMAP_KEY"), "高德 Key（补百度缺失的省市，留空不启用）")

	qps := fs.Int("qps", 100, "enrich / refine 的请求速率上限（自适应模式下是天花板）")
	workers := fs.Int("workers", 16, "并发数")
	probeStep := fs.Int("probe-step", 256, "片内每多少 IP 布一个探测点")
	probeCap := fs.Int("probe-cap", 1024, "每个段最多几个探测点")
	minPiece := fs.Int("min-piece", 2048, "GeoCN 切片小于该值就合并")
	probeJitter := fs.Float64("probe-jitter", 0.6, "探测点片内随机偏移比例 0~1（0=固定中点/可复现，默认 0.6 每次不同）")
	probeSeed := fs.Int64("probe-seed", 0, "抖动种子（0=每次运行随机）")
	mask := fs.String("mask", "/24", "探测点去重粒度: /24 | /16 | none（抖动采样要绕过缓存时用 none）")

	verifyN := fs.Int("verify-n", 100000, "verify 对拍抽样 IP 数（0 = 跳过对拍）")
	onlyCustom := fs.Bool("only-custom", false, "只重跑 merge + build（仅改了 data/custom/*.txt 时用，不碰段表）")
	skipFetch := fs.Bool("skip-fetch", false, "跳过下载（用本地已有数据）")
	skipLegacy := fs.Bool("skip-legacy", false, "跳过旧库境外补丁")
	skipEnrich := fs.Bool("skip-enrich", false, "跳过百度增强")
	skipRefine := fs.Bool("skip-refine", false, "跳过边界收敛")
	_ = fs.Parse(args)

	// 只改自定义覆盖时，parse 会重灌段表、把百度/GeoCN 的增强结果洗掉，
	// 所以这个模式下强制跳过所有写段表的步骤，只留 merge + build。
	if *onlyCustom {
		*skipFetch, *skipLegacy, *skipEnrich, *skipRefine = true, true, true, true
	}

	start := time.Now()
	fmt.Printf("==== 一键重建 xdb ====\n")
	fmt.Printf("库: %s\n输出: %s\n", *dbPath, *xdbOut)
	if *onlyCustom {
		fmt.Printf("模式: 只更新自定义覆盖（复用段表，只跑 merge + build）\n")
	}

	// 全量重建会先跑 parse 重灌段表；先把当前库快照一份，供 enrich 回填空省市
	// （源后来查不到的历史积累，如台湾城市、云段省市，否则会被洗掉）。
	seedDBPath := "build/backup/prev.db"
	if !*skipEnrich {
		if _, err := os.Stat(*dbPath); err == nil {
			if err := copyFile(*dbPath, seedDBPath); err != nil {
				log.Printf("快照当前库失败（本次不回填）: %v", err)
				seedDBPath = ""
			} else {
				log.Printf("已快照当前库: %s（enrich 前用于回填空省市）", seedDBPath)
			}
		} else {
			seedDBPath = ""
		}
	}

	var steps []allStep

	if !*skipFetch {
		steps = append(steps, allStep{"下载数据（qqwry.dat / 网段表 / GeoCN）", func() error {
			err := runFetch([]string{})
			if err != nil {
				// 下载失败不该中断：本地已有数据照样能出库，只是不新
				log.Printf("!! 下载失败，继续用本地已有数据: %v", err)
			}
			return nil
		}})
	}

	if !*onlyCustom {
		steps = append(steps, allStep{"解析 qqwry → 段表", func() error {
			return runParse([]string{"-db", *dbPath})
		}})
	}

	if !*skipLegacy {
		if fi, err := os.Stat(*legacyXdb); err == nil && !fi.IsDir() {
			steps = append(steps, allStep{"旧库境外省市补丁", func() error {
				return runLegacy([]string{"-db", *dbPath, "-xdb", *legacyXdb})
			}})
		} else {
			fmt.Printf("（跳过旧库补丁：%s 不存在）\n", *legacyXdb)
		}
	}

	if !*skipEnrich {
		steps = append(steps, allStep{"百度增强（ISP / 省市 / 区县）", func() error {
			a := []string{
				"-db", *dbPath,
				"-qps", strconv.Itoa(*qps),
				"-workers", strconv.Itoa(*workers),
				"-probe-step", strconv.Itoa(*probeStep),
				"-probe-cap", strconv.Itoa(*probeCap),
				"-min-piece", strconv.Itoa(*minPiece),
				"-probe-jitter", strconv.FormatFloat(*probeJitter, 'f', -1, 64),
				"-probe-seed", strconv.FormatInt(*probeSeed, 10),
				"-mask", *mask,
			}
			a = append(a, "-adaptive", "true")
			if seedDBPath != "" {
				a = append(a, "-seed-db", seedDBPath)
			}
			if *amapKey != "" {
				a = append(a, "-amap-key", *amapKey)
			}
			return runEnrich(a)
		}})
	}

	if !*skipRefine {
		steps = append(steps, allStep{"大段边界递归收敛", func() error {
			return runRefine([]string{
				"-db", *dbPath,
				"-qps", strconv.Itoa(*qps),
				"-workers", strconv.Itoa(*workers),
			})
		}})
	}

	steps = append(steps,
		allStep{"叠加自定义覆盖 → txt", func() error {
			return runMerge([]string{"-db", *dbPath, "-out", *txtOut})
		}},
		allStep{"生成 xdb", func() error {
			return runBuild([]string{"-from", "txt", "-src", *txtOut, "-out", *xdbOut})
		}},
	)

	// 段表没动过，verify / stats 读的都是 SQLite 中间表，这时跑没有意义
	if !*onlyCustom {
		steps = append(steps,
			allStep{"校验（全覆盖 + 对拍）", func() error {
				a := []string{"-db", *dbPath, "-n", strconv.Itoa(*verifyN), "-sample", "false"}
				if *verifyN <= 0 {
					a = append(a, "-golden", "")
				}
				return runVerify(a)
			}},
			allStep{"覆盖率报表", func() error {
				return runStats([]string{"-db", *dbPath})
			}},
		)
	}

	total := len(steps)
	for i, st := range steps {
		fmt.Printf("\n========== [%d/%d] %s ==========\n", i+1, total, st.name)
		t0 := time.Now()
		if err := st.run(); err != nil {
			fmt.Printf("\n!! 第 %d 步「%s」失败: %v\n", i+1, st.name, err)
			return fmt.Errorf("流水线在第 %d 步中断: %w", i+1, err)
		}
		fmt.Printf("---------- 用时 %s ----------\n", time.Since(t0).Round(time.Second))
	}

	// 产物摘要
	fmt.Printf("\n========== 完成 ==========\n")
	fi, err := os.Stat(*xdbOut)
	if err == nil {
		fmt.Printf("文件      : %s\n", *xdbOut)
		fmt.Printf("大小      : %s（%d 字节）\n", humanSize(fi.Size()), fi.Size())
		if sum, err := fileMD5(*xdbOut); err == nil {
			fmt.Printf("md5       : %s\n", sum)
		}
	}
	fmt.Printf("总耗时    : %s\n", time.Since(start).Round(time.Second))
	fmt.Printf("\n发布时把 %s 拷进 ip2region_update/ 并改名 ip2region_<数据版本>.xdb 即可。\n", *xdbOut)
	return nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// copyFile 复制文件（用于全量重建前快照当前库）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
