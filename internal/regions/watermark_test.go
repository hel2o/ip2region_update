package regions

import "testing"

func TestStripWatermark(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"CZ88.NET", ""},
		{"cz88.net", ""},
		{"www.cz88.net", ""},
		{"沙田区/CZ88.NET", "沙田区"},
		{"大堂区/CZ88.NET", "大堂区"},
		{"CZ88.NET 中国电信", "中国电信"},
		{"中国电信", "中国电信"},
		{"日本", "日本"},
		{"/CZ88.NET/", ""},
		{"CZ88.NET/CZ88.NET", ""},
	}
	for _, c := range cases {
		if got := StripWatermark(c.in); got != c.want {
			t.Errorf("StripWatermark(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitDropsWatermark(t *testing.T) {
	// 纯真境外记录形如 Country="泰国", Area="CZ88.NET"；
	// 也有国家和水印粘在一个串里的情况。
	if p := Split("泰国 CZ88.NET"); p.Country != "泰国" {
		t.Errorf("Split 未清理水印: %+v", p)
	}
	if p := Split("CZ88.NET"); p.Country != "" || p.City != "" || p.Town != "" {
		t.Errorf("纯水印串应拆成空: %+v", p)
	}
	if p := Split("泰国–CZ88.NET"); HasWatermark(p.City) || HasWatermark(p.Town) {
		t.Errorf("分段后仍有水印: %+v", p)
	}
}
