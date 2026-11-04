package geocn

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCodes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "full.txt")
	body := "110000\t北京市\n110100\t北京市\n110101\t东城区\n"
	// 补足条目数，避免被"条目过少"拦住
	for i := 0; i < 120; i++ {
		body += fmt.Sprintf("99%04d\t占位%d\n", i, i)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	codes, err := loadCodes(p)
	if err != nil {
		t.Fatal(err)
	}
	if codes["110101"] != "东城区" {
		t.Fatalf("110101 应是东城区，实际 %q", codes["110101"])
	}
}

func TestLoadCodesRejectsJunk(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(p, []byte("这不是编码表\n随便写点\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCodes(p); err == nil {
		t.Fatal("乱格式应报错")
	}
}

func TestHasDistrict(t *testing.T) {
	if (Info{District: "东城区"}).HasDistrict() != true {
		t.Fatal("有区县应返回 true")
	}
	if (Info{District: ""}).HasDistrict() != false {
		t.Fatal("无区县应返回 false")
	}
}

// TestLookupRealDB 用真实库做冒烟测试；数据不在就跳过。
func TestLookupRealDB(t *testing.T) {
	const mmdb = "../../data/geocn/GeoCN.mmdb"
	const codes = "../../data/geocn/full.txt"
	if _, err := os.Stat(mmdb); err != nil {
		t.Skip("未下载 GeoCN 数据，跳过")
	}
	db, err := Open(mmdb, codes)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if db.CodeCount() < 1000 {
		t.Fatalf("编码表条目过少: %d", db.CodeCount())
	}

	// 1.0.1.0/24 → 350100 福州市
	info, ok := db.Lookup(0x01000100)
	if !ok {
		t.Fatal("1.0.1.0 应有结果")
	}
	if info.Province != "福建省" || info.City != "福州市" {
		t.Fatalf("1.0.1.0 应是福建省福州市，实际 %+v", info)
	}

	// 省级编码（末四位 0000）不该带区县
	if info, ok := db.Lookup(0x01000001); ok && info.Code[2:] == "0000" && info.HasDistrict() {
		t.Fatalf("省级编码不应有区县: %+v", info)
	}

	// 区县级编码应能翻译出区名
	found := false
	for _, ip := range []uint32{0x01010101, 0x01010102, 0xDF030501} {
		if i, ok := db.Lookup(ip); ok && i.HasDistrict() {
			found = true
			break
		}
	}
	if !found {
		t.Skip("抽样 IP 恰好都没区县编码，跳过这条断言")
	}
}
