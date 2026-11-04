package ispip

import (
	"os"
	"path/filepath"
	"testing"
)

func writePage(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndLookup(t *testing.T) {
	dir := t.TempDir()
	// 同一块 1.12.0.0/14 同时出现在电信和腾讯云列表里
	writePage(t, dir, "chinatelecom.html", "1.12.0.0/16<br>2.0.0.0/16<br>")
	writePage(t, dir, "tencent.html", "1.12.0.0/14<br>")
	// 兜底桶不能当 ISP 用
	writePage(t, dir, "othernet.html", "9.9.9.0/24<br>")
	// 未知页面忽略
	writePage(t, dir, "unknown.html", "8.8.8.0/24<br>")

	tab, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tab.Networks != 3 {
		t.Fatalf("应解析 3 条 CIDR，实际 %d", tab.Networks)
	}

	cases := map[string]string{
		"1.12.0.1":  "腾讯云",  // 云厂商优先于基础运营商
		"1.15.255.255": "腾讯云", // /14 覆盖 1.12–1.15
		"2.0.0.1":    "中国电信", // 只在电信列表里
		"9.9.9.1":   "",      // othernet 不算 ISP
		"8.8.8.1":   "",      // 未知页面忽略
	}
	for ip, want := range cases {
		if got := tab.Lookup(mustIP(ip)); got != want {
			t.Errorf("Lookup(%s)=%q want %q", ip, got, want)
		}
	}
}

func TestLoadEmptyDir(t *testing.T) {
	if _, err := LoadDir(t.TempDir()); err == nil {
		t.Fatal("空目录应报错")
	}
}

func mustIP(s string) uint32 {
	var a, b, c, d uint32
	for i, p := range splitDots(s) {
		switch i {
		case 0:
			a = p
		case 1:
			b = p
		case 2:
			c = p
		case 3:
			d = p
		}
	}
	return a<<24 | b<<16 | c<<8 | d
}

func splitDots(s string) []uint32 {
	var out []uint32
	cur := uint32(0)
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, cur)
			cur = 0
			continue
		}
		cur = cur*10 + uint32(s[i]-'0')
	}
	return append(out, cur)
}
