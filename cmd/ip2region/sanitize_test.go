package main

import (
	"testing"

	"github.com/hel2o/ip2region/internal/geo"
)

func TestCleanGeo(t *testing.T) {
	cases := []struct {
		name string
		in   geo.GeoInfo
		want geo.GeoInfo
	}{
		{
			name: "水印串",
			in:   geo.GeoInfo{Country: "泰国", ISP: "CZ88.NET"},
			want: geo.GeoInfo{Country: "泰国"},
		},
		{
			name: "水印与地名粘连",
			in:   geo.GeoInfo{Country: "中国", Town: "沙田区/CZ88.NET"},
			want: geo.GeoInfo{Country: "中国", Town: "沙田区"},
		},
		{
			name: "直辖市省=市",
			in:   geo.GeoInfo{Country: "中国", Province: "北京市", City: "北京市"},
			want: geo.GeoInfo{Country: "中国", Province: "北京市"},
		},
		{
			name: "特区省=市",
			in:   geo.GeoInfo{Country: "中国", Province: "香港特别行政区", City: "香港特别行政区", County: "新界"},
			want: geo.GeoInfo{Country: "中国", Province: "香港特别行政区", County: "新界"},
		},
		{
			name: "市县同名（洛杉矶）",
			in:   geo.GeoInfo{Country: "美国", Province: "加利福尼亚州", City: "洛杉矶", County: "洛杉矶"},
			want: geo.GeoInfo{Country: "美国", Province: "加利福尼亚州", County: "洛杉矶"},
		},
		{
			name: "三层全同名（汉堡）",
			in:   geo.GeoInfo{Country: "德国", Province: "汉堡", City: "汉堡", County: "汉堡"},
			want: geo.GeoInfo{Country: "德国", Province: "汉堡"},
		},
		{
			name: "正常数据不动",
			in:   geo.GeoInfo{Country: "中国", Province: "广东省", City: "广州市", County: "番禺区", ISP: "中国电信"},
			want: geo.GeoInfo{Country: "中国", Province: "广东省", City: "广州市", County: "番禺区", ISP: "中国电信"},
		},
		{
			name: "吉林省吉林市不算重复",
			in:   geo.GeoInfo{Country: "中国", Province: "吉林省", City: "吉林市"},
			want: geo.GeoInfo{Country: "中国", Province: "吉林省", City: "吉林市"},
		},
	}
	for _, c := range cases {
		g := c.in
		cleanGeo(&g)
		if g != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, g, c.want)
		}
	}
}
