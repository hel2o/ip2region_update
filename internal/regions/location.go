package regions

import (
	"strings"
)

// ISPLocation 是百度 opendata 接口 location 串的解析结果。
//
// 百度 location 形如：
//
//	广东省广州市 电信        → 省/市 + ISP
//	江苏省南京市 电信
//	北京市海淀区 百度        → 能到区（仅少数机房 IP）
//	中国 移动                → 只有国家 + ISP
//	美国                     → 境外只有国家
type ISPLocation struct {
	Country  string
	Province string
	City     string
	County   string
	ISP      string
}

// 省级后缀（市级/区县级后缀见 regions.go 的 citySuffix / countySuffix）
var provinceSuffix = []string{"特别行政区", "自治区", "省"}

// ParseLocation 解析百度返回的 location 串。
// 原则：能拆多细拆多细，拆不出来留空，绝不臆造。
func ParseLocation(s string) ISPLocation {
	var out ISPLocation
	loc, isp := splitLocationISP(s)
	if isp != "" {
		if canon, ok := NormalizeISP(isp); ok {
			out.ISP = canon
		} else {
			out.ISP = isp
		}
	}
	if loc == "" {
		return out
	}

	rest := loc

	// 1. 国家 + 省级
	switch {
	case strings.HasPrefix(rest, "中国"):
		out.Country = "中国"
		rest = strings.TrimPrefix(rest, "中国")
	case func() bool { _, _, ok := matchProvince(rest); return ok }():
		p, n, _ := matchProvince(rest)
		out.Country = "中国"
		out.Province = p.full
		if p.direct {
			out.City = p.full
		}
		rest = rest[n:]
	default:
		// 境外：整串当国家（"美国"、"澳大利亚"）
		out.Country = rest
		return out
	}

	// 2. 省（"中国广东省广州市" 这种前缀连写）
	if out.Province == "" && rest != "" {
		if seg, left := takeSegment(rest, provinceSuffix); seg != "" {
			if p, _, ok := matchProvince(seg); ok {
				out.Province = p.full
				if p.direct {
					out.City = p.full
				}
			} else {
				out.Province = seg
			}
			rest = left
		} else if p, n, ok := matchProvince(rest); ok {
			out.Province = p.full
			if p.direct {
				out.City = p.full
			}
			rest = rest[n:]
		}
	}

	// 3. 市（直辖市的 City 已填，只需吞掉重复段）
	if rest != "" {
		if out.City != "" {
			// "北京市北京市" 去重
			if seg, left := takeSegment(rest, citySuffix); seg != "" && seg == out.City {
				rest = left
			}
		} else if seg, left := takeSegment(rest, citySuffix); seg != "" {
			out.City = seg
			rest = left
		}
	}

	// 4. 区县
	if rest != "" {
		if seg, _ := takeSegment(rest, countySuffix); seg != "" {
			out.County = seg
		} else {
			out.County = rest
		}
	}
	return out
}

// splitLocationISP 把 "广东省广州市 电信" 拆成 (地区串, ISP串)。
// 分隔符可能是半角/全角空格。最后一段若像 ISP 就独立出来。
func splitLocationISP(s string) (loc, isp string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\u3000' || r == '\t' || r == '\u00a0'
	})
	if len(fields) == 0 {
		return "", ""
	}
	if len(fields) == 1 {
		// "广东省广州市电信" 这种无分隔的串，以行政区后缀为准，不能整体当 ISP
		if !hasAdminSuffix(fields[0]) && ispLike(fields[0]) {
			return "", fields[0]
		}
		return fields[0], ""
	}
	last := fields[len(fields)-1]
	if !hasAdminSuffix(last) && ispLike(last) {
		return strings.Join(fields[:len(fields)-1], ""), last
	}
	return strings.Join(fields, ""), ""
}

// hasAdminSuffix 判断串里是否含行政区划后缀。
func hasAdminSuffix(s string) bool {
	for _, suf := range []string{"省", "市", "区", "县", "自治区", "特别行政区", "自治州", "盟", "旗"} {
		if strings.Contains(s, suf) {
			return true
		}
	}
	return false
}

// ispLike 判断一个短串是否像运营商/机构名。
func ispLike(s string) bool {
	if s == "" {
		return false
	}
	if _, ok := NormalizeISP(s); ok {
		return true
	}
	for _, k := range []string{"电信", "联通", "移动", "铁通", "广电", "教育网", "宽带", "云", "网络", "科技", "数据"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
