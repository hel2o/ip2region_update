package regions

import "testing"

func TestParseLocation(t *testing.T) {
	cases := []struct {
		in                                     string
		country, prov, city, county, isp        string
	}{
		{"广东省广州市 电信", "中国", "广东省", "广州市", "", "中国电信"},
		{"江苏省南京市 电信", "中国", "江苏省", "南京市", "", "中国电信"},
		{"北京市海淀区 百度", "中国", "北京市", "北京市", "海淀区", "百度"},
		{"北京市北京市 电信", "中国", "北京市", "北京市", "", "中国电信"},
		{"中国 移动", "中国", "", "", "", "中国移动"},
		{"中国", "中国", "", "", "", ""},
		{"美国", "美国", "", "", "", ""},
		{"澳大利亚", "澳大利亚", "", "", "", ""},
		{"内蒙古自治区呼和浩特市 联通", "中国", "内蒙古自治区", "呼和浩特市", "", "中国联通"},
		{"新疆维吾尔自治区乌鲁木齐市 电信", "中国", "新疆维吾尔自治区", "乌鲁木齐市", "", "中国电信"},
		{"香港", "中国", "香港特别行政区", "香港特别行政区", "", ""},
		{"中国台湾省台北市 中华电信", "中国", "台湾省", "台北市", "", "中华电信"},
		{"浙江省杭州市 阿里巴巴anycast公共DNS", "中国", "浙江省", "杭州市", "", "阿里巴巴anycast公共DNS"},
		{"广东省广州市番禺区 电信", "中国", "广东省", "广州市", "番禺区", "中国电信"},
		{"广东省广州市电信", "中国", "广东省", "广州市", "电信", ""},
	}
	for _, c := range cases {
		got := ParseLocation(c.in)
		if got.Country != c.country || got.Province != c.prov || got.City != c.city ||
			got.County != c.county || got.ISP != c.isp {
			t.Errorf("ParseLocation(%q)\n  got  %+v\n  want {%s|%s|%s|%s|%s}",
				c.in, got, c.country, c.prov, c.city, c.county, c.isp)
		}
	}
}
