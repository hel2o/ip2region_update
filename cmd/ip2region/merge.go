package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
)

// runMerge 把段表 + 自定义覆盖合成最终 txt。
//
// 自定义覆盖优先级最高：先 Apply 到段表，再 Compact，最后写 maker 可读的 txt。
func runMerge(args []string) error {
	fs := newFlagSet("merge", "叠加自定义 CIDR 覆盖并输出最终 txt")
	dbPath := fs.String("db", "data/ip2region.db", "SQLite 路径")
	customDir := fs.String("custom", "data/custom", "自定义覆盖目录（*.txt）")
	out := fs.String("out", "build/ip2region.txt", "输出 txt")
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

	over, err := loadCustomDir(*customDir)
	if err != nil {
		return err
	}
	log.Printf("自定义覆盖 %d 条", len(over))

	merged := segs
	if len(over) > 0 {
		merged = geo.Apply(segs, over)
	}
	// 清掉 "CZ88.NET" 之类的数据源水印，必须在 Compact 之前，
	// 这样清空同名字段后的相邻段才能合并回一段
	if n := sanitizeSegments(merged); n > 0 {
		log.Printf("清洗字段（水印/重复层级）: %d 段", n)
	}
	merged = geo.Compact(merged)
	if err := geo.Validate(merged); err != nil {
		return fmt.Errorf("合并结果校验失败: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	t0 := time.Now()
	n, err := exportTxt(merged, *out)
	if err != nil {
		return err
	}

	var customIPs uint64
	for _, s := range over {
		customIPs += s.Len()
	}
	fmt.Println("---- merge 完成 ----")
	fmt.Printf("输入段数            : %d\n", len(segs))
	fmt.Printf("自定义规则          : %d 条（覆盖 %d 个 IP）\n", len(over), customIPs)
	fmt.Printf("输出段数            : %d\n", n)
	fmt.Printf("覆盖 IP 总数        : %d\n", geo.CountIPs(merged))
	fmt.Printf("输出                : %s\n", *out)
	fmt.Printf("耗时                : %s\n", time.Since(t0).Round(time.Millisecond))
	return nil
}

// loadCustomDir 读取目录下所有 *.txt 覆盖规则。
func loadCustomDir(dir string) ([]geo.Segment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 目录不存在视为没有自定义规则
		}
		return nil, err
	}
	var out []geo.Segment
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".txt") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		rows, err := parseCustomFile(p)
		if err != nil {
			return nil, err
		}
		log.Printf("  %s: %d 条", e.Name(), len(rows))
		out = append(out, rows...)
	}
	// 按 Start 排序，后写者覆盖先写者（Apply 内部按此顺序）
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, nil
}

// parseCustomFile 解析覆盖文件。
//
// 每行两种写法，`#` 开头为注释：
//
//	CIDR|国|省|市|区|镇|ISP      例: 181.0.0.0/12|中国|广东省|广州市|番禺区|钟村|中国电信
//	起IP|止IP|国|省|市|区|镇|ISP  例: 1.2.3.0|1.2.3.255|中国|...
//
// 区域串字段数不足 6 时右补空；超过 6 报错（防止竖线污染）。
func parseCustomFile(path string) ([]geo.Segment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []geo.Segment
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seg, err := parseCustomLine(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", filepath.Base(path), lineNo, err)
		}
		out = append(out, seg)
	}
	return out, sc.Err()
}

func parseCustomLine(line string) (geo.Segment, error) {
	parts := strings.Split(line, "|")
	if len(parts) < 3 {
		return geo.Segment{}, fmt.Errorf("字段不足，至少需要 地址|国|省 形式: %q", line)
	}

	var start, end uint32
	first := strings.TrimSpace(parts[0])
	if strings.Contains(first, "/") {
		var err error
		start, end, err = geo.ParseCIDRRange(first)
		if err != nil {
			return geo.Segment{}, err
		}
		parts = parts[1:]
	} else {
		if len(parts) < 4 {
			return geo.Segment{}, fmt.Errorf("起止 IP 形式字段不足: %q", line)
		}
		var ok bool
		start, ok = geo.IP2Uint(first)
		if !ok {
			return geo.Segment{}, fmt.Errorf("非法起始 IP: %q", first)
		}
		end, ok = geo.IP2Uint(strings.TrimSpace(parts[1]))
		if !ok {
			return geo.Segment{}, fmt.Errorf("非法结束 IP: %q", parts[1])
		}
		parts = parts[2:]
	}

	region := strings.TrimSpace(strings.Join(parts, "|"))
	g, err := parseCustomGeo(region)
	if err != nil {
		return geo.Segment{}, err
	}
	if g.IsEmpty() {
		return geo.Segment{}, fmt.Errorf("区域串为空: %q", line)
	}
	if end < start {
		return geo.Segment{}, fmt.Errorf("结束 IP 小于起始 IP: %q", line)
	}
	return geo.Segment{Start: start, End: end, Geo: g, Source: "custom"}, nil
}

// parseCustomGeo 解析 6 段区域串，字段不足右补空。
func parseCustomGeo(s string) (geo.GeoInfo, error) {
	f := strings.Split(s, "|")
	if len(f) > geo.FieldCount {
		return geo.GeoInfo{}, fmt.Errorf("区域串有 %d 段，超过 %d 段（字段里不能含竖线）", len(f), geo.FieldCount)
	}
	for len(f) < geo.FieldCount {
		f = append(f, "")
	}
	g := geo.GeoInfo{
		Country: strings.TrimSpace(f[0]), Province: strings.TrimSpace(f[1]),
		City: strings.TrimSpace(f[2]), County: strings.TrimSpace(f[3]),
		Town: strings.TrimSpace(f[4]), ISP: strings.TrimSpace(f[5]),
	}
	return g, nil
}
