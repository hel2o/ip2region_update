// Package geo 定义 IP 段归属的统一数据模型。
//
// 输出区域串固定为 6 段：国|省|市|区|镇|ISP，
// 例如：中国|广东省|广州市|番禺区|钟村|中国电信
//
// 注意：官方 ip2region SDK 的 Region 结构按 5 段解析，本库不兼容那条路径，
// 查询侧必须自行 SplitN(s, "|", 6)。
package geo

import (
	"fmt"
	"strings"
)

// FieldCount 是区域串的字段数。
const FieldCount = 6

// GeoInfo 是一条 IP 段的地理归属。
type GeoInfo struct {
	Country  string // 中国 / 美国 / IANA
	Province string // 广东省
	City     string // 广州市
	County   string // 番禺区
	Town     string // 钟村
	ISP      string // 中国电信
}

// String 输出 6 段区域串，缺失字段留空。
func (g GeoInfo) String() string {
	f := [FieldCount]string{g.Country, g.Province, g.City, g.County, g.Town, g.ISP}
	for i := range f {
		f[i] = sanitize(f[i])
	}
	return strings.Join(f[:], "|")
}

// IsEmpty 表示该归属无任何有效信息。
func (g GeoInfo) IsEmpty() bool {
	return g == GeoInfo{}
}

// Merge 用非空字段覆盖 g 的空字段（src 优先填充缺失项）。
func (g *GeoInfo) Merge(src GeoInfo) {
	if g.Country == "" {
		g.Country = src.Country
	}
	if g.Province == "" {
		g.Province = src.Province
	}
	if g.City == "" {
		g.City = src.City
	}
	if g.County == "" {
		g.County = src.County
	}
	if g.Town == "" {
		g.Town = src.Town
	}
	if g.ISP == "" {
		g.ISP = src.ISP
	}
}

// Fill 用 v 覆盖 g 的全部字段。
func (g *GeoInfo) Fill(v GeoInfo) { *g = v }

// Key 返回用于等价比较的键，空字段不参与。
func (g GeoInfo) Key() string { return g.String() }

// ParseGeo 解析 6 段区域串。少于 6 段时右补空，多于 6 段时把多余部分并入 ISP。
func ParseGeo(s string) (GeoInfo, error) {
	if !strings.Contains(s, "|") {
		return GeoInfo{}, fmt.Errorf("区域串缺少分隔符: %q", s)
	}
	parts := strings.Split(s, "|")
	var g GeoInfo
	fields := []*string{&g.Country, &g.Province, &g.City, &g.County, &g.Town, &g.ISP}
	for i := 0; i < len(parts) && i < FieldCount-1; i++ {
		*fields[i] = sanitize(parts[i])
	}
	if len(parts) >= FieldCount {
		g.ISP = sanitize(strings.Join(parts[FieldCount-1:], "|"))
	}
	return g, nil
}

// sanitize 去掉会破坏 xdb 区域串的字符。
// 竖线是字段分隔符；换行会截断 txt 中间文件。
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return ""
	}
	s = strings.ReplaceAll(s, "|", "/")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}
