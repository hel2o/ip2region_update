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

func gi(prov, city, isp string) geo.GeoInfo {
	return geo.GeoInfo{Country: "中国", Province: prov, City: city, ISP: isp}
}

func TestDecidePart(t *testing.T) {
	t.Run("ISP 为空时填充", func(t *testing.T) {
		out, k, conf := decidePart(gi("广东省", "广州", ""),
			provider.Result{Province: "广东省", City: "广州市", ISP: "中国电信"}, true)
		if out.ISP != "中国电信" {
			t.Fatalf("应填充 ISP: %+v", out)
		}
		if out.Province != "广东省" || out.City != "广州" {
			t.Fatalf("省市只差后缀，不该改: %+v", out)
		}
		if k.isp != 1 || conf {
			t.Fatalf("统计不对: %+v conf=%v", k, conf)
		}
	})

	t.Run("省变了市必须跟着改", func(t *testing.T) {
		out, k, _ := decidePart(gi("广东省", "广州", "中国电信"),
			provider.Result{Province: "广西壮族自治区", City: "南宁市", ISP: "中国电信"}, true)
		if out.Province != "广西壮族自治区" || out.City != "南宁市" {
			t.Fatalf("省改了市必须同步: %+v", out)
		}
		if k.prov != 1 || k.city != 1 {
			t.Fatalf("统计不对: %+v", k)
		}
	})

	t.Run("省变了但百度没给市且原有市则不动", func(t *testing.T) {
		out, _, _ := decidePart(gi("广东省", "广州", ""),
			provider.Result{Province: "海南省"}, true)
		if out.Province != "广东省" || out.City != "广州" {
			t.Fatalf("百度给不出市属低置信度，整条不该动: %+v", out)
		}
	})

	t.Run("云类段不覆盖省市只补空", func(t *testing.T) {
		// 已有值：不动
		out, _, _ := decidePart(gi("广东省", "广州", "腾讯云"),
			provider.Result{Province: "北京市", City: "北京市"}, true)
		if out.Province != "广东省" || out.City != "广州" {
			t.Fatalf("云段不该被覆盖: %+v", out)
		}
		// 空白：填
		out2, k, _ := decidePart(gi("", "", "腾讯云"),
			provider.Result{Province: "北京市", City: "北京市"}, true)
		if out2.Province != "北京市" || out2.City != "北京市" {
			t.Fatalf("空白段应被填充: %+v", out2)
		}
		if k.prov != 1 || k.city != 1 {
			t.Fatalf("统计不对: %+v", k)
		}
	})

	t.Run("override=false 只补空不覆盖", func(t *testing.T) {
		out, k, _ := decidePart(gi("广东省", "广州", ""),
			provider.Result{Province: "北京市", City: "北京市", ISP: "中国电信"}, false)
		if out.Province != "广东省" || out.City != "广州" {
			t.Fatalf("不该覆盖已有省市: %+v", out)
		}
		if out.ISP != "中国电信" {
			t.Fatalf("空 ISP 仍应补: %+v", out)
		}
		if k.prov != 0 || k.city != 0 || k.isp != 1 {
			t.Fatalf("统计不对: %+v", k)
		}
	})

	t.Run("百度没数据时不动", func(t *testing.T) {
		out, _, _ := decidePart(gi("广东省", "广州", ""),
			provider.Result{Raw: "中国"}, true)
		if out != gi("广东省", "广州", "") {
			t.Fatalf("空结果不该改动: %+v", out)
		}
	})

	t.Run("ISP 冲突只记录不写", func(t *testing.T) {
		out, _, conf := decidePart(gi("广东省", "广州", "腾讯云"),
			provider.Result{Province: "广东省", City: "广州", ISP: "中国电信"}, true)
		if out.ISP != "腾讯云" {
			t.Fatalf("冲突不该写库: %+v", out)
		}
		if !conf {
			t.Fatal("应报告冲突")
		}
	})

	t.Run("改市时清空旧区县和镇", func(t *testing.T) {
		base := gi("广东省", "广州", "中国电信")
		base.County = "番禺区"
		base.Town = "钟村"
		out, k, _ := decidePart(base,
			provider.Result{Province: "广东省", City: "深圳市"}, true)
		if out.City != "深圳市" {
			t.Fatalf("应换市: %+v", out)
		}
		if out.County != "" || out.Town != "" {
			t.Fatalf("换市后旧区县/镇必须清空: %+v", out)
		}
		if k.countyCleared != 1 {
			t.Fatalf("统计不对: %+v", k)
		}
	})
}

func TestProbeCount(t *testing.T) {
	opt := Options{ProbeStep: 4096, ProbeCap: 16}
	cases := []struct {
		start, end uint32
		want       int
	}{
		{0, 255, 1},        // 一个 /24，1 个点
		{0, 4095, 1},       // 正好一步
		{0, 4096, 2},       // 多一个 IP 就两片
		{0, 65535, 16},     // /16 → 16 片（封顶）
		{0, 0xFFFFFFFF, 16}, // 全域 → 仍然 16 片
	}
	for _, c := range cases {
		sg := geo.Segment{Start: c.start, End: c.end}
		if got := probeCount(opt, &sg); got != c.want {
			t.Errorf("probeCount(%d-%d)=%d want %d", c.start, c.end, got, c.want)
		}
	}
	// ProbeStep=0 表示退回"每段一个中点"
	if got := probeCount(Options{}, &geo.Segment{Start: 0, End: 0xFFFFFF}); got != 1 {
		t.Errorf("未设步长时应只探一个点，实际 %d", got)
	}
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
