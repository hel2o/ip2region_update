// Package provider 封装外部 IP 归属查询服务（qifu 画像接口 + 百度 opendata 备用）。
//
// 设计要点：provider 只负责"查一个 IP 返回原始结构化结果"，
// 缓存、限流、收敛、冲突裁定全在 enrich 层，方便换源（高德/ip138）而不改上层。
package provider

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNoData 表示服务明确回了"没有数据"，属于可缓存结果（避免重复请求）。
	ErrNoData = errors.New("provider: 无数据")
	// ErrBlocked 表示被限流/封禁（429/403/风控页），必须退避，不可重试。
	ErrBlocked = errors.New("provider: 被限流或封禁")
)

// Result 是一次查询的归一结果。
// 注意：百度只到城市级，County 多数为空，不要指望它补区镇。
type Result struct {
	Country  string
	Province string
	City     string
	County   string
	ISP      string
	Raw      string // 原始 location 串，便于排查
}

// Provider 是外部查询服务的统一接口。
type Provider interface {
	Name() string
	Lookup(ctx context.Context, ip uint32) (Result, error)
}

// IsCacheable 判断错误结果是否值得写入缓存。
// 无数据要缓存（否则重跑会重新请求一遍空结果），限流/封禁/网络错误不能缓存。
func IsCacheable(err error) bool {
	return errors.Is(err, ErrNoData)
}

// CacheNamer 是可选接口：返回参与缓存匹配的 provider 名（可含历史名/组合名）。
// 换源后旧缓存仍能被复用，不必整库重查。
type CacheNamer interface {
	CacheNames() []string
}

// CacheNames 取一个 provider 的缓存匹配名列表（未实现 CacheNamer 时退化为 Name()）。
func CacheNames(p Provider) []string {
	if n, ok := p.(CacheNamer); ok {
		if v := n.CacheNames(); len(v) > 0 {
			return v
		}
	}
	return []string{p.Name()}
}

// ShortNamer 是可选接口：返回写进 segments.source 的短名（如 qifu / baidu）。
type ShortNamer interface {
	Short() string
}

// ShortName 取来源短名，用于 source 标签（未实现 ShortNamer 时退化为 Name()）。
func ShortName(p Provider) string {
	if s, ok := p.(ShortNamer); ok {
		if v := s.Short(); v != "" {
			return v
		}
	}
	return p.Name()
}

// nowMs 便于测试替换。
var nowMs = func() int64 { return time.Now().UnixMilli() }
