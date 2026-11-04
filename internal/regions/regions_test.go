package regions

import "testing"

func TestSplitTypical(t *testing.T) {
	cases := []struct {
		in      string
		country string
		prov    string
		city    string
		county  string
		town    string
	}{
		{"中国广东省广州市番禺区钟村", "中国", "广东省", "广州市", "番禺区", "钟村"},
		{"中国广东省广州市", "中国", "广东省", "广州市", "", ""},
		{"广东省深圳市", "中国", "广东省", "深圳市", "", ""},
		{"中国江苏省苏州市昆山市玉山镇", "中国", "江苏省", "苏州市", "昆山市", "玉山镇"},
		{"中国广西壮族自治区南宁市青秀区", "中国", "广西壮族自治区", "南宁市", "青秀区", ""},
		{"内蒙古包头市青山区", "中国", "内蒙古自治区", "包头市", "青山区", ""},
		{"新疆乌鲁木齐市天山区", "中国", "新疆维吾尔自治区", "乌鲁木齐市", "天山区", ""},
		{"中国北京市朝阳区", "中国", "北京市", "北京市", "朝阳区", ""},
		{"北京市海淀区", "中国", "北京市", "北京市", "海淀区", ""},
		{"上海浦东新区", "中国", "上海市", "上海市", "浦东新区", ""},
		{"重庆万州区", "中国", "重庆市", "重庆市", "万州区", ""},
		{"香港", "中国", "香港特别行政区", "香港特别行政区", "", ""},
		{"中国香港九龙城区", "中国", "香港特别行政区", "香港特别行政区", "九龙城区", ""},
		{"澳门", "中国", "澳门特别行政区", "澳门特别行政区", "", ""},
		{"台湾省台北市", "中国", "台湾省", "台北市", "", ""},
		{"中国台湾省新竹县", "中国", "台湾省", "", "新竹县", ""},
		{"湖北省恩施土家族苗族自治州恩施市", "中国", "湖北省", "恩施土家族苗族自治州", "恩施市", ""},
		{"贵州省黔东南苗族侗族自治州凯里市", "中国", "贵州省", "黔东南苗族侗族自治州", "凯里市", ""},
		{"黑龙江省大兴安岭地区加格达奇区", "中国", "黑龙江省", "大兴安岭地区", "加格达奇区", ""},
	}
	for _, c := range cases {
		got := Split(c.in)
		if got.Country != c.country || got.Province != c.prov || got.City != c.city ||
			got.County != c.county || got.Town != c.town {
			t.Errorf("Split(%q) = %+v, want {%s|%s|%s|%s|%s}",
				c.in, got, c.country, c.prov, c.city, c.county, c.town)
		}
	}
}

func TestSplitDashed(t *testing.T) {
	cases := []struct {
		in                                string
		country, prov, city, county, town string
	}{
		{"中国–福建", "中国", "福建省", "", "", ""},
		{"中国–广东–广州", "中国", "广东省", "广州", "", ""},
		{"中国–广东–广州–番禺–钟村", "中国", "广东省", "广州", "番禺", "钟村"},
		{"中国–上海–上海", "中国", "上海市", "上海市", "", ""},
		{"中国–北京", "中国", "北京市", "北京市", "", ""},
		{"香港–香港", "中国", "香港特别行政区", "香港特别行政区", "", ""},
		{"台湾–台北", "中国", "台湾省", "台北", "", ""},
		{"香港–香港", "中国", "香港特别行政区", "香港特别行政区", "", ""},
		{"山东德阳市", "中国", "山东省", "德阳市", "", ""},
		{"美国–加利福尼亚州–圣克拉拉–山景城", "美国", "加利福尼亚州", "圣克拉拉", "山景城", ""},
		{"美国", "美国", "", "", "", ""},
	}
	for _, c := range cases {
		got := Split(c.in)
		if got.Country != c.country || got.Province != c.prov || got.City != c.city ||
			got.County != c.county || got.Town != c.town {
			t.Errorf("Split(%q) = %+v, want {%s|%s|%s|%s|%s}",
				c.in, got, c.country, c.prov, c.city, c.county, c.town)
		}
	}
}

func TestSplitForeign(t *testing.T) {
	cases := []string{"美国", "美国弗吉尼亚州", "澳大利亚", "日本东京都", "APNIC", "保留地址"}
	for _, c := range cases {
		got := Split(c)
		if got.Country != c {
			t.Errorf("Split(%q).Country = %q, want 原样保留", c, got.Country)
		}
		if got.Province != "" || got.City != "" {
			t.Errorf("境外串不应拆出省市: %q -> %+v", c, got)
		}
	}
}

func TestNormalizeISP(t *testing.T) {
	cases := map[string]string{
		"电信":         "中国电信",
		"中国电信":       "中国电信",
		"联通":         "中国联通",
		"网通":         "中国联通",
		"移动":         "中国移动",
		"铁通":         "中国铁通",
		"教育网/CERNET": "教育网",
		"BGP多线":      "BGP多线",
	}
	for in, want := range cases {
		got, ok := NormalizeISP(in)
		if !ok || got != want {
			t.Errorf("NormalizeISP(%q) = %q,%v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "中山大学", "白云机场"} {
		if _, ok := NormalizeISP(in); ok {
			t.Errorf("NormalizeISP(%q) 不应命中", in)
		}
	}
}
