// Package store 提供构建流水线的 SQLite 持久化：
// segments 是规范化段表，ip_cache 是外部 API 结果缓存（重跑零请求），
// meta 存版本信息。断点续跑全部建立在这张库上。
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/hel2o/ip2region/internal/geo"
)

// DB 封装 SQLite 连接。
type DB struct {
	db *sql.DB
}

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;

CREATE TABLE IF NOT EXISTS segments (
  start_ip  INTEGER PRIMARY KEY,
  end_ip    INTEGER NOT NULL,
  country   TEXT NOT NULL DEFAULT '',
  province  TEXT NOT NULL DEFAULT '',
  city      TEXT NOT NULL DEFAULT '',
  county    TEXT NOT NULL DEFAULT '',
  town      TEXT NOT NULL DEFAULT '',
  isp       TEXT NOT NULL DEFAULT '',
  source    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS ip_cache (
  ip         INTEGER PRIMARY KEY,
  provider   TEXT NOT NULL,
  province   TEXT NOT NULL DEFAULT '',
  city       TEXT NOT NULL DEFAULT '',
  district   TEXT NOT NULL DEFAULT '',
  isp        TEXT NOT NULL DEFAULT '',
  raw        TEXT NOT NULL DEFAULT '',
  fetched_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// Open 打开（必要时创建）数据库并建表。
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("建表失败: %w", err)
	}
	return &DB{db: db}, nil
}

// Close 关闭连接。
func (s *DB) Close() error { return s.db.Close() }

// SaveSegments 全量重写段表（先清空），单事务完成。
func (s *DB) SaveSegments(segs []geo.Segment) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM segments"); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO segments
		(start_ip, end_ip, country, province, city, county, town, isp, source)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, sg := range segs {
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

// LoadSegments 按序读出全部段表。
func (s *DB) LoadSegments() ([]geo.Segment, error) {
	rows, err := s.db.Query(`SELECT start_ip, end_ip,
		country, province, city, county, town, isp, source FROM segments ORDER BY start_ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []geo.Segment
	for rows.Next() {
		var sg geo.Segment
		var start, end int64
		if err := rows.Scan(&start, &end,
			&sg.Geo.Country, &sg.Geo.Province, &sg.Geo.City,
			&sg.Geo.County, &sg.Geo.Town, &sg.Geo.ISP, &sg.Source); err != nil {
			return nil, err
		}
		sg.Start, sg.End = uint32(start), uint32(end)
		out = append(out, sg)
	}
	return out, rows.Err()
}

// CountSegments 返回段表行数。
func (s *DB) CountSegments() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM segments").Scan(&n)
	return n, err
}

// SetMeta / GetMeta 存取元信息。
func (s *DB) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *DB) GetMeta(key string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}
