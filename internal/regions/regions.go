// Package regions 把纯真库的国家串拆成 国家/省/市/区县/乡镇。
//
// 支持两种形态：
//
//  1. metowolf 预分隔串（en dash U+2013 连接，省用短名）：
//     "中国–福建"、"中国–广东–广州"、"美国–加利福尼亚州–圣克拉拉–山景城"
//  2. 原版 CZ88 连写串（兜底）："中国广东省广州市番禺区钟村"
//
// 短名仅对省级补全称（34 个封闭集合）；市/区/镇保持原样，
// 宁可少一个"市"字也不造出"延边市"这种错误名。
package regions

import "strings"

// Dash 是 metowolf 数据使用的分隔符。
const Dash = "–"

// Parts 是一次拆分结果。
type Parts struct {
	Country  string
	Province string
	City     string
	County   string
	Town     string
}

type provinceEntry struct {
	full   string
	alias  string
	direct bool // 直辖市/特别行政区：City 回填为省级
}

var provinces = []provinceEntry{
	{"北京市", "北京", true},
	{"天津市", "天津", true},
	{"上海市", "上海", true},
	{"重庆市", "重庆", true},
	{"河北省", "河北", false},
	{"山西省", "山西", false},
	{"辽宁省", "辽宁", false},
	{"吉林省", "吉林", false},
	{"黑龙江省", "黑龙江", false},
	{"江苏省", "江苏", false},
	{"浙江省", "浙江", false},
	{"安徽省", "安徽", false},
	{"福建省", "福建", false},
	{"江西省", "江西", false},
	{"山东省", "山东", false},
	{"河南省", "河南", false},
	{"湖北省", "湖北", false},
	{"湖南省", "湖南", false},
	{"广东省", "广东", false},
	{"海南省", "海南", false},
	{"四川省", "四川", false},
	{"贵州省", "贵州", false},
	{"云南省", "云南", false},
	{"陕西省", "陕西", false},
	{"甘肃省", "甘肃", false},
	{"青海省", "青海", false},
	{"内蒙古自治区", "内蒙古", false},
	{"广西壮族自治区", "广西", false},
	{"西藏自治区", "西藏", false},
	{"宁夏回族自治区", "宁夏", false},
	{"新疆维吾尔自治区", "新疆", false},
	{"香港特别行政区", "香港", true},
	{"澳门特别行政区", "澳门", true},
	{"台湾省", "台湾", false},
}

// matchProvince 在 s 头部做最长前缀命中。
func matchProvince(s string) (provinceEntry, int, bool) {
	best := -1
	bestLen := 0
	for i, p := range provinces {
		for _, cand := range []string{p.full, p.alias} {
			if strings.HasPrefix(s, cand) && len(cand) > bestLen {
				best, bestLen = i, len(cand)
			}
		}
	}
	if best < 0 {
		return provinceEntry{}, 0, false
	}
	return provinces[best], bestLen, true
}

// takeSegment 从 s 头部取到第一个行政后缀（含后缀本身）为止的片段。
// 同一位置命中多个后缀时取最长的。返回 (片段, 剩余串)。
func takeSegment(s string, suffixes []string) (string, string) {
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		rest := string(runes[i:])
		best := ""
		for _, suf := range suffixes {
			if strings.HasPrefix(rest, suf) && len(suf) > len(best) {
				best = suf
			}
		}
		if best != "" {
			return string(runes[:i+len([]rune(best))]), string(runes[i+len([]rune(best)):])
		}
	}
	return "", ""
}

var (
	citySuffix   = []string{"自治州", "地区", "林区", "盟", "市"}
	countySuffix = []string{"自治县", "自治旗", "矿区", "林区", "新区", "市", "区", "县", "旗"}
)

// Split 拆分纯真国家串，规则见包注释。
func Split(s string) Parts {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\u3000", "")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return Parts{}
	}
	if strings.Contains(s, Dash) {
		return splitDashed(s)
	}
	return splitCompact(s)
}

// splitDashed 处理 en dash 预分隔串。
// 首段通常是国家（中国/美国…）；若首段直接命中省级（"台湾–台北"、"香港–香港"）则视为无国家段。
// 省段再做一次省级匹配，消化 "山东德阳市" 这类漏分隔脏串；
// 直辖市/特别行政区后面重复出现的同名义段（"中国–上海–上海"）跳过。
func splitDashed(s string) Parts {
	segs := strings.Split(s, Dash)
	var p Parts
	fields := []*string{&p.Province, &p.City, &p.County, &p.Town}
	var direct *provinceEntry

	i := 0
	applied := func(seg string) bool {
		prov, n, ok := matchProvince(seg)
		if !ok {
			return false
		}
		p.Province = prov.full
		p.Country = "中国" // 港澳台等无国名前缀时归一
		if leftover := seg[n:]; leftover != "" {
			// "山东德阳市" → 山东省 + 德阳市，剩余进 City
			if p.City == "" {
				p.City = leftover
			} else {
				p.Town = joinNonEmpty(p.Town, leftover)
			}
		}
		if prov.direct {
			p.City = prov.full
			direct = &provinces[indexOfProvince(prov.full)]
		}
		return true
	}

	if _, _, ok := matchProvince(segs[0]); ok {
		applied(segs[0])
		i = 1
	} else {
		p.Country = segs[0]
		i = 1
		if i < len(segs) && applied(segs[i]) {
			i++
		}
	}

	fi := 0
	if p.City != "" {
		fi = 1
	}
	if p.Town != "" {
		fi = 3
	}
	for ; i < len(segs); i++ {
		seg := segs[i]
		if seg == "" {
			continue
		}
		if direct != nil && (seg == direct.alias || strings.HasPrefix(direct.full, seg)) {
			continue // 直辖市重复段
		}
		for fi < len(fields) && *fields[fi] != "" {
			fi++
		}
		if fi >= len(fields) {
			p.Town = joinNonEmpty(p.Town, seg)
			continue
		}
		*fields[fi] = seg
		fi++
	}
	return p
}

func indexOfProvince(full string) int {
	for i := range provinces {
		if provinces[i].full == full {
			return i
		}
	}
	return -1
}

// ProvinceShort 返回省级全称对应的短名（广东省→广东）；未知名返回空。
func ProvinceShort(full string) string {
	if i := indexOfProvince(full); i >= 0 {
		return provinces[i].alias
	}
	return ""
}

// ProvinceFromShort 把省级短名还原成全称（广东→广东省）；未知返回 false。
func ProvinceFromShort(short string) (string, bool) {
	for _, p := range provinces {
		if p.alias == short {
			return p.full, true
		}
	}
	return "", false
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "/" + b
}

// splitCompact 兜底处理原版 CZ88 连写串。
func splitCompact(s string) Parts {
	var p Parts
	rest := s
	if strings.HasPrefix(rest, "中国") {
		p.Country = "中国"
		rest = rest[len("中国"):]
	} else {
		p.Country = "中国"
	}

	if prov, n, ok := matchProvince(rest); ok {
		p.Province = prov.full
		rest = rest[n:]
		if prov.direct {
			p.City = prov.full
		}
		p.Country = "中国" // 港澳台等无国名前缀的情况在此归一
		if rest == "" {
			return p
		}
	} else {
		// 境外：整串作为国家字段，不做进一步拆分
		return Parts{Country: s}
	}

	if city, left := takeSegment(rest, citySuffix); city != "" {
		p.City = city
		rest = left
		if rest == "" {
			return p
		}
	}
	if county, left := takeSegment(rest, countySuffix); county != "" {
		p.County = county
		rest = left
		if rest == "" {
			return p
		}
	}
	p.Town = rest
	return p
}

var ispTable = []struct {
	match     string
	canonical string
}{
	// 港澳台运营商必须排在"电信"之前，否则"中华电信"会被子串匹配成中国电信
	{"中华电信", "中华电信"},
	{"台湾固网", "台湾固网"},
	{"台湾大哥大", "台湾大哥大"},
	{"远传", "远传电信"},
	{"亚太电信", "亚太电信"},
	{"香港电讯", "香港电讯"},
	{"和记电讯", "和记电讯"},
	{"数码通", "数码通"},
	{"电信盈科", "电讯盈科"},
	{"电信", "中国电信"},
	{"联通", "中国联通"},
	{"网通", "中国联通"},
	{"移动", "中国移动"},
	{"铁通", "中国铁通"},
	{"教育网", "教育网"},
	{"CERNET", "教育网"},
	{"长城宽带", "长城宽带"},
	{"鹏博士", "长城宽带"},
	{"广电", "中国广电"},
	{"有线通", "有线通"},
	{"天威", "天威视讯"},
	{"世纪互联", "世纪互联"},
	{"光环新网", "光环新网"},
	{"阿里云", "阿里云"},
	{"腾讯云", "腾讯云"},
	{"华为云", "华为云"},
	{"BGP", "BGP多线"},
	{"多线", "BGP多线"},
}

// ispOrgTable 是云厂商/IDC/DNS 组织名：命中后把整串放进 ISP 字段，
// 不做归一（如 "阿里巴巴anycast公共DNS"、"谷歌公司DNS服务器" 的描述本身有价值）。
// 注意避免过短歧义词：苹果会撞北京苹果园/广西平果，故只用 "Apple"。
var ispOrgTable = []string{
	"阿里", "腾讯", "谷歌", "Google", "Google", "CloudFlare", "Cloudflare", "CloudFront",
	"Amazon", "亚马逊", "AWS", "Apple", "微软", "Microsoft", "Azure",
	"UCloud", "网宿", "蓝汛", "百度", "网易", "字节", "抖音", "快手", "京东",
	"金山云", "青云", "七牛", "Oracle", "甲骨文", "Fastly", "Akamai",
	"Linode", "DigitalOcean", "Vultr", "Hetzner", "OVH", "Leaseweb",
	"Dropbox", "Netflix", "Telegram", "Discord", "Cloudflare",
	"IDC", "数据中心", "机房",
}

// NormalizeISP 识别并归一运营商名。返回 false 表示不是运营商串。
func NormalizeISP(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	for _, e := range ispTable {
		if strings.Contains(s, e.match) {
			return e.canonical, true
		}
	}
	for _, m := range ispOrgTable {
		if strings.Contains(s, m) {
			return s, true
		}
	}
	return "", false
}
