package ispip

// 港澳台地域表：ispip.m27.tech 的 hongkong / macau / taiwan 三个页面是
// "地域兜底桶"（故意不在 ISP 网段表 pageName 里），每页列出该地区的全部 CIDR。
//
// 用途：归属地优先级第二（自定义覆盖 > 港澳台表 > qifu/百度 > qqwry）。
// 真实案例：72.255.243.92 在 ispip 香港表的 72.255.224.0/19 里，
// 但 qqwry/qifu 都把它判成美国——表里有交集就必须纠正成 中国|香港特别行政区。
//
// 实现：复用 Table/sweep，把"省份名"当 Piece.ISP 存，Lookup 返回省份名。

import (
	"fmt"
	"os"
	"path/filepath"
)

// hmtPages 把页面文件映射成标准省份名（与 qifu/库内命名一致）。
var hmtPages = map[string]string{
	"hongkong.html": "香港特别行政区",
	"macau.html":    "澳门特别行政区",
	"taiwan.html":   "台湾省",
}

// LoadRegionTable 读目录下三个港澳台页面，返回"Lookup 返回省份名"的地域表。
// 某个页面缺失只跳过（打日志级别的问题由调用方看统计发现），
// 三个页面都读不到才报错。三个页面的段理论互不重叠，重叠时按固定顺序取舍。
func LoadRegionTable(dir string) (*Table, error) {
	var spans []span
	t := &Table{PerISP: map[string]int{}}
	// prio 按页面固定顺序递增，重叠时先声明的胜出，结果可复现
	prio := 0
	for _, file := range []string{"hongkong.html", "macau.html", "taiwan.html"} {
		prov := hmtPages[file]
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("读取港澳台地域表 %s: %w", file, err)
		}
		t.Files++
		prio++
		seen := map[string]bool{}
		for _, m := range cidrRe.FindAllStringSubmatch(string(data), -1) {
			cidr := m[1] + "/" + m[2]
			if seen[cidr] {
				continue
			}
			seen[cidr] = true
			start, end, err := parseCIDR(m[1], m[2])
			if err != nil {
				continue
			}
			spans = append(spans, span{start: start, end: end, prio: prio, isp: prov})
			t.Networks++
			t.PerISP[prov]++
		}
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("目录 %s 下没有解析到任何港澳台网段", dir)
	}
	t.pieces = sweep(spans)
	t.starts = make([]uint32, len(t.pieces))
	for i, p := range t.pieces {
		t.starts[i] = p.Start
		t.Covered += uint64(p.End) - uint64(p.Start) + 1
	}
	return t, nil
}

// Intersects 判断 [start,end] 是否与表内任一区间有交集。
func (t *Table) Intersects(start, end uint32) bool {
	i := sortSearchUint32(t.starts, start)
	if i > 0 && t.pieces[i-1].End >= start {
		return true
	}
	return i < len(t.pieces) && t.pieces[i].Start <= end
}

// FullyCovers 若 [start,end] 完整落在同一段内，返回该段的归属（省份名）。
func (t *Table) FullyCovers(start, end uint32) (string, bool) {
	i := sortSearchUint32(t.starts, start)
	if i == 0 {
		return "", false
	}
	p := t.pieces[i-1]
	if start >= p.Start && end <= p.End {
		return p.ISP, true
	}
	return "", false
}

// sortSearchUint32 在升序切片里找第一个大于 ip 的下标。
func sortSearchUint32(a []uint32, ip uint32) int {
	lo, hi := 0, len(a)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if a[mid] <= ip {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
