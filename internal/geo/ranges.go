package geo

import (
	"fmt"
	"sort"
)

// MaxIP 是 IPv4 地址空间的最后一个值。
const MaxIP uint32 = 0xFFFFFFFF

// Segment 是一条闭区间 [Start, End] 的归属记录。
// Start/End 用 uint32 表示，与 ip2region xdb 的内部表示一致。
type Segment struct {
	Start  uint32
	End    uint32
	Geo    GeoInfo
	Source string // 数据来源：qqwry / baidu / isp / custom，仅用于排查
}

// Len 返回该段包含的 IP 个数。
func (s Segment) Len() uint64 {
	if s.End < s.Start {
		return 0
	}
	return uint64(s.End) - uint64(s.Start) + 1
}

// Invalid 判断区间是否合法。
func (s Segment) Invalid() bool { return s.End < s.Start }

// UnknownGeo 是给无法确定归属的地址填的占位值。
var UnknownGeo = GeoInfo{Country: "IANA", ISP: "保留地址"}

// Normalize 把任意段序列整理成"排好序、无重叠、无缝隙、全覆盖"的标准形态。
//
// 返回值保证：
//   - 按 Start 严格升序
//   - seg[i].End+1 == seg[i+1].Start
//   - 首段 Start==0，末段 End==MaxIP
//
// 输入中的重叠按"按 Start 升序后先到先得"处理，被裁剪的段只保留未被占用的尾部；
// 缝隙用 UnknownGeo 填满。需要显式优先级时用 Apply。
func Normalize(segs []Segment) []Segment {
	if len(segs) == 0 {
		return []Segment{{Start: 0, End: MaxIP, Geo: UnknownGeo, Source: "fill"}}
	}
	sorted := make([]Segment, len(segs))
	copy(sorted, segs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})

	out := make([]Segment, 0, len(sorted)+16)
	var cursor uint32
	for _, s := range sorted {
		if s.Invalid() {
			continue
		}
		if s.Start > cursor {
			out = append(out, Segment{Start: cursor, End: s.Start - 1, Geo: UnknownGeo, Source: "fill"})
			cursor = s.Start
		}
		if s.End < cursor {
			continue // 完全被前段覆盖
		}
		s.Start = cursor
		out = append(out, s)
		if s.End == MaxIP {
			cursor = MaxIP
			break
		}
		cursor = s.End + 1
	}
	if len(out) > 0 && out[len(out)-1].End < MaxIP {
		out = append(out, Segment{Start: out[len(out)-1].End + 1, End: MaxIP, Geo: UnknownGeo, Source: "fill"})
	}
	return out
}

// Apply 用 over 覆盖 base，返回新的规范化段表。
//
// base 必须是 Normalize 产物。over 内部重叠时后出现者优先。
// 结果保持全覆盖且无重叠。
func Apply(base, over []Segment) []Segment {
	if len(over) == 0 {
		return base
	}
	o := make([]Segment, len(over))
	copy(o, over)
	sort.SliceStable(o, func(i, j int) bool { return o[i].Start < o[j].Start })

	// 复制一份 base，避免原地修改调用方的数据。
	cur := make([]Segment, len(base))
	copy(cur, base)

	out := make([]Segment, 0, len(base)+len(o))
	i := 0
	for _, ov := range o {
		if ov.Invalid() {
			continue
		}
		for i < len(cur) && cur[i].End < ov.Start {
			out = append(out, cur[i])
			i++
		}
		if i >= len(cur) {
			break
		}
		if cur[i].Start > ov.End {
			out = append(out, ov)
			continue
		}
		if cur[i].Start < ov.Start {
			left := cur[i]
			left.End = ov.Start - 1
			out = append(out, left)
			cur[i].Start = ov.Start
		}
		for i < len(cur) && cur[i].End <= ov.End {
			i++
		}
		if i < len(cur) && cur[i].Start <= ov.End {
			cur[i].Start = ov.End + 1
		}
		out = append(out, ov)
	}
	for ; i < len(cur); i++ {
		out = append(out, cur[i])
	}
	return out
}

// Compact 合并相邻且归属完全相同的段，返回可能更短的段表。
func Compact(segs []Segment) []Segment {
	if len(segs) == 0 {
		return segs
	}
	out := make([]Segment, 0, len(segs))
	cur := segs[0]
	for _, s := range segs[1:] {
		if s.Invalid() {
			continue
		}
		if cur.End != MaxIP && s.Start == cur.End+1 && s.Geo == cur.Geo {
			cur.End = s.End
			continue
		}
		out = append(out, cur)
		cur = s
	}
	out = append(out, cur)
	return out
}

// Validate 检查段表是否满足全覆盖、无重叠、严格升序。
func Validate(segs []Segment) error {
	if len(segs) == 0 {
		return fmt.Errorf("段表为空")
	}
	if segs[0].Start != 0 {
		return fmt.Errorf("首段未从 0.0.0.0 开始: %s", Uint2IP(segs[0].Start))
	}
	for i, s := range segs {
		if s.Invalid() {
			return fmt.Errorf("第 %d 段区间非法: %d-%d", i, s.Start, s.End)
		}
		if i == 0 {
			continue
		}
		prev := segs[i-1]
		if s.Start != prev.End+1 {
			if s.Start <= prev.End {
				return fmt.Errorf("第 %d 段与第 %d 段重叠: %s", i-1, i, Uint2IP(s.Start))
			}
			return fmt.Errorf("第 %d 段与第 %d 段之间有缝隙: %s-%s",
				i-1, i, Uint2IP(prev.End+1), Uint2IP(s.Start-1))
		}
	}
	if last := segs[len(segs)-1]; last.End != MaxIP {
		return fmt.Errorf("末段未覆盖到 255.255.255.255，止于 %s", Uint2IP(last.End))
	}
	return nil
}

// CountIPs 统计段表覆盖的 IP 总数。
func CountIPs(segs []Segment) uint64 {
	var n uint64
	for _, s := range segs {
		n += s.Len()
	}
	return n
}
