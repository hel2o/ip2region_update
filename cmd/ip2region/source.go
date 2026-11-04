package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hel2o/ip2region/internal/provider"
)

// 主查询源取值。
const (
	sourceQifu     = "qifu"     // 百度企服 IP 画像（默认，结构化字段 + 风险标签）
	sourceOpendata = "opendata" // 百度 opendata 老接口（只回一个 location 串）
)

// buildSource 按 -primary 组装查询源。
//
// 默认 qifu 优先、opendata 兜底：qifu 实测无配额、不封 IP，且返回结构化字段；
// 一旦某个 IP 查不到（或 qifu 挂了/被封），自动回退到老的 opendata 接口，
// 两个源共用同一份 DB 缓存，换源不会导致整库重查。
func buildSource(primary string, timeout time.Duration) (provider.Provider, error) {
	opendata := provider.NewBaidu(timeout)

	switch strings.ToLower(strings.TrimSpace(primary)) {
	case "", sourceQifu:
		log.Printf("主源: qifu IP 画像 → 失败回退 opendata 旧接口")
		return provider.NewChain(provider.NewQifu(timeout), opendata), nil
	case sourceOpendata, "baidu":
		log.Printf("主源: opendata 旧接口（未启用 qifu）")
		return opendata, nil
	default:
		return nil, fmt.Errorf("非法 -primary: %q（支持 qifu | opendata）", primary)
	}
}
