// Package legacy 读取旧版 ip2region xdb，用于把里面更详细的境外数据补回新库。
//
// 为什么不直接用 xdbmaker.Searcher：那个只支持单点查询，而这里需要把整个
// 旧库展开成区间表做归并（几十万段逐条查询反而更慢）。
// 所以直接按 xdb 的二进制布局解析段索引区：
//
//	header[8:12]  startIndexPtr
//	header[12:16] endIndexPtr
//	索引区每条 14 字节：sip(4) eip(4) dataLen(2) dataPtr(4)
//	数据区 dataPtr 处是 dataLen 字节的 UTF-8 region 串
package legacy

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hel2o/ip2region/internal/geo"
)

const (
	headerLen         = 256
	segmentIndexBlock = 14
	minFileSize       = headerLen + 65536*8 // 至少要容纳 header + 向量索引
	regionFieldCount  = 6
)

// Record 是旧库里的一条归属记录。Region 保留原始 6 段字段，字段数异常的按需补齐。
type Record struct {
	Start  uint32
	End    uint32
	Region [regionFieldCount]string
}

// Load 读入旧 xdb，返回按 Start 升序、彼此不重叠的区间表。
func Load(path string) ([]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < minFileSize {
		return nil, fmt.Errorf("文件太小（%d 字节），不是有效的 xdb", len(data))
	}

	startPtr := binary.LittleEndian.Uint32(data[8:12])
	endPtr := binary.LittleEndian.Uint32(data[12:16])
	if startPtr >= endPtr || int(endPtr) > len(data) {
		return nil, fmt.Errorf("索引区指针异常：%d ~ %d（文件 %d 字节）", startPtr, endPtr, len(data))
	}

	n := (endPtr - startPtr) / segmentIndexBlock
	recs := make([]Record, 0, n)
	// 同一 region 串在数据区会被大量复用，缓存一下避免重复解码
	cache := make(map[uint32]string, 4096)

	for i := uint32(0); i < n; i++ {
		p := startPtr + i*segmentIndexBlock
		sip := binary.LittleEndian.Uint32(data[p:])
		eip := binary.LittleEndian.Uint32(data[p+4:])
		dataLen := int(binary.LittleEndian.Uint16(data[p+8:]))
		dataPtr := binary.LittleEndian.Uint32(data[p+10:])
		if sip > eip || dataLen == 0 {
			continue
		}
		if int(dataPtr)+dataLen > len(data) {
			continue
		}

		region, ok := cache[dataPtr]
		if !ok {
			region = string(data[dataPtr : dataPtr+uint32(dataLen)])
			cache[dataPtr] = region
		}

		var rec Record
		rec.Start, rec.End = sip, eip
		rec.Region = splitRegion(region)
		recs = append(recs, rec)
	}

	sort.Slice(recs, func(i, j int) bool { return recs[i].Start < recs[j].Start })
	return recs, nil
}

// splitRegion 把 region 串切成固定 6 段。
// 旧库里有少量 7/8/10 段的脏数据（字段内容含竖线），超出部分丢弃，
// 不足的补空——宁可少一段也不能让后面的字段整体错位。
func splitRegion(s string) [regionFieldCount]string {
	var out [regionFieldCount]string
	parts := strings.Split(s, "|")
	for i := 0; i < regionFieldCount && i < len(parts); i++ {
		out[i] = strings.TrimSpace(parts[i])
	}
	return out
}

// Geo 把记录转成 GeoInfo。
func (r Record) Geo() geo.GeoInfo {
	return geo.GeoInfo{
		Country:  r.Region[0],
		Province: r.Region[1],
		City:     r.Region[2],
		County:   r.Region[3],
		Town:     r.Region[4],
		ISP:      r.Region[5],
	}
}

// Index 是区间表的二分索引。
type Index struct {
	recs   []Record
	starts []uint32
}

// NewIndex 构建索引。
func NewIndex(recs []Record) *Index {
	starts := make([]uint32, len(recs))
	for i := range recs {
		starts[i] = recs[i].Start
	}
	return &Index{recs: recs, starts: starts}
}

// Lookup 返回覆盖 ip 的记录。
func (x *Index) Lookup(ip uint32) (Record, bool) {
	if len(x.recs) == 0 {
		return Record{}, false
	}
	lo, hi := 0, len(x.starts)-1
	// 找最后一个 Start <= ip
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if x.starts[mid] <= ip {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if x.starts[lo] > ip {
		return Record{}, false
	}
	if ip > x.recs[lo].End {
		return Record{}, false
	}
	return x.recs[lo], true
}

// Len 返回记录数。
func (x *Index) Len() int { return len(x.recs) }

// Range 返回与 [start,end] 有交集的记录，并把首尾裁剪到该范围内。
func (x *Index) Range(start, end uint32) []Record {
	if len(x.recs) == 0 || start > end {
		return nil
	}
	i := sort.Search(len(x.starts), func(i int) bool { return x.recs[i].End >= start })
	var out []Record
	for ; i < len(x.recs) && x.recs[i].Start <= end; i++ {
		r := x.recs[i]
		if r.Start < start {
			r.Start = start
		}
		if r.End > end {
			r.End = end
		}
		if r.Start <= r.End {
			out = append(out, r)
		}
	}
	return out
}
