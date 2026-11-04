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
	"sort"
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

	// 区间索引：按 Start 升序、互不重叠，用于"按区间枚举"（补区县时要把
	// 一个大段按 GeoCN 的网段边界切开，单点查询做不到）。
	spans    []span
	infoOf   []Info // 与 codeList 一一对应，避免每次查询重算省市名
	codeList []string
	codeSeen map[string]int
}

// span 是索引里的一条区间。code 存的是 codeList 下标，省内存。
type span struct {
	Start, End uint32
	code       uint32
}

// Slice 是 Spans 返回的一段切片（已裁剪到查询区间内）。
type Slice struct {
	Start, End uint32
	Info       Info
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
	info := d.resolve(fmt.Sprintf("%06d", rec.DivisionCode), rec.ISP)
	// 省名查不到时整个结果不可用（编码表版本不匹配）
	if info.Province == "" {
		return Info{}, false
	}
	return info, true
}

// resolve 把编码翻译成省市县名。
func (d *DB) resolve(code, isp string) Info {
	info := Info{
		Code:     code,
		Province: d.codes[code[:2]+"0000"],
		ISP:      isp,
	}
	if !strings.HasSuffix(code, "0000") {
		info.City = d.codes[code[:4]+"00"]
	}
	if !strings.HasSuffix(code, "00") {
		info.District = d.codes[code]
	}
	if info.City == "" {
		info.City = info.Province // 省级编码：市未知
	}
	return info
}

// BuildIndex 建立区间索引。调用一次后 Spans 才可用。
//
// 全库约 111 万条，遍历一次 mmdb 需要一两秒，所以做成显式调用，
// 只补点（Lookup）的调用方不必付这个代价。
func (d *DB) BuildIndex() error {
	if d.spans != nil {
		return nil
	}
	d.codeSeen = make(map[string]int, 4096)

	for result := range d.mmdb.Networks() {
		p := result.Prefix()
		addr := p.Addr()
		if !addr.Is4() && !addr.Is4In6() {
			continue
		}
		bits := p.Bits()
		if addr.Is4In6() {
			addr = addr.Unmap()
			bits -= 96
		}
		if bits < 0 || bits > 32 {
			continue
		}
		var rec struct {
			DivisionCode uint64 `maxminddb:"division_code"`
			ISP          string `maxminddb:"isp"`
		}
		if err := result.Decode(&rec); err != nil || rec.DivisionCode == 0 {
			continue
		}
		a4 := addr.As4()
		start := uint32(a4[0])<<24 | uint32(a4[1])<<16 | uint32(a4[2])<<8 | uint32(a4[3])
		var end uint32
		if bits == 0 {
			end = 0xFFFFFFFF
		} else {
			end = start | uint32((uint64(1)<<(32-bits))-1)
		}
		code := fmt.Sprintf("%06d", rec.DivisionCode)
		ci, ok := d.codeSeen[code]
		if !ok {
			info := d.resolve(code, rec.ISP)
			if info.Province == "" {
				continue
			}
			ci = len(d.codeList)
			d.codeSeen[code] = ci
			d.codeList = append(d.codeList, code)
			d.infoOf = append(d.infoOf, info)
		}
		d.spans = append(d.spans, span{Start: start, End: end, code: uint32(ci)})
	}

	sort.Slice(d.spans, func(i, j int) bool { return d.spans[i].Start < d.spans[j].Start })
	// 合并同编码的相邻区间，减少条目
	out := d.spans[:0]
	for _, sp := range d.spans {
		if n := len(out); n > 0 && out[n-1].code == sp.code && out[n-1].End+1 == sp.Start {
			out[n-1].End = sp.End
			continue
		}
		out = append(out, sp)
	}
	d.spans = out
	return nil
}

// SpanCount 返回索引条目数。
func (d *DB) SpanCount() int { return len(d.spans) }

// Spans 把 [start, end] 按 GeoCN 的网段边界切开，返回裁剪后的各片。
// 未建立索引或完全不覆盖时返回 nil。
func (d *DB) Spans(start, end uint32) []Slice {
	if len(d.spans) == 0 || start > end {
		return nil
	}
	i := sort.Search(len(d.spans), func(i int) bool { return d.spans[i].End >= start })
	var out []Slice
	for ; i < len(d.spans) && d.spans[i].Start <= end; i++ {
		sp := d.spans[i]
		a, b := sp.Start, sp.End
		if a < start {
			a = start
		}
		if b > end {
			b = end
		}
		if a > b {
			continue
		}
		out = append(out, Slice{Start: a, End: b, Info: d.infoOf[sp.code]})
	}
	return out
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
