package enrich

import (
	"testing"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/provider"
)

func seg(start, end uint32, prov, city, isp string) geo.Segment {
	return geo.Segment{
		Start: start, End: end, Source: "qqwry",
		Geo: geo.GeoInfo{Country: "中国", Province: prov, City: city, ISP: isp},
	}
}

func TestDecide(t *testing.T) {
	t.Run("ISP 为空时填充", func(t *testing.T) {
		segs := []geo.Segment{seg(100, 200, "广东省", "广州", "")}
		ups, k, conf := decide(segs, &probe{segIdx: []int{0}},
			provider.Result{Province: "广东省", City: "广州市", ISP: "中国电信"}, true)
		if len(ups) != 1 || ups[0].ISP == nil || *ups[0].ISP != "中国电信" {
			t.Fatalf("应填充 ISP: %+v", ups)
		}
		if ups[0].Province != nil || ups[0].City != nil {
			t.Fatalf("省市只差后缀，不该改: %+v", ups[0])
		}
		if k.isp != 1 || conf.n != 0 {
			t.Fatalf("统计不对: %+v conf=%d", k, conf.n)
		}
	})

	t.Run("省变了市必须跟着改", func(t *testing.T) {
		segs := []geo.Segment{seg(100, 200, "广东省", "广州", "中国电信")}
		ups, k, _ := decide(segs, &probe{segIdx: []int{0}},
			provider.Result{Province: "广西壮族自治区", City: "南宁市", ISP: "中国电信"}, true)
		if len(ups) != 1 || ups[0].Province == nil || *ups[0].Province != "广西壮族自治区" {
			t.Fatalf("应改省: %+v", ups)
		}
		if ups[0].City == nil || *ups[0].City != "南宁市" {
			t.Fatalf("省改了市必须同步: %+v", ups)
		}
		if k.prov != 1 || k.city != 1 {
			t.Fatalf("统计不对: %+v", k)
		}
	})

	t.Run("省变了但百度没给市且原有市则不动", func(t *testing.T) {
		segs := []geo.Segment{seg(100, 200, "广东省", "广州", "")}
		ups, _, _ := decide(segs, &probe{segIdx: []int{0}},
			provider.Result{Province: "海南省"}, true)
		if len(ups) != 0 {
			t.Fatalf("百度给不出市属低置信度，整条不该动: %+v", ups)
		}
	})

	t.Run("云类段不覆盖省市只补空", func(t *testing.T) {
		segs := []geo.Segment{
			seg(100, 200, "广东省", "广州", "腾讯云"),
			seg(201, 300, "", "", "腾讯云"),
		}
		ups, k, _ := decide(segs, &probe{segIdx: []int{0, 1}},
			provider.Result{Province: "北京市", City: "北京市", ISP: "中国电信"}, true)
		if len(ups) != 1 || ups[0].Start != 201 {
			t.Fatalf("只该给空白段补值，实际 %+v", ups)
		}
		if ups[0].Province == nil || *ups[0].Province != "北京市" {
			t.Fatalf("空白段应被填充: %+v", ups[0])
		}
		if k.prov != 1 || k.city != 1 {
			t.Fatalf("统计不对: %+v", k)
		}
	})

	t.Run("默认只补空不覆盖", func(t *testing.T) {
		segs := []geo.Segment{seg(100, 200, "广东省", "广州", "")}
		ups, k, _ := decide(segs, &probe{segIdx: []int{0}},
			provider.Result{Province: "北京市", City: "北京市", ISP: "中国电信"}, false)
		if len(ups) != 1 || ups[0].Province != nil || ups[0].City != nil {
			t.Fatalf("默认不该覆盖已有省市: %+v", ups)
		}
		if ups[0].ISP == nil || *ups[0].ISP != "中国电信" {
			t.Fatalf("空 ISP 仍应补: %+v", ups[0])
		}
		if k.prov != 0 || k.city != 0 || k.isp != 1 {
			t.Fatalf("统计不对: %+v", k)
		}
	})

	t.Run("百度没数据时不动", func(t *testing.T) {
		segs := []geo.Segment{seg(100, 200, "广东省", "广州", "")}
		ups, _, _ := decide(segs, &probe{segIdx: []int{0}}, provider.Result{Raw: "中国"}, true)
		if len(ups) != 0 {
			t.Fatalf("空结果不该产生更新: %+v", ups)
		}
	})

	t.Run("ISP 冲突只记录不写", func(t *testing.T) {
		segs := []geo.Segment{seg(100, 200, "广东省", "广州", "腾讯云")}
		ups, _, conf := decide(segs, &probe{segIdx: []int{0}},
			provider.Result{Province: "广东省", City: "广州", ISP: "中国电信"}, true)
		if len(ups) != 0 {
			t.Fatalf("冲突不该写库: %+v", ups)
		}
		if conf.n != 1 || conf.sample == "" {
			t.Fatalf("应记录冲突: %+v", conf)
		}
	})

	t.Run("同 /24 多段共享一次结果", func(t *testing.T) {
		segs := []geo.Segment{
			seg(100, 120, "广东省", "广州", ""),
			seg(121, 140, "广东省", "广州", ""),
		}
		ups, k, _ := decide(segs, &probe{segIdx: []int{0, 1}},
			provider.Result{Province: "广东省", City: "广州市", ISP: "中国联通"}, true)
		if len(ups) != 2 || k.isp != 2 {
			t.Fatalf("两段都应更新: %+v k=%+v", ups, k)
		}
	})
}

func TestTrimAdminSuffix(t *testing.T) {
	cases := map[string]string{
		"广州市": "广州", "广州": "广州", "番禺区": "番禺",
		"内蒙古自治区": "内蒙古", "香港特别行政区": "香港",
	}
	for in, want := range cases {
		if got := trimAdminSuffix(in); got != want {
			t.Errorf("trimAdminSuffix(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFillPairKeepsProvinceCityConsistent(t *testing.T) {
	cases := []struct {
		name             string
		prov, city       string
		resProv, resCity string
		wantProv, wantCity string
	}{
		{"省有市空且百度省不一致：不填市", "江西省", "", "广东省", "广州市", "江西省", ""},
		{"省有市空且百度省一致：填市", "广东省", "", "广东省", "广州市", "广东省", "广州市"},
		{"省空市有且百度市一致：填省", "", "广州市", "广东省", "广州市", "广东省", "广州市"},
		{"省空市有且百度市不同：不填省", "", "广州市", "江西省", "南昌市", "", "广州市"},
		{"省市都空：成对填", "", "", "广东省", "广州市", "广东省", "广州市"},
		{"省市都有：补空不动", "江西省", "南昌市", "广东省", "广州市", "江西省", "南昌市"},
		{"百度只给省且市为空：填省", "", "", "广东省", "", "广东省", ""},
	}
	for _, c := range cases {
		g := &geo.GeoInfo{Country: "中国", Province: c.prov, City: c.city}
		FillPair(g, provider.Result{Province: c.resProv, City: c.resCity})
		if g.Province != c.wantProv || g.City != c.wantCity {
			t.Errorf("%s: got %s|%s want %s|%s",
				c.name, g.Province, g.City, c.wantProv, c.wantCity)
		}
	}
}
