package regions

import "strings"

// cloudKeywords 标记"地理位置由路由决定"的段：云厂商、CDN、IDC、Anycast。
//
// 这类段的 IP 归属方是云厂商，但流量落在哪个 PoP 取决于用户从哪访问。
// 用百度探一个 IP 得到的城市，往往是 PoP 城市而不是网段注册地，
// 拿它覆盖 qqwry 的省市区会把归属改坏（实测 1.12.0.0/14 被判到北京，
// 而 qqwry 和 ip-api 都指向广东）。
//
// 所以 enrich/refine 对这类段只补空，不覆盖。
var cloudKeywords = []string{
	"腾讯云", "阿里云", "华为云", "金山云", "青云", "UCloud", "七牛", "又拍云",
	"网宿", "蓝汛", "世纪互联", "光环新网", "帝联", "白山云",
	"CloudFlare", "Cloudflare", "Akamai", "Fastly", "CloudFront",
	"Google", "Amazon", "AWS", "Azure", "微软", "Apple", "Fastly",
	"BGP", "多线", "数据中心", "机房", "IDC", "CDN",
	"字节", "抖音", "快手", "哔哩哔哩", "Bilibili", "网易", "百度", "京东",
}

// IsCloudISP 判断 ISP 串是否属于云/CDN/IDC 类（地理位置不可靠）。
func IsCloudISP(isp string) bool {
	if isp == "" {
		return false
	}
	for _, k := range cloudKeywords {
		if strings.Contains(isp, k) {
			return true
		}
	}
	return false
}
