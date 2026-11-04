// Package refine 对大段做自适应细分（二分收敛）。
//
// 背景：qqwry 里有很多 /10 ~ /16 的大段，整段只标一个省市。只探一个中点就把
// 结果套满整段，误差会被放大到几百万个 IP（实测 18,685 个大段覆盖了中国 83%
// 的地址空间）。所以对超过阈值的大段改成多点探测：
//
//	探 lo / mid / hi 三点
//	  ├─ 三点归属一致 → 整段确认，1~3 次请求
//	  └─ 不一致      → 在 mid 处二分，递归处理两半（深度受限）
//
// 细分产生的子段直接替换原段，落库时保持连续、不重不漏。
package refine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hel2o/ip2region/internal/enrich"
	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/provider"
	"github.com/hel2o/ip2region/internal/regions"
	"github.com/hel2o/ip2region/internal/store"
)

// Options 控制细分行为。
type Options struct {
	Threshold  int    // 只处理长度 >= 该值的段（默认 4096，即 /20）
	MinSpan    int    // 收敛到这个粒度就停（默认 256，即 /24）
	MaxDepth   int    // 递归深度安全阀（默认 20）
	SegProbes  int    // 单个段最多探测几次（默认 128，防止复杂段烧请求）
	QPS        int    // 全局请求速率上限
	Workers    int    // 并发处理的段数
	Limit      int    // 最大请求数（0=不限）
	DryRun     bool   // 只统计不写库
	ProgressN  int    // 每 N 个段打进度
	OnlyEmpty  bool   // 只处理省市为空的段（--only-empty）
	ReportFile string // 细分明细输出
}

// Stats 是执行统计。
type Stats struct {
	BigSegments int
	Probes      int
	Requests    int
	CacheHits   int
	NoData      int
	Errors      int
	Confirmed   int // 三点一致，整段确认
	SplitSegs   int // 被细分的原段数
	NewPieces   int // 细分后新增的段数
	Elapsed     time.Duration
}

type refiner struct {
	db  *store.DB
	p   provider.Provider
	opt Options

	reqCount int64
	tokens   chan struct{}

	mu     sync.Mutex
	stat   Stats
	report []string
}

// Run 执行细分。
func Run(ctx context.Context, db *store.DB, p provider.Provider, segs []geo.Segment, opt Options) (Stats, error) {
	start := time.Now()
	if opt.Threshold <= 0 {
		opt.Threshold = 4096
	}
	if opt.MinSpan <= 0 {
		opt.MinSpan = 256
	}
	if opt.SegProbes <= 0 {
		opt.SegProbes = 128
	}
	if opt.MaxDepth <= 0 {
		opt.MaxDepth = 20
	}
	if opt.QPS <= 0 {
		opt.QPS = 20
	}
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	if opt.ProgressN <= 0 {
		opt.ProgressN = 500
	}

	r := &refiner{db: db, p: p, opt: opt, tokens: make(chan struct{}, 1)}

	// 收集目标段
	var targets []geo.Segment
	for _, sg := range segs {
		if sg.Geo.Country != "中国" {
			continue
		}
		if int(sg.Len()) < opt.Threshold {
			continue
		}
		if opt.OnlyEmpty && sg.Geo.Province != "" && sg.Geo.City != "" {
			continue
		}
		targets = append(targets, sg)
	}
	log.Printf("待细分大段 %d 个（阈值 %d IP，深度 %d）", len(targets), opt.Threshold, opt.MaxDepth)
	if len(targets) == 0 {
		return Stats{Elapsed: time.Since(start)}, nil
	}

	// 限速器
	interval := time.Second / time.Duration(opt.QPS)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				select {
				case r.tokens <- struct{}{}:
				default:
				}
			}
		}
	}()

	stopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan geo.Segment)
	var wg sync.WaitGroup
	var blocked atomic.Bool

	for w := 0; w < opt.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sg := range jobs {
				if stopCtx.Err() != nil {
					return
				}
				if err := r.refineSegment(stopCtx, sg); err != nil {
					if errors.Is(err, provider.ErrBlocked) {
						blocked.Store(true)
						cancel()
						return
					}
					if errors.Is(err, errBudget) {
						cancel()
						return
					}
					if errors.Is(err, context.Canceled) {
						return
					}
					r.mu.Lock()
					r.stat.Errors++
					r.mu.Unlock()
					log.Printf("细分 %s-%s 失败: %v", geo.Uint2IP(sg.Start), geo.Uint2IP(sg.End), err)
				}
			}
		}()
	}

feed:
	for _, sg := range targets {
		select {
		case <-stopCtx.Done():
			break feed
		case jobs <- sg:
		}
	}
	close(jobs)
	wg.Wait()

	st := r.snapshot()
	st.Elapsed = time.Since(start)
	if err := r.writeReport(); err != nil {
		log.Printf("写细分明细失败: %v", err)
	}
	if blocked.Load() {
		return st, provider.ErrBlocked
	}
	if err := ctx.Err(); err != nil {
		return st, err
	}
	return st, nil
}

var errBudget = errors.New("refine: 请求预算用尽")

// probeGeo 查一个 IP 的归属，优先读缓存。
func (r *refiner) probeGeo(ctx context.Context, ip uint32) (provider.Result, bool, error) {
	lo, hi := ip&0xFFFFFF00, ip|0xFF
	if c, ok, err := r.db.GetCacheInRangeAny(lo, hi, provider.CacheNames(r.p)); err == nil && ok {
		r.mu.Lock()
		r.stat.CacheHits++
		r.mu.Unlock()
		return provider.Result{Province: c.Province, City: c.City, County: c.District, ISP: c.ISP, Raw: c.Raw}, true, nil
	}

	if r.opt.Limit > 0 && atomic.LoadInt64(&r.reqCount) >= int64(r.opt.Limit) {
		return provider.Result{}, false, errBudget
	}

	select {
	case <-ctx.Done():
		return provider.Result{}, false, ctx.Err()
	case <-r.tokens:
	}

	atomic.AddInt64(&r.reqCount, 1)
	res, err := r.p.Lookup(ctx, ip)
	r.mu.Lock()
	r.stat.Requests++
	if err != nil && provider.IsCacheable(err) {
		r.stat.NoData++
	}
	r.mu.Unlock()
	if err != nil {
		if provider.IsCacheable(err) {
			_ = r.db.PutCache(store.CachedResult{IP: ip, Provider: r.p.Name(), FetchedAt: time.Now().Unix()})
			return provider.Result{}, false, nil
		}
		return provider.Result{}, false, err
	}
	_ = r.db.PutCache(store.CachedResult{
		IP: ip, Provider: r.p.Name(), Province: res.Province, City: res.City,
		District: res.County, ISP: res.ISP, Raw: res.Raw, FetchedAt: time.Now().Unix(),
	})
	r.mu.Lock()
	r.stat.Probes++
	r.mu.Unlock()
	return res, false, nil
}

// refineSegment 对一个大段做自适应细分并落库。
func (r *refiner) refineSegment(ctx context.Context, sg geo.Segment) error {
	r.mu.Lock()
	r.stat.BigSegments++
	r.mu.Unlock()

	budget := r.opt.SegProbes
	pieces, err := r.resolve(ctx, sg, 0, &budget)
	if err != nil {
		return err
	}

	if !r.opt.DryRun && len(pieces) > 1 {
		if err := r.db.ReplaceSegment(sg.Start, sg.End, pieces); err != nil {
			return err
		}
	}

	r.mu.Lock()
	if len(pieces) > 1 {
		r.stat.SplitSegs++
		r.stat.NewPieces += len(pieces) - 1
		if len(r.report) < 5000 {
			for _, pc := range pieces {
				r.report = append(r.report, fmt.Sprintf("%s-%s\t%s\t(原段 %s-%s|%s|%s)",
					geo.Uint2IP(pc.Start), geo.Uint2IP(pc.End), pc.Geo.String(),
					geo.Uint2IP(sg.Start), geo.Uint2IP(sg.End), sg.Geo.Province, sg.Geo.City))
			}
		}
	}
	done := r.stat.BigSegments
	r.mu.Unlock()

	if done%r.opt.ProgressN == 0 {
		st := r.snapshot()
		log.Printf("进度 %d 个段（请求 %d，缓存 %d，细分 %d）", done, st.Requests, st.CacheHits, st.SplitSegs)
	}
	return nil
}

// resolve 递归确认 [lo,hi] 的归属，只在"两端结论不同"的区间继续收敛。
//
// 每次探三个点（起点、中点、终点）：
//
//	三点一致     → 整段一个结论，左右都收敛完毕
//	首=中 ≠ 尾   → 左半已确认，直接出一片，只往右半收敛
//	首 ≠ 中=尾   → 右半已确认，只往左半收敛
//	三点都不同   → 两半都要收敛
//
// 区间收到 MinSpan（默认 256 = /24）就停，因为再细百度也分不出来。
//
// 成本对比：每个边界定位到 MinSpan 只需要 log2(段长/MinSpan) 轮探测；
// 而均匀采样要把边界定位到同样精度，需要 段长/MinSpan 个点。
// 一个 /16 收敛到 /24 是 8 轮，均匀采样要 256 个点。
//
// budget 是本段的探测预算，用完就按当前已知结果落定，防止归属混乱的段
// 无限递归。
func (r *refiner) resolve(ctx context.Context, sg geo.Segment, depth int, budget *int) ([]geo.Segment, error) {
	lo, hi := sg.Start, sg.End
	mid := uint32((uint64(lo) + uint64(hi)) / 2)
	span := uint64(hi) - uint64(lo) + 1

	// 够细了 / 递归过深 / 预算用完：探中点直接定这一段
	if span <= uint64(r.opt.MinSpan) || depth >= r.opt.MaxDepth || *budget <= 0 {
		p, _, err := r.probeGeo(ctx, mid)
		if err != nil {
			return nil, err
		}
		return r.leaf(sg, p, depth)
	}
	*budget -= 3

	pLo, _, err := r.probeGeo(ctx, lo)
	if err != nil {
		return nil, err
	}
	pMid, _, err := r.probeGeo(ctx, mid)
	if err != nil {
		return nil, err
	}
	pHi, _, err := r.probeGeo(ctx, hi)
	if err != nil {
		return nil, err
	}

	leftSame := enrich.SameGeo(pLo, pMid)
	rightSame := enrich.SameGeo(pMid, pHi)

	// 三点一致：整段一个结论
	if leftSame && rightSame {
		return r.leaf(sg, pMid, depth)
	}

	// 左半已确认：出一片，只收敛右半
	if leftSame {
		left := r.piece(lo, mid-1, applyTo(pLo, sg.Geo), sg.Source)
		right, err := r.resolve(ctx, geo.Segment{Start: mid, End: hi, Geo: sg.Geo, Source: sg.Source}, depth+1, budget)
		if err != nil {
			return nil, err
		}
		return append([]geo.Segment{left}, right...), nil
	}

	// 右半已确认：只收敛左半
	if rightSame {
		right := r.piece(mid, hi, applyTo(pHi, sg.Geo), sg.Source)
		left, err := r.resolve(ctx, geo.Segment{Start: lo, End: mid - 1, Geo: sg.Geo, Source: sg.Source}, depth+1, budget)
		if err != nil {
			return nil, err
		}
		return append(left, right), nil
	}

	// 三点都不同：两半都要收敛
	left, err := r.resolve(ctx, geo.Segment{Start: lo, End: mid - 1, Geo: sg.Geo, Source: sg.Source}, depth+1, budget)
	if err != nil {
		return nil, err
	}
	right, err := r.resolve(ctx, geo.Segment{Start: mid, End: hi, Geo: sg.Geo, Source: sg.Source}, depth+1, budget)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// leaf 生成一个"不再细分"的段。
func (r *refiner) leaf(sg geo.Segment, p provider.Result, depth int) ([]geo.Segment, error) {
	geoOut := applyTo(p, sg.Geo)
	if depth == 0 {
		r.mu.Lock()
		r.stat.Confirmed++
		r.mu.Unlock()
		// 顶层（原段仍在库里）才写库；细分片由 ReplaceSegment 一并写入
		if !r.opt.DryRun && geoOut != sg.Geo {
			_ = r.db.UpdateSegments([]store.SegmentUpdate{{
				Start: sg.Start, Province: ptr(geoOut.Province), City: ptr(geoOut.City),
				ISP: ispPtr(geoOut.ISP, sg.Geo.ISP), Source: enrich.TagSource(sg.Source, "+baidu"),
			}})
		}
	}
	return []geo.Segment{r.piece(sg.Start, sg.End, geoOut, sg.Source)}, nil
}

// piece 构造一个细分片。
func (r *refiner) piece(start, end uint32, g geo.GeoInfo, src string) geo.Segment {
	return geo.Segment{Start: start, End: end, Geo: g, Source: enrich.TagSource(src, "+baidu")}
}

// applyTo 把探测结果合并进原有归属。
//
// 与 enrich 同一套口径：
//   - 云/CDN/IDC 段只补空，不覆盖（地理位置由路由决定，探到的是 PoP 城市）
//   - 百度说换省却给不出市、而原值本来有市 → 置信度不足，不动省市
//   - ISP 只补空；区镇永不改动
func applyTo(res provider.Result, old geo.GeoInfo) geo.GeoInfo {
	out := old
	virtual := regions.IsCloudISP(old.ISP)

	switch {
	case virtual:
		// 云段只补空，且省/市必须成对自洽
		enrich.FillPair(&out, res)

	case res.Province == "":
		// 百度没给省：无法验证市属于哪个省，只补空不动已有值
		enrich.FillPair(&out, res)

	case out.City != "" && res.City == "":
		// 低置信度：说换省却给不出市，省市都不动

	default:
		// 三点探测一致，省市成对覆盖，天然自洽
		if res.City != "" && !enrich.SameName(out.City, res.City) {
			// 市变了，原区县/镇挂在旧市下，不再成立
			out.County = ""
			out.Town = ""
		}
		out.Province = res.Province
		if res.City != "" {
			out.City = res.City
		}
	}
	if out.ISP == "" && res.ISP != "" {
		out.ISP = res.ISP
	}
	return out
}

func ptr(s string) *string { return &s }

// ispPtr 只有需要填充时才返回非 nil。
func ispPtr(newVal, oldVal string) *string {
	if oldVal == "" && newVal != "" {
		return &newVal
	}
	return nil
}

func (r *refiner) snapshot() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stat
}

func (r *refiner) writeReport() error {
	if r.opt.ReportFile == "" || len(r.report) == 0 {
		return nil
	}
	sort.Strings(r.report)
	var sb strings.Builder
	sb.WriteString("段范围\t新归属\t原段\n")
	for _, l := range r.report {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return os.WriteFile(r.opt.ReportFile, []byte(sb.String()), 0o644)
}
