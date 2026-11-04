package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/hel2o/ip2region/internal/geo"
)

// CachedResult 是 ip_cache 表的一行。
type CachedResult struct {
	IP        uint32
	Provider  string
	Province  string
	City      string
	District  string
	ISP       string
	Raw       string
	FetchedAt int64
}

// GetCache 按精确 IP 读缓存。第二个返回值表示命中。
func (s *DB) GetCache(ip uint32, provider string) (CachedResult, bool, error) {
	row := s.db.QueryRow(`SELECT ip, provider, province, city, district, isp, raw, fetched_at
		FROM ip_cache WHERE ip = ? AND provider = ?`, int64(ip), provider)
	var r CachedResult
	var ipv int64
	err := row.Scan(&ipv, &r.Provider, &r.Province, &r.City, &r.District, &r.ISP, &r.Raw, &r.FetchedAt)
	if err == sql.ErrNoRows {
		return CachedResult{}, false, nil
	}
	if err != nil {
		return CachedResult{}, false, err
	}
	r.IP = uint32(ipv)
	return r, true, nil
}

// GetCacheInRange 在 [lo,hi] 内找一条已有缓存（用于 /24 级复用）。
func (s *DB) GetCacheInRange(lo, hi uint32, provider string) (CachedResult, bool, error) {
	row := s.db.QueryRow(`SELECT ip, provider, province, city, district, isp, raw, fetched_at
		FROM ip_cache WHERE ip BETWEEN ? AND ? AND provider = ?
		ORDER BY fetched_at DESC LIMIT 1`, int64(lo), int64(hi), provider)
	var r CachedResult
	var ipv int64
	err := row.Scan(&ipv, &r.Provider, &r.Province, &r.City, &r.District, &r.ISP, &r.Raw, &r.FetchedAt)
	if err == sql.ErrNoRows {
		return CachedResult{}, false, nil
	}
	if err != nil {
		return CachedResult{}, false, err
	}
	r.IP = uint32(ipv)
	return r, true, nil
}

// PutCache 写入/覆盖一条缓存。
func (s *DB) PutCache(r CachedResult) error {
	_, err := s.db.Exec(`INSERT INTO ip_cache(ip, provider, province, city, district, isp, raw, fetched_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(ip) DO UPDATE SET provider=excluded.provider, province=excluded.province,
		  city=excluded.city, district=excluded.district, isp=excluded.isp,
		  raw=excluded.raw, fetched_at=excluded.fetched_at`,
		int64(r.IP), r.Provider, r.Province, r.City, r.District, r.ISP, r.Raw, r.FetchedAt)
	return err
}

// CountCache 统计某 provider 的缓存条数。
func (s *DB) CountCache(provider string) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM ip_cache WHERE provider = ?", provider).Scan(&n)
	return n, err
}

// ReplaceSegment 用一个连续子段列表替换 [start,end] 范围内的段，
// 子段必须首尾相接且完全覆盖原范围。整体在一个事务内完成。
func (s *DB) ReplaceSegment(start, end uint32, pieces []geo.Segment) error {
	if len(pieces) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM segments WHERE start_ip = ? AND end_ip = ?",
		int64(start), int64(end)); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO segments
		(start_ip, end_ip, country, province, city, county, town, isp, source)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sg := range pieces {
		if _, err := stmt.Exec(
			int64(sg.Start), int64(sg.End),
			sg.Geo.Country, sg.Geo.Province, sg.Geo.City,
			sg.Geo.County, sg.Geo.Town, sg.Geo.ISP, sg.Source,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SegmentUpdate 是一条段归属更新。
// 字段为 nil 表示"不动该列"，非 nil（含空串）表示显式写入 —— 这样
// "改了省但百度没给市" 时不会留下 广东省+南京市 这种不一致。
type SegmentUpdate struct {
	Start    uint32
	Province *string
	City     *string
	County   *string
	Town     *string
	ISP      *string
	Source   string // 例: "qqwry+baidu"
}

// UpdateSegments 批量更新段的省/市/ISP（不碰区镇，区镇只信 qqwry）。
func (s *DB) UpdateSegments(ups []SegmentUpdate) error {
	if len(ups) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, u := range ups {
		sets := []string{"source = ?"}
		args := []any{u.Source}
		if u.Province != nil {
			sets = append(sets, "province = ?")
			args = append(args, *u.Province)
		}
		if u.City != nil {
			sets = append(sets, "city = ?")
			args = append(args, *u.City)
		}
		if u.County != nil {
			sets = append(sets, "county = ?")
			args = append(args, *u.County)
		}
		if u.Town != nil {
			sets = append(sets, "town = ?")
			args = append(args, *u.Town)
		}
		if u.ISP != nil {
			sets = append(sets, "isp = ?")
			args = append(args, *u.ISP)
		}
		query := "UPDATE segments SET " + strings.Join(sets, ", ") + " WHERE start_ip = ?"
		args = append(args, int64(u.Start))
		if _, err := tx.Exec(query, args...); err != nil {
			return fmt.Errorf("更新段 %d: %w", u.Start, err)
		}
	}
	return tx.Commit()
}
