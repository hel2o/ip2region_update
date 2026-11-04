package regions

import "strings"

// cz88Mark 是纯真免费版给境外记录打的版权水印。
//
// 境外记录的地区串常常直接就是 "CZ88.NET"，被当成 ISP/市/镇写进库里；
// 也有 "沙田区/CZ88.NET" 这种和真实地名粘在一起的形态。
// 这些都不是地理信息，属于数据源噪音，统一清掉。
const cz88Mark = "cz88.net"

// separatorTrim 是清洗后需要从两端剥掉的分隔符。
const separatorTrim = " /-|,、;；"

// StripWatermark 去掉字符串里的 CZ88 水印（大小写不敏感）。
//
//	"CZ88.NET"          → ""
//	"沙田区/CZ88.NET"    → "沙田区"
//	"www.cz88.net"      → ""
//	"CZ88.NET 中国电信"  → "中国电信"
//	"中国电信"           → "中国电信"（不含水印时原样返回）
func StripWatermark(s string) string {
	if s == "" {
		return ""
	}
	if !strings.Contains(strings.ToLower(s), cz88Mark) {
		return s
	}

	out := s
	for {
		i := strings.Index(strings.ToLower(out), cz88Mark)
		if i < 0 {
			break
		}
		out = out[:i] + out[i+len(cz88Mark):]
	}

	// 删掉水印后可能剩下 "www." 或重复分隔符，一起收拾干净
	out = strings.ReplaceAll(out, "www.", "")
	for strings.Contains(out, "//") {
		out = strings.ReplaceAll(out, "//", "/")
	}
	out = strings.Trim(out, separatorTrim)
	out = strings.TrimSpace(out)

	// 只剩分隔符的也视为空
	if strings.Trim(out, separatorTrim) == "" {
		return ""
	}
	return out
}

// HasWatermark 判断字符串是否含 CZ88 水印。
func HasWatermark(s string) bool {
	return strings.Contains(strings.ToLower(s), cz88Mark)
}
