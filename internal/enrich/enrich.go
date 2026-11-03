// Package enrich 用外部 provider 校正段表的省/市/ISP。
//
// 硬规则（与"提升精度"目标绑定）：
//   - 百度只到城市级 → 区/镇 永不改，只认 qqwry
//   - 只覆盖更具体的值：provider 返回空字段时保留原值，绝不清空
//   - ISP 只在原本为空时填充；两边都有值但不同只记冲突，不写库（qqwry 的
//     "腾讯云/网宿" 往往比百度的 "电信" 更细）
//   - 同 /24 共用一个查询结果（百度的归属按网段，不按单 IP）
package enrich

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

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/provider"
	"github.com/hel2o/ip2region/internal/store"
)

// Options 控制 enrich 行为。
type Options struct {
	QPS          int    // 全局请求速率上限
	Workers      int    // 并发数
	Limit        int    // 最多发多少次请求（0 = 不限，测试用）
	Mask         uint32 // 按此掩码去重（默认 /24 = 0xFFFFFF00；传 0 表示不去重）
	DryRun       bool   // 只统计不写库
	Timeout      time.Duration
	ProgressN    int    // 每 N 个 probe 打一行进度
	ConflictFile string // 冲突样例输出路径（空则不写）
	MaxConflicts int    // 冲突文件最多写多少行
}

// Stats 是执行统计。
type Stats struct {
	Segments   int // 参与增强的段数（中国段）
	Probes     int // 去重后的探测点数
	DBHits     int // 命中 SQLite 缓存
	Requests   int // 真实请求数
	NoData     int
	Errors     int
	ProvFixed  int
	CityFixed  int
	ISPFilled  int
	Conflicts  int
	Elapsed    time.Duration
	CursorStop uint32 // 中断时的进度游标（0 表示正常跑完）
}

type probe struct {
	key    uint32 // 去重键（ip & mask）
	ip     uint32 // 实际探测 IP（段中点）
	segIdx []int  // 该键对应的段下标
}

// Run 执行增强。ctx 取消会保存游标后返回。
func Run(ctx context.Context, db *store.DB, p provider.Provider, segs []geo.Segment, opt Options) (Stats, error) {
	start := time.Now()
	if opt.Workers <= 0 {
		opt.Workers = 8
	}
	if opt.QPS <= 0 {
		opt.QPS = 20
	}
	if opt.Mask == 0 {
		opt.Mask = 0xFFFFFF00
	}
	if opt.ProgressN <= 0 {
		opt.ProgressN = 2000
	}
	if opt.MaxConflicts <= 0 {
		opt.MaxConflicts = 2000
	}

	var st Stats

	// 1. 组装 probe 列表（按 /24 去重）
	byKey := map[uint32]*probe{}
	var probes []*probe
	for i := range segs {
		sg := &segs[i]
		if sg.Geo.Country != "中国" {
			continue
		}
		ip := uint32((uint64(sg.Start) + uint64(sg.End)) / 2)
		key := ip & opt.Mask
		st.Segments++
		pr, ok := byKey[key]
		if !ok {
			pr = &probe{key: key, ip: ip}
			byKey[key] = pr
			probes = append(probes, pr)
		}
		pr.segIdx = append(pr.segIdx, i)
	}
	st.Probes = len(probes)
	log.Printf("待增强中国段 %d 个，去重后探测点 %d 个（并发 %d，限速 %d QPS）",
		st.Segments, st.Probes, opt.Workers, opt.QPS)

	// 2. 限速器
	interval := time.Second / time.Duration(opt.QPS)
	tokens := make(chan struct{}, 1)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				select {
				case tokens <- struct{}{}:
				default:
				}
			}
		}
	}()

	// 3. worker 池
	type probeResult struct {
		pr  *probe
		res provider.Result
		err error
		hit bool // 是否来自缓存
	}
	jobs := make(chan *probe)
	out := make(chan probeResult, opt.Workers*2)

	var conflicts []string
	var confMu sync.Mutex

	var reqCount int64
	var ctxStop atomic.Bool
	stopCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for w := 0; w < opt.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pr := range jobs {
				if stopCtx.Err() != nil {
					return
				}

				// 先查 DB 缓存（同 /24 任一条即可）
				if cached, ok, err := db.GetCacheInRange(pr.key, pr.key|^opt.Mask, p.Name()); err == nil && ok {
					out <- probeResult{pr: pr, res: provider.Result{
						Province: cached.Province, City: cached.City,
						County: cached.District, ISP: cached.ISP, Raw: cached.Raw,
					}, hit: true}
					continue
				}

				// 限速 + 预算
				select {
				case <-stopCtx.Done():
					return
				case <-tokens:
				}
				if opt.Limit > 0 && atomic.LoadInt64(&reqCount) >= int64(opt.Limit) {
					cancel()
					return
				}
				atomic.AddInt64(&reqCount, 1)

				res, err := p.Lookup(stopCtx, pr.ip)
				if errors.Is(err, provider.ErrBlocked) {
					ctxStop.Store(true)
					cancel()
					out <- probeResult{pr: pr, err: err}
					return
				}
				if err != nil && provider.IsCacheable(err) {
					// 无数据也缓存，避免重跑重复请求
					_ = db.PutCache(store.CachedResult{
						IP: pr.ip, Provider: p.Name(), FetchedAt: time.Now().Unix(),
					})
					out <- probeResult{pr: pr, err: err}
					continue
				}
				if err != nil {
					out <- probeResult{pr: pr, err: err}
					continue
				}

				_ = db.PutCache(store.CachedResult{
					IP: pr.ip, Provider: p.Name(),
					Province: res.Province, City: res.City, District: res.County,
					ISP: res.ISP, Raw: res.Raw, FetchedAt: time.Now().Unix(),
				})
				out <- probeResult{pr: pr, res: res}
			}
		}()
	}

	// 4. 投递
	go func() {
		defer close(jobs)
		for _, pr := range probes {
			select {
			case <-stopCtx.Done():
				return
			case jobs <- pr:
			}
		}
	}()

	// 5. 消费 + 落库
	done := make(chan struct{})
	go func() {
		defer close(done)
		for r := range out {
			if r.hit {
				st.DBHits++
			} else if r.err == nil {
				st.Requests++
			} else if provider.IsCacheable(r.err) {
				st.Requests++
				st.NoData++
			} else {
				st.Requests++
				st.Errors++
			}

			if r.err == nil || provider.IsCacheable(r.err) {
				ups, kinds, conf := decide(segs, r.pr, r.res)
				st.ProvFixed += kinds.prov
				st.CityFixed += kinds.city
				st.ISPFilled += kinds.isp
				st.Conflicts += conf.n
				if conf.sample != "" && len(conflicts) < opt.MaxConflicts {
					confMu.Lock()
					conflicts = append(conflicts, conf.sample)
					confMu.Unlock()
				}
				if !opt.DryRun && len(ups) > 0 {
					if err := db.UpdateSegments(ups); err != nil {
						log.Printf("写库失败: %v", err)
					}
				}
			}

			if (st.DBHits+st.Requests)%opt.ProgressN == 0 {
				el := time.Since(start)
				rate := float64(st.Requests) / el.Seconds()
				log.Printf("进度 %d/%d（请求 %d，缓存 %d，%.1f req/s）",
					st.DBHits+st.Requests, st.Probes, st.Requests, st.DBHits, rate)
			}
		}
	}()

	wg.Wait()
	close(out)
	<-done

	st.Elapsed = time.Since(start)

	if err := writeConflicts(opt.ConflictFile, conflicts); err != nil {
		log.Printf("写冲突样例失败: %v", err)
	}
	if ctxStop.Load() {
		return st, provider.ErrBlocked
	}
	// 只有父 ctx（用户 Ctrl-C）才算中断；预算用完是正常结束
	if err := ctx.Err(); err != nil {
		return st, err
	}
	return st, nil
}

type kindCount struct{ prov, city, isp int }
type confInfo struct {
	n      int
	sample string
}

// sameName 比较行政区名，忽略 市/区/县 等后缀差异。
// qqwry 存 "广州"，百度给 "广州市"，不是真实差异，不该触发全表改写。
func sameName(a, b string) bool {
	return trimAdminSuffix(a) == trimAdminSuffix(b)
}

func trimAdminSuffix(s string) string {
	for _, suf := range []string{"特别行政区", "自治区", "自治州", "自治县", "市", "区", "县", "盟", "旗", "省"} {
		if strings.HasSuffix(s, suf) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

// decide 决定某个探测点的结果如何应用到其所有段上。
func decide(segs []geo.Segment, pr *probe, res provider.Result) ([]store.SegmentUpdate, kindCount, confInfo) {
	var (
		ups  []store.SegmentUpdate
		k    kindCount
		conf confInfo
	)
	if res.Province == "" && res.City == "" && res.ISP == "" {
		return nil, k, conf
	}

	for _, idx := range pr.segIdx {
		sg := segs[idx]
		up := store.SegmentUpdate{Start: sg.Start, Source: sg.Source + "+baidu"}

		provDiff := res.Province != "" && !sameName(sg.Geo.Province, res.Province)
		cityDiff := res.City != "" && !sameName(sg.Geo.City, res.City)

		if provDiff {
			p := res.Province
			up.Province = &p
			k.prov++
			// 省变了市必须跟着变，否则会留下跨省组合；百度没给市就清空
			c := res.City
			up.City = &c
			if sg.Geo.City != "" {
				k.city++
			}
		} else if cityDiff {
			c := res.City
			up.City = &c
			k.city++
		}

		if sg.Geo.ISP == "" && res.ISP != "" {
			i := res.ISP
			up.ISP = &i
			k.isp++
		} else if sg.Geo.ISP != "" && res.ISP != "" && sg.Geo.ISP != res.ISP {
			conf.n++
			if conf.sample == "" {
				conf.sample = fmt.Sprintf("%s\t%s|%s|%s\t%s|%s|%s\t%s",
					geo.Uint2IP(sg.Start), sg.Geo.Province, sg.Geo.City, sg.Geo.ISP,
					res.Province, res.City, res.ISP, res.Raw)
			}
		}

		if up.Province != nil || up.City != nil || up.ISP != nil {
			ups = append(ups, up)
		}
	}
	return ups, k, conf
}

// writeConflicts 输出冲突样例（TSV，便于人工抽检）。
func writeConflicts(path string, lines []string) error {
	if path == "" || len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "ip\tqqwry(省|市|ISP)\tbaidu(省|市|ISP)\traw\n"); err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := fmt.Fprintln(f, l); err != nil {
			return err
		}
	}
	return nil
}
