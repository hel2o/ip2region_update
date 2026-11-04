// Package ispip 读取 ispip.m27.tech 的国内运营商/云厂商网段表，
// 给出"这个 IP 属于哪家运营商"的判断。
//
// 和百度接口的本质区别：这份数据来自 APNIC 分配记录 + whois，
// 是**分配归属**，不是"探测点落在哪个城市"。所以它判断 ISP 比百度可靠得多，
// 也不受 anycast / PoP 影响。
//
// 数据格式：每个运营商一个页面，正文是纯 CIDR 列表（每行一条，<br> 分隔）。
// 站点每日更新，脚本自动生成，官方自己也注明"难免有错漏"。
package ispip

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SourceURL 是数据来源站点。
const SourceURL = "https://ispip.m27.tech/"

// pageName 把页面文件名映射成运营商名。
// 只收录能作为 ISP 字段的实体；othernet / hongkong / macau / taiwan 是地域兜底桶，
// 不是运营商，收录会让"台湾某固网"被改成"台湾"。
var pageName = map[string]string{
	"chinatelecom":   "中国电信",
	"unicom":         "中国联通",
	"cmcc":           "中国移动",
	"crtc":           "中国铁通",
	"cernet":         "教育网",
	"cstnet":         "科技网",
	"broadcasting":   "中国广电",
	"gwbn":           "长城宽带",
	"sinnet":         "世纪互联",
	"chinanetcenter": "网宿",
	"tencent":        "腾讯云",
	"alisoft":        "阿里云",
	"huawei":         "华为云",
	"baidu":          "百度",
	"kscloud":        "金山云",
	"ucloud":         "UCloud",
	"meituan":        "美团",
	"jdcom":          "京东",
	"netease":        "网易",
	"xiaoniaoyun":    "小鸟云",
	"googlecn":       "Google",
}

// priority 决定网段重叠时谁胜出。
// 云厂商/IDC 的分配比基础运营商更具体，必须优先，否则 1.12.0.0/14 这种
// 腾讯云段会被电信列表抢走。
var priority = map[string]int{
	"中国电信": 10, "中国联通": 10, "中国移动": 10, "中国铁通": 10,
	"教育网": 20, "科技网": 20, "中国广电": 20,
	"长城宽带": 30, "世纪互联": 30, "网宿": 30,
	"腾讯云": 40, "阿里云": 40, "华为云": 40, "百度": 40, "金山云": 40,
	"UCloud": 40, "美团": 40, "京东": 40, "网易": 40, "小鸟云": 40, "Google": 40,
}

// PageKeys 返回需要下载的页面名（按字母序）。
func PageKeys() []string {
	keys := make([]string, 0, len(pageName))
	for k := range pageName {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Piece 是合并后的一段不重叠区间及其归属。
type Piece struct {
	Start uint32
	End   uint32
	ISP   string
}

// Table 是查询用的索引，按 Start 升序且互不重叠。
type Table struct {
	pieces []Piece
	starts []uint32

	// 统计信息，便于排查
	Networks int            // 原始 CIDR 条数
	Covered  uint64         // 覆盖 IP 数
	Files    int            // 读到的运营商页面数
	PerISP   map[string]int // 各运营商 CIDR 条数
}

var cidrRe = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})/(\d{1,2})\b`)

type span struct {
	start, end uint32
	prio       int
	isp        string
}

// LoadDir 读取目录下所有 *.html / *.txt 网段表。
// 文件名（去掉扩展名）必须是已知运营商，否则跳过。
func LoadDir(dir string) (*Table, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取网段表目录: %w", err)
	}

	var spans []span
	t := &Table{PerISP: map[string]int{}}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".html" && ext != ".htm" && ext != ".txt" {
			continue
		}
		key := strings.TrimSuffix(strings.ToLower(e.Name()), ext)
		isp, ok := pageName[key]
		if !ok {
			continue // 兜底桶或未知页面
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		t.Files++
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
			spans = append(spans, span{start: start, end: end, prio: priority[isp], isp: isp})
			t.Networks++
			t.PerISP[isp]++
		}
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("目录 %s 下没有解析到任何网段", dir)
	}

	t.pieces = sweep(spans)
	t.starts = make([]uint32, len(t.pieces))
	for i, p := range t.pieces {
		t.starts[i] = p.Start
		t.Covered += uint64(p.End) - uint64(p.Start) + 1
	}
	return t, nil
}

// sweep 把可能重叠的区间压成不重叠的段，重叠处取优先级最高的归属。
func sweep(spans []span) []Piece {
	type event struct {
		pos   int64
		isp   string
		prio  int
		delta int
	}
	evs := make([]event, 0, len(spans)*2)
	for _, s := range spans {
		evs = append(evs, event{pos: int64(s.start), isp: s.isp, prio: s.prio, delta: +1})
		evs = append(evs, event{pos: int64(s.end) + 1, isp: s.isp, prio: s.prio, delta: -1})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].pos < evs[j].pos })

	active := map[string]int{}
	prioOf := map[string]int{}
	var out []Piece

	const limit = int64(1) << 32
	var curPos int64
	best := ""

	flush := func(endPos int64) {
		if best == "" || endPos <= curPos {
			return
		}
		if endPos > limit {
			endPos = limit
		}
		out = append(out, Piece{Start: uint32(curPos), End: uint32(endPos - 1), ISP: best})
	}

	for i := 0; i < len(evs); {
		pos := evs[i].pos
		flush(pos)
		for i < len(evs) && evs[i].pos == pos {
			e := evs[i]
			if e.delta > 0 {
				active[e.isp]++
				prioOf[e.isp] = e.prio
			} else if active[e.isp]--; active[e.isp] <= 0 {
				delete(active, e.isp)
			}
			i++
		}
		best = ""
		bestPrio := -1
		for isp := range active {
			if p := prioOf[isp]; p > bestPrio {
				bestPrio, best = p, isp
			}
		}
		curPos = pos
		if pos >= limit {
			break
		}
	}

	// 合并相邻同归属的段，缩小索引
	if len(out) < 2 {
		return out
	}
	merged := out[:1]
	for _, p := range out[1:] {
		last := &merged[len(merged)-1]
		if last.ISP == p.ISP && last.End+1 == p.Start {
			last.End = p.End
			continue
		}
		merged = append(merged, p)
	}
	return merged
}

// Lookup 返回 ip 所属运营商；未收录返回空。
func (t *Table) Lookup(ip uint32) string {
	i := sort.Search(len(t.starts), func(i int) bool { return t.starts[i] > ip })
	if i == 0 {
		return ""
	}
	p := t.pieces[i-1]
	if ip >= p.Start && ip <= p.End {
		return p.ISP
	}
	return ""
}

// Priority 返回某运营商名的优先级（未知名返回 0）。
func Priority(isp string) int { return priority[isp] }

// PieceCount 返回合并后的区间数。
func (t *Table) PieceCount() int { return len(t.pieces) }

// parseCIDR 把 CIDR 转成闭区间，并做基本合法性检查。
func parseCIDR(netStr, bits string) (uint32, uint32, error) {
	parts := strings.Split(netStr, ".")
	if len(parts) != 4 {
		return 0, 0, fmt.Errorf("非法 IP: %s", netStr)
	}
	var v uint32
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return 0, 0, fmt.Errorf("非法 IP: %s", netStr)
		}
		v |= uint32(n) << (24 - 8*i)
	}
	ones, err := strconv.Atoi(bits)
	if err != nil || ones < 0 || ones > 32 {
		return 0, 0, fmt.Errorf("非法掩码: %s/%s", netStr, bits)
	}
	mask := uint32(0xFFFFFFFF)
	if ones < 32 {
		mask = ^(uint32(1)<<(32-ones) - 1)
	}
	start := v & mask
	return start, start | ^mask, nil
}
