// Copyright 2022 The Ip2Region Authors. All rights reserved.
// Use of this source code is governed by a Apache2.0-style
// license that can be found in the LICENSE file.

// ---
// @Author Lion <chenxin619315@gmail.com>
// @Date   2022/06/16

package xdbmaker

import (
	"bufio"
	"embed"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var shiftIndex = []int{24, 16, 8, 0}

func CheckIP(ip string) (uint32, error) {
	var ps = strings.Split(strings.TrimSpace(ip), ".")
	if len(ps) != 4 {
		return 0, fmt.Errorf("invalid ip address `%s`", ip)
	}

	var val = uint32(0)
	for i, s := range ps {
		d, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("the %dth part `%s` is not an integer", i, s)
		}

		if d < 0 || d > 255 {
			return 0, fmt.Errorf("the %dth part `%s` should be an integer bettween 0 and 255", i, s)
		}

		val |= uint32(d) << shiftIndex[i]
	}

	// convert the ip to integer
	return val, nil
}

func Long2IP(ip uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", (ip>>24)&0xFF, (ip>>16)&0xFF, (ip>>8)&0xFF, ip&0xFF)
}

func MidIP(sip uint32, eip uint32) uint32 {
	return uint32((uint64(sip) + uint64(eip)) >> 1)
}

// LoadHeader load the header info from the specified handle
func LoadHeader(handle *os.File) (*Header, error) {
	_, err := handle.Seek(0, 0)
	if err != nil {
		return nil, fmt.Errorf("seek to the header: %w", err)
	}

	var buff = make([]byte, HeaderInfoLength)
	rLen, err := handle.Read(buff)
	if err != nil {
		return nil, err
	}

	if rLen != len(buff) {
		return nil, fmt.Errorf("incomplete read: readed bytes should be %d", len(buff))
	}

	return NewHeader(buff)
}

// LoadHeaderFromFile load header info from the specified db file path
func LoadHeaderFromFile(dbFile string) (*Header, error) {
	handle, err := os.OpenFile(dbFile, os.O_RDONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open xdb file `%s`: %w", dbFile, err)
	}

	defer func(handle *os.File) {
		_ = handle.Close()
	}(handle)

	header, err := LoadHeader(handle)
	if err != nil {
		return nil, err
	}

	return header, nil
}

// LoadHeaderFromBuff wrap the header info from the content buffer
func LoadHeaderFromBuff(cBuff []byte) (*Header, error) {
	return NewHeader(cBuff[0:256])
}

// LoadVectorIndex util function to load the vector index from the specified file handle
func LoadVectorIndex(handle *os.File) ([]byte, error) {
	// load all the vector index block
	_, err := handle.Seek(HeaderInfoLength, 0)
	if err != nil {
		return nil, fmt.Errorf("seek to vector index: %w", err)
	}

	var buff = make([]byte, VectorIndexRows*VectorIndexCols*VectorIndexSize)
	rLen, err := handle.Read(buff)
	if err != nil {
		return nil, err
	}

	if rLen != len(buff) {
		return nil, fmt.Errorf("incomplete read: readed bytes should be %d", len(buff))
	}

	return buff, nil
}

// LoadVectorIndexFromFile load vector index from a specified file path
func LoadVectorIndexFromFile(dbFile string) ([]byte, error) {
	handle, err := os.OpenFile(dbFile, os.O_RDONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open xdb file `%s`: %w", dbFile, err)
	}

	defer func() {
		_ = handle.Close()
	}()

	vIndex, err := LoadVectorIndex(handle)
	if err != nil {
		return nil, err
	}

	return vIndex, nil
}

// LoadContent load the whole xdb content from the specified file handle
func LoadContent(handle *os.File) ([]byte, error) {
	// get file size
	fi, err := handle.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}

	size := fi.Size()

	// seek to the head of the file
	_, err = handle.Seek(0, 0)
	if err != nil {
		return nil, fmt.Errorf("seek to get xdb file length: %w", err)
	}

	var buff = make([]byte, size)
	rLen, err := handle.Read(buff)
	if err != nil {
		return nil, err
	}

	if rLen != len(buff) {
		return nil, fmt.Errorf("incomplete read: readed bytes should be %d, but got %d", len(buff), rLen)
	}

	return buff, nil
}

// LoadContentFromFile load the whole xdb content from the specified db file path
func LoadContentFromFile(dbFile string) ([]byte, error) {
	handle, err := os.OpenFile(dbFile, os.O_RDONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open xdb file `%s`: %w", dbFile, err)
	}

	defer func() {
		_ = handle.Close()
	}()

	cBuff, err := LoadContent(handle)
	if err != nil {
		return nil, err
	}

	return cBuff, nil
}

// LoadContentFromFS load the whole xdb binary from embed.FS
func LoadContentFromFS(fs embed.FS, filePath string) ([]byte, error) {
	file, err := fs.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open embedded file `%s`: %w", filePath, err)
	}
	defer file.Close()

	var cBuff []byte
	cBuff, err = io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded file `%s`: %w", filePath, err)
	}

	return cBuff, nil
}

// ---- 以下函数取自 maker/golang/xdb/util.go（官方实现，Apache 2.0）----

// IterateSegments 迭代源 txt 文件中的数据段，并自动合并连续且 region 相同的段。
func IterateSegments(handle *os.File, before func(l string), cb func(seg *Segment) error) error {
	var last *Segment = nil
	var scanner = bufio.NewScanner(handle)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		var l = strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\n"))
		if len(l) < 1 { // ignore empty line
			continue
		}

		if l[0] == '#' { // ignore the comment line
			continue
		}

		if before != nil {
			before(l)
		}

		var ps = strings.SplitN(l, "|", 3)
		if len(ps) != 3 {
			return fmt.Errorf("invalid ip segment line `%s`", l)
		}

		sip, err := CheckIP(ps[0])
		if err != nil {
			return fmt.Errorf("check start ip `%s`: %s", ps[0], err)
		}

		eip, err := CheckIP(ps[1])
		if err != nil {
			return fmt.Errorf("check end ip `%s`: %s", ps[1], err)
		}

		if sip > eip {
			return fmt.Errorf("start ip(%s) should not be greater than end ip(%s)", ps[0], ps[1])
		}

		if len(ps[2]) < 1 {
			return fmt.Errorf("empty region info in segment line `%s`", l)
		}

		var seg = &Segment{
			StartIP: sip,
			EndIP:   eip,
			Region:  ps[2],
		}

		// check and automatic merging the Consecutive Segments, which means:
		// 1, region info is the same
		// 2, last.eip+1 = cur.sip
		if last == nil {
			last = seg
			continue
		} else if last.Region == seg.Region {
			if err = seg.AfterCheck(last); err == nil {
				last.EndIP = seg.EndIP
				continue
			}
		}

		if err = cb(last); err != nil {
			return err
		}

		// reset the last
		last = seg
	}

	// process the last segment
	if last != nil {
		return cb(last)
	}

	return nil
}

// CheckSegments 校验段列表的连续性。
func CheckSegments(segList []*Segment) error {
	var last *Segment
	for _, seg := range segList {
		// sip must <= eip
		if seg.StartIP > seg.EndIP {
			return fmt.Errorf("segment `%s`: start ip should not be greater than end ip", seg.String())
		}

		// check the continuity of the data segment
		if last != nil {
			if last.EndIP+1 != seg.StartIP {
				return fmt.Errorf("discontinuous segment `%s`: last.eip+1 != cur.sip", seg.String())
			}
		}

		last = seg
	}

	return nil
}
