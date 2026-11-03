// Package qqwry 解析纯真 IP 数据库（qqwry.dat，CZ88 格式）。
//
// 文件结构：
//
//	[0:4]   首个索引偏移（小端 uint32）
//	[4:8]   末个索引偏移（小端 uint32）
//	索引区: 每项 7 字节 = 4 字节起始 IP（小端）+ 3 字节记录偏移（小端）
//	记录区: 4 字节结束 IP（小端）+ 归属信息
//
// 归属信息解析（LumaQQ 文档定义）：
//	mode=0x01: 后 3 字节是绝对偏移，跳过去按同规则解析 国家+地区
//	mode=0x02: 后 3 字节是国家串偏移；地区在本记录 mode 字节 +4 处按同规则解析
//	其他:      国家串从 mode 字节处直读到 \0；紧随其后的 1 字节若是 0x01/0x02
//	           则是地区串的 3 字节偏移，否则直接是地区串
//
// 旧实现 (_legacy/qqwry_reader.go) 把 0x01 与 0x02 一律当跳转处理，
// 导致 0x02 记录在国家串结束后把数据区里下一条字符串误读成 ISP。
package qqwry

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const (
	redirectMode1 = 0x01 // 整体跳转（国家+地区）
	redirectMode2 = 0x02 // 仅国家跳转
	indexEntryLen = 7
)

// Record 是一条原始记录，字符串已按 GB18030 解码为 UTF-8。
type Record struct {
	Start   uint32
	End     uint32
	Country string // 国别串，可能包含省市区镇连写
	Area    string // 地区/运营商串
}

type parser struct {
	data []byte
}

// Parse 解析整库。
func Parse(dat []byte) ([]Record, error) {
	if len(dat) < 8 {
		return nil, fmt.Errorf("文件过小: %d 字节", len(dat))
	}
	p := &parser{data: dat}
	firstIdx := binary.LittleEndian.Uint32(dat[:4])
	lastIdx := binary.LittleEndian.Uint32(dat[4:8])
	if firstIdx > lastIdx || int(lastIdx)+indexEntryLen > len(dat) {
		return nil, fmt.Errorf("索引区偏移异常: first=%d last=%d size=%d", firstIdx, lastIdx, len(dat))
	}

	total := (int(lastIdx)-int(firstIdx))/indexEntryLen + 1
	records := make([]Record, 0, total)
	for off := int(firstIdx); off+indexEntryLen <= int(lastIdx)+indexEntryLen; off += indexEntryLen {
		start := binary.LittleEndian.Uint32(dat[off : off+4])
		recOff := int(b3(dat[off+4 : off+7]))
		if recOff+4 > len(dat) {
			return nil, fmt.Errorf("记录偏移越界: %d", recOff)
		}
		end := binary.LittleEndian.Uint32(dat[recOff : recOff+4])
		country, area, err := p.parseCountryArea(recOff+4, 0)
		if err != nil {
			return nil, fmt.Errorf("解析记录 offset=%d 失败: %w", recOff, err)
		}
		records = append(records, Record{
			Start:   start,
			End:     end,
			Country: cleanText(country),
			Area:    cleanText(area),
		})
	}
	return records, nil
}

// ParseFile 读取并解析 dat 文件。
func ParseFile(path string) ([]Record, error) {
	dat, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(dat)
}

// parseCountryArea 在 pos 处解析 国家+地区。
func (p *parser) parseCountryArea(pos int, depth int) (string, string, error) {
	if pos >= len(p.data) {
		return "", "", fmt.Errorf("偏移越界: %d", pos)
	}
	if depth > 3 {
		return "", "", fmt.Errorf("跳转层数过深")
	}
	mode := p.data[pos]
	switch mode {
	case redirectMode1:
		return p.parseCountryArea(int(b3(p.data[pos+1:pos+4])), depth+1)
	case redirectMode2:
		country := p.readString(int(b3(p.data[pos+1 : pos+4])))
		area, err := p.parseArea(pos+4, depth)
		return country, area, err
	default:
		country, _, _ := strings.Cut(p.readRaw(pos), "\x00")
		area, err := p.parseArea(pos+len(country)+1, depth)
		return country, area, err
	}
}

// parseArea 在 pos 处解析地区串。pos 可能是 mode 字节，也可能直接是串首字节。
func (p *parser) parseArea(pos int, depth int) (string, error) {
	if pos >= len(p.data) {
		return "", nil
	}
	mode := p.data[pos]
	switch mode {
	case redirectMode1, redirectMode2:
		return p.readString(int(b3(p.data[pos+1 : pos+4]))), nil
	default:
		s, _, _ := strings.Cut(p.readRaw(pos), "\x00")
		return s, nil
	}
}

// readString 读取 GBK/GB18030 编码的 \0 结尾字符串并解码。
func (p *parser) readString(pos int) string {
	s, _, _ := strings.Cut(p.readRaw(pos), "\x00")
	return s
}

// readRaw 返回从 pos 开始到 \0 或数据末尾的原始字节（未解码）。
func (p *parser) readRaw(pos int) string {
	if pos < 0 || pos >= len(p.data) {
		return ""
	}
	end := pos
	for end < len(p.data) && p.data[end] != 0 {
		end++
	}
	return string(p.data[pos:end])
}

func b3(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}

var gbkDecoder = simplifiedchinese.GB18030.NewDecoder()

// decodeGBK 把 GB18030 字节流转成 UTF-8。
func decodeGBK(s string) string {
	out, _, err := transform.Bytes(gbkDecoder, []byte(s))
	if err != nil {
		return s // 解码失败时保留原文，不丢记录
	}
	return string(out)
}

// cleanText 清理控制字符与多余空白。
func cleanText(s string) string {
	s = decodeGBK(s)
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
