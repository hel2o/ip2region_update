package provider

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
)

// Chain 把两个 provider 串成"主源优先、失败回退备用源"。
//
// 回退条件（任一命中就换备用源）：
//   - 主源网络错误 / 5xx / 业务码异常（重试后仍失败）
//   - 主源返回"无数据"（ErrNoData）——备用源可能查得到
//   - 主源被限流封禁（ErrBlocked）
//
// 主源一旦被判封禁就永久让位（进程内不再尝试），避免继续空耗；
// 备用源被封禁则不影响主源，只在两边都封禁时才向上抛 ErrBlocked 让上层停机。
type Chain struct {
	primary   Provider
	secondary Provider
	names     []string

	primaryDown atomic.Bool
	warnOnce    atomic.Bool
}

// NewChain 创建回退链。secondary 为 nil 时等价于只用主源。
func NewChain(primary, secondary Provider) *Chain {
	c := &Chain{primary: primary, secondary: secondary}
	c.names = []string{primary.Name()}
	if secondary != nil {
		c.names = append(c.names, secondary.Name())
	}
	return c
}

// Name 返回 provider 标识。用组合名是为了让缓存能同时匹配两个来源的历史记录。
func (c *Chain) Name() string {
	if c.secondary == nil {
		return c.primary.Name()
	}
	return c.primary.Name() + "+" + c.secondary.Name()
}

// CacheNames 返回可复用缓存的所有 provider 名（含历史名与组合名）。
//
// 关键：换源后旧缓存（provider="baidu"）仍然命中，不会因为改名而白跑一遍。
func (c *Chain) CacheNames() []string {
	out := make([]string, 0, len(c.names)+1)
	out = append(out, c.Name())
	return append(out, c.names...)
}

// Lookup 先打主源，失败再打备用源。
func (c *Chain) Lookup(ctx context.Context, ip uint32) (Result, error) {
	if c.secondary == nil {
		return c.primary.Lookup(ctx, ip)
	}

	var perr error
	if !c.primaryDown.Load() {
		res, err := c.primary.Lookup(ctx, ip)
		if err == nil {
			return res, nil
		}
		perr = err
		if ctx.Err() != nil {
			return Result{}, err
		}
		if errors.Is(err, ErrBlocked) {
			c.primaryDown.Store(true)
			if c.warnOnce.CompareAndSwap(false, true) {
				log.Printf("主源 %s 被限流/封禁，本轮剩余请求全部改走备用源 %s",
					c.primary.Name(), c.secondary.Name())
			}
		}
	} else {
		perr = fmt.Errorf("主源 %s 已被封禁", c.primary.Name())
	}

	res, serr := c.secondary.Lookup(ctx, ip)
	if serr == nil {
		return res, nil
	}

	// 两边都失败：决定向上抛哪个错误
	switch {
	case perr != nil && errors.Is(perr, ErrBlocked):
		// 主源被封 → 让上层停机（备用源也救不了全局）
		return Result{}, perr
	case errors.Is(serr, ErrBlocked):
		// 只有备用源被封 → 不致命，照常记一次失败
		return Result{}, perr
	case IsCacheable(serr) || (perr != nil && IsCacheable(perr)):
		// 任一方明确"无数据"就按无数据缓存，避免重跑再打一遍
		return Result{}, ErrNoData
	case perr != nil:
		return Result{}, perr
	default:
		return Result{}, serr
	}
}
