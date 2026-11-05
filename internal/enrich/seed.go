package enrich

import (
	"fmt"
	"log"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/store"
)

// SeedFromDB 用上一轮库里的归属回填本轮段表的空字段。
//
// 为什么需要：全量重建会先跑 parse 把段表从原始数据重灌一遍，上一轮 enrich
// 累积的省市（尤其是源后来查不到的那部分，如台湾的城市、云段的省市）会随之
// 丢失。这里在 enrich 之前按"重叠面积最大"把旧值补回**空字段**：
//
//   - 省为空：补省 + 补市
//   - 省有值但市为空：只在旧库省完全一致时补市（避免张冠李戴）
//   - 两个都非空：不动（本轮探测结果优先，宁可信新不信旧）
//
// 只填空、不覆盖，所以不会把上一轮清掉的错误值又搬回来。
func SeedFromDB(segs []geo.Segment, oldPath string) (ups []store.SegmentUpdate, fillProv, fillCity int, err error) {
	old, err := store.Open(oldPath)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("打开旧库 %s: %w", oldPath, err)
	}
	defer old.Close()
	olds, err := old.LoadSegments()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("读旧库段表: %w", err)
	}
	log.Printf("旧库回填：旧库 %d 段，本库 %d 段", len(olds), len(segs))

	j := 0
	for i := range segs {
		s := &segs[i]
		if s.Geo.Country != "中国" {
			continue
		}
		if s.Geo.Province != "" && s.Geo.City != "" {
			continue
		}
		for j < len(olds) && olds[j].End < s.Start {
			j++
		}
		best := -1
		var bestOv uint64
		for k := j; k < len(olds) && olds[k].Start <= s.End; k++ {
			o := olds[k]
			if o.Geo.Country != "中国" {
				continue
			}
			// 省已有值时必须一致，否则补市会张冠李戴
			if s.Geo.Province != "" && o.Geo.Province != s.Geo.Province {
				continue
			}
			if s.Geo.Province == "" && o.Geo.Province == "" && o.Geo.City == "" {
				continue
			}
			if s.Geo.Province != "" && o.Geo.City == "" {
				continue
			}
			lo, hi := s.Start, s.End
			if o.Start > lo {
				lo = o.Start
			}
			if o.End < hi {
				hi = o.End
			}
			if ov := uint64(hi) - uint64(lo) + 1; ov > bestOv {
				best, bestOv = k, ov
			}
		}
		if best < 0 {
			continue
		}
		o := olds[best]
		beforeP, beforeC := s.Geo.Province, s.Geo.City
		if beforeP == "" && o.Geo.Province != "" {
			s.Geo.Province = o.Geo.Province
			fillProv++
		}
		if beforeC == "" && o.Geo.City != "" {
			s.Geo.City = o.Geo.City
			fillCity++
		}
		if s.Geo.Province != beforeP || s.Geo.City != beforeC {
			pv, cv := s.Geo.Province, s.Geo.City
			ups = append(ups, store.SegmentUpdate{Start: s.Start, Province: &pv, City: &cv})
		}
	}
	return ups, fillProv, fillCity, nil
}
