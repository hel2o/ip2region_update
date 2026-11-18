package qqwry

import (
	"bytes"
	"encoding/binary"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// makeSynthetic 构造一个最小可用的 qqwry.dat，覆盖四种记录形态：
// 纯串 / 0x02 国家跳转 / 0x01 整体跳转 / 0x02 国家跳转 + 0x02 地区跳转。
func makeSynthetic(t *testing.T) []byte {
	t.Helper()

	enc := simplifiedchinese.GB18030.NewEncoder()
	gbk := func(s string) []byte {
		out, _, err := transform.Bytes(enc, []byte(s))
		if err != nil {
			t.Fatalf("GBK 编码失败: %v", err)
		}
		return out
	}
	cstr := func(s string) []byte { return append(gbk(s), 0) }
	p3 := func(off uint32) []byte {
		b := make([]byte, 3)
		b[0] = byte(off)
		b[1] = byte(off >> 8)
		b[2] = byte(off >> 16)
		return b
	}

	type recSpec struct {
		start, end uint32
		build      func(strOff func(string) uint32) []byte
	}

	dummy := func(string) uint32 { return 0 }
	records := []recSpec{
		// 1. 纯串：国家 + 地区直接内联
		{ip("1.0.1.0"), ip("1.0.3.255"), func(so func(string) uint32) []byte {
			return append(cstr("中国广东省广州市"), cstr("电信")...)
		}},
		// 2. 0x02 国家跳转 + 地区直串
		{ip("8.8.8.0"), ip("8.8.8.255"), func(so func(string) uint32) []byte {
			out := []byte{redirectMode2}
			out = append(out, p3(so("美国"))...)
			return append(out, cstr("谷歌")...)
		}},
		// 3. 0x01 整体跳转：目标处是 国家\0 + 地区串
		{ip("114.114.0.0"), ip("114.114.114.255"), func(so func(string) uint32) []byte {
			// 跳转目标也是一条"内联记录"，放进字符串区统一分配偏移
			return append([]byte{redirectMode1}, p3(so("\x01target3"))...)
		}},
		// 4. 0x02 国家跳转 + 0x02 地区跳转
		{ip("223.5.5.0"), ip("223.5.5.255"), func(so func(string) uint32) []byte {
			out := []byte{redirectMode2}
			out = append(out, p3(so("中国浙江省杭州市"))...)
			out = append(out, redirectMode2)
			out = append(out, p3(so("阿里云"))...)
			return out
		}},
		// 5. 保留地址，地区为空
		{ip("10.0.0.0"), ip("10.255.255.255"), func(so func(string) uint32) []byte {
			return append(cstr("保留地址"), cstr("")...)
		}},
	}

	// pass1: 计算记录区大小（loc 长度与偏移值无关，用 dummy 即可）
	recAreaSize := 0
	locSizes := make([]int, len(records))
	for i, r := range records {
		locSizes[i] = len(r.build(dummy))
		recAreaSize += 4 + locSizes[i]
	}

	// 字符串区基址；特殊 key 以 \x01 开头表示"内联记录"，见下
	strBase := uint32(8 + len(records)*7 + recAreaSize)

	var strArea bytes.Buffer
	strOffsets := map[string]uint32{}
	strOff := func(s string) uint32 {
		if off, ok := strOffsets[s]; ok {
			return off
		}
		off := strBase + uint32(strArea.Len())
		strOffsets[s] = off
		if strings_HasPrefix01(s) {
			// 内联记录：114 案例的 0x01 跳转目标 = 国家\0 + 地区\0
			strArea.Write(cstr("中国江苏省南京市"))
			strArea.Write(cstr("联通"))
			return off
		}
		strArea.Write(cstr(s))
		return off
	}

	// pass2: 真实构建
	var recArea bytes.Buffer
	var recOffsets []uint32
	for i, r := range records {
		recOffsets = append(recOffsets, uint32(8+len(records)*7+recArea.Len()))
		var endBuf [4]byte
		binary.LittleEndian.PutUint32(endBuf[:], r.end)
		recArea.Write(endBuf[:])
		loc := r.build(strOff)
		if len(loc) != locSizes[i] {
			t.Fatalf("记录 %d 两遍构建长度不一致", i)
		}
		recArea.Write(loc)
	}

	var out bytes.Buffer
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 8)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(8+len(records)*7-7))
	out.Write(hdr[:])
	for i, r := range records {
		var startBuf [4]byte
		binary.LittleEndian.PutUint32(startBuf[:], r.start)
		out.Write(startBuf[:])
		out.Write(p3(recOffsets[i]))
	}
	out.Write(recArea.Bytes())
	out.Write(strArea.Bytes())
	return out.Bytes()
}

func strings_HasPrefix01(s string) bool {
	return len(s) > 0 && s[0] == 0x01
}

func TestParseSynthetic(t *testing.T) {
	dat := makeSynthetic(t)
	recs, err := Parse(dat)
	if err != nil {
		t.Fatal(err)
	}
	want := []Record{
		{Start: ip("1.0.1.0"), End: ip("1.0.3.255"), Country: "中国广东省广州市", Area: "电信"},
		{Start: ip("8.8.8.0"), End: ip("8.8.8.255"), Country: "美国", Area: "谷歌"},
		{Start: ip("114.114.0.0"), End: ip("114.114.114.255"), Country: "中国江苏省南京市", Area: "联通"},
		{Start: ip("223.5.5.0"), End: ip("223.5.5.255"), Country: "中国浙江省杭州市", Area: "阿里云"},
		{Start: ip("10.0.0.0"), End: ip("10.255.255.255"), Country: "保留地址", Area: ""},
	}
	if len(recs) != len(want) {
		t.Fatalf("记录数不符: got %d want %d", len(recs), len(want))
	}
	for i, w := range want {
		if recs[i] != w {
			t.Errorf("第 %d 条不符:\n got=%+v\nwant=%+v", i, recs[i], w)
		}
	}
}

func ip(s string) uint32 {
	var b [4]byte
	parts := bytes.Split([]byte(s), []byte("."))
	for i, p := range parts {
		v := 0
		for _, c := range p {
			v = v*10 + int(c-'0')
		}
		b[i] = byte(v)
	}
	return binary.BigEndian.Uint32(b[:])
}
