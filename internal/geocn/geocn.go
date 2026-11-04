// Package geocn 读取 GeoCN 离线库（MMDB），提供区县级归属。
//
// 数据来源：https://github.com/ljxi/GeoCN
// 字段只有两个：division_code（国标行政区划编码）和 isp（7 种粗标签）。
//
// 它的价值在**区县**：本仓库其他数据源都到不了这一级
// （qqwry 只有 4.67% 的段有区县，百度接口只到市）。
// 实测 GeoCN 覆盖中国 92% 的段，其中 54% 的 IP 有区县级编码。
//
// 它的短板也明确：
//   - 省市来自"扫描公开 API"，云厂商/Anycast 段会给出 PoP 城市，不可信
//   - 只有 7 种 ISP 标签，比 ispip 网段表粗得多
//   - 更新"随缘"，可能几个月不更
//
// 所以本包只负责"翻译编码"，是否采信由 enrich 按规则决定。
package geocn

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Info 是一条查询结果。
type Info struct {
	Code     string // 6 位行政区划编码
	Province string // 编码前 2 位 + 0000
	City     string // 编码前 4 位 + 00
	District string // 编码本身，仅当末两位非 00 才有
	ISP      string // 原始粗标签：电信/联通/移动/鹏博士/教育网/广电
}

// DB 是打开的 GeoCN 库。
type DB struct {
	mmdb   *maxminddb.Reader
	codes  map[string]string
	Source string
}

// Open 打开 MMDB 并加载行政区划编码表（AreaCity 的 full.txt）。
func Open(mmdbPath, codeTablePath string) (*DB, error) {
	r, err := maxminddb.Open(mmdbPath)
	if err != nil {
		return nil, fmt.Errorf("打开 GeoCN mmdb: %w", err)
	}
	codes, err := loadCodes(codeTablePath)
	if err != nil {
		r.Close()
		return nil, err
	}
	return &DB{mmdb: r, codes: codes, Source: mmdbPath}, nil
}

// Close 释放文件句柄。
func (d *DB) Close() error { return d.mmdb.Close() }

// CodeCount 返回编码表条目数（便于排查表有没有加载对）。
func (d *DB) CodeCount() int { return len(d.codes) }

// Lookup 查询一个 IPv4 的归属。未收录返回 false。
func (d *DB) Lookup(ip uint32) (Info, bool) {
	var rec struct {
		DivisionCode uint64 `maxminddb:"division_code"`
		ISP          string `maxminddb:"isp"`
	}
	addr := netip.AddrFrom4([4]byte{byte(ip >> 24), byte(ip >> 16), byte(ip >> 8), byte(ip)})
	result := d.mmdb.Lookup(addr)
	if !result.Found() || result.Err() != nil {
		return Info{}, false
	}
	if err := result.Decode(&rec); err != nil {
		return Info{}, false
	}
	if rec.DivisionCode == 0 {
		return Info{}, false
	}
	code := fmt.Sprintf("%06d", rec.DivisionCode)
	info := Info{
		Code:     code,
		Province: d.codes[code[:2]+"0000"],
		ISP:      rec.ISP,
	}
	if !strings.HasSuffix(code, "0000") {
		info.City = d.codes[code[:4]+"00"]
	}
	if !strings.HasSuffix(code, "00") {
		info.District = d.codes[code]
	}
	// 省/市名查不到时整个结果不可用（编码表版本不匹配）
	if info.Province == "" {
		return Info{}, false
	}
	if info.City == "" {
		info.City = info.Province // 省级编码：市未知
	}
	return info, true
}

// HasDistrict 表示这条结果带区县级信息。
func (i Info) HasDistrict() bool { return i.District != "" }

// loadCodes 读取 `<编码>\t<名称>` 格式的行政区划表。
func loadCodes(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开行政区划编码表: %w", err)
	}
	defer f.Close()

	codes := make(map[string]string, 4096)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		code = strings.TrimSpace(code)
		name = strings.TrimSpace(name)
		if len(code) == 6 && name != "" {
			codes[code] = name
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(codes) < 100 {
		return nil, fmt.Errorf("编码表条目过少（%d），文件可能不是 AreaCity full.txt", len(codes))
	}
	return codes, nil
}
