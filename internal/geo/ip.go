package geo

import (
	"encoding/binary"
	"fmt"
	"net"
)

// IP2Uint 把 IPv4 字符串转成 uint32。IPv6 或非法输入返回 0,false。
func IP2Uint(s string) (uint32, bool) {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(ip), true
}

// MustIP2Uint 同 IP2Uint，但失败时 panic，仅用于确认过合法性的场景。
func MustIP2Uint(s string) uint32 {
	v, ok := IP2Uint(s)
	if !ok {
		panic("非法 IPv4 地址: " + s)
	}
	return v
}

// Uint2IP 把 uint32 转成 IPv4 字符串。
func Uint2IP(v uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return net.IP(b[:]).String()
}

// ParseCIDRRange 把 CIDR 展开成闭区间。
func ParseCIDRRange(cidr string) (start, end uint32, err error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, 0, fmt.Errorf("非法 CIDR %q: %w", cidr, err)
	}
	if ipnet.IP.To4() == nil {
		return 0, 0, fmt.Errorf("不支持 IPv6 CIDR: %q", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return 0, 0, fmt.Errorf("非法掩码长度: %q", cidr)
	}
	start = binary.BigEndian.Uint32(ipnet.IP.To4())
	end = start | (MaxIP >> uint(ones))
	return start, end, nil
}

// Prefix 判断 v 是否落在该段内。
func (s Segment) Contains(v uint32) bool { return v >= s.Start && v <= s.End }

// Find 在已排序段表中二分查找 v 所在段，未命中返回 nil。
func Find(segs []Segment, v uint32) *Segment {
	lo, hi := 0, len(segs)-1
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		switch {
		case v < segs[mid].Start:
			hi = mid - 1
		case v > segs[mid].End:
			lo = mid + 1
		default:
			return &segs[mid]
		}
	}
	return nil
}
