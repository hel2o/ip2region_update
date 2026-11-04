// Package enrich 用外部 provider 校正段表的省/市/ISP。
//
// 硬规则（与"提升精度"目标绑定）：
//   - 百度只到城市级 → 区/镇 永不改，只认 qqwry
//   - 默认**只补空不覆盖**：实测百度在省市上与 qqwry 是同一量级的噪声源
//     （40 样本里 1 例改对、1 例改错），覆盖只会引入无谓抖动；
//     覆盖需要显式 -override，且云/CDN 段永远不覆盖
//   - provider 返回空字段时保留原值，绝不清空
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
	"github.com/hel2o/ip2region/internal/geocn"
	"github.com/hel2o/ip2region/internal/ispip"
	"github.com/hel2o/ip2region/internal/provider"
	"github.com/hel2o/ip2region/internal/regions"
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
	Override     bool   // 是否允许覆盖已有省市（默认 false，只补空）
	ISPTables    *ispip.Table // 运营商网段表（nil=不按网段判 ISP）
	GeoCN        *geocn.DB    // GeoCN 离线库（nil=不补区县）
	ProbeStep    int          // 每多少 IP 布一个探测点（0 = 每段只探中点）
	ProbeCap     int          // 每个段最多探几个点（0 = 不封顶）
}

// Stats 是执行统计。
type Stats struct {
	ISPFilledByNet int // 网段表补充的 ISP
	ISPFixedByNet  int // 网段表纠正的 ISP（云/IDC 优先）
	CountyFilled   int // GeoCN 补的区县
	CountyCleared  int // 因省市变更而清空的旧区县
	GeoCNConflict  int // GeoCN 与现有省市不一致而放弃的段
	Segments   int // 参与增强的段数（中国段）
	Pieces     int // 按探测密度切出的片数
	SplitSegs  int // 因片间结论不同而被切开的段数
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
	key   uint32    // 去重键（ip & mask）
	ip    uint32    // 实际探测 IP
	parts []segPart // 该键覆盖的段内区间
}

// segPart 是段内被某个探测点覆盖的一段区间。
// 大段会被切成多片，每片独立探测、独立决策，结论不同就切开——
// 这解决了"一个 /10 只探一个中点、结论套满 419 万 IP"的问题。
type segPart struct {
	idx        int    // 段下标
	start, end uint32 // 该段内的区间
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

	// 0. 先用运营商网段表定 ISP（分配归属，比百度可靠）
	if opt.ISPTables != nil {
		filled, fixed, err := applyISPTables(db, segs, opt.ISPTables, opt.DryRun)
		if err != nil {
			return st, err
		}
		st.ISPFilledByNet, st.ISPFixedByNet = filled, fixed
		log.Printf("网段表定 ISP: 补空 %d 段, 纠正 %d 段", filled, fixed)
	}

	// 1. 组装 probe 列表（按段长自适应切片，再按 /24 去重）
	byKey := map[uint32]*probe{}
	var probes []*probe
	for i := range segs {
		sg := &segs[i]
		if sg.Geo.Country != "中国" {
			continue
		}
		st.Segments++
		n := probeCount(opt, sg)
		span := uint64(sg.End) - uint64(sg.Start) + 1
		for j := 0; j < n; j++ {
			a := sg.Start + uint32(span*uint64(j)/uint64(n))
			b := sg.Start + uint32(span*uint64(j+1)/uint64(n)) - 1
			ip := uint32((uint64(a) + uint64(b)) / 2)
			key := ip & opt.Mask
			pr, ok := byKey[key]
			if !ok {
				pr = &probe{key: key, ip: ip}
				byKey[key] = pr
				probes = append(probes, pr)
			}
			pr.parts = append(pr.parts, segPart{idx: i, start: a, end: b})
			st.Pieces++
		}
	}
	st.Probes = len(probes)
	log.Printf("待增强中国段 %d 个，按密度切出 %d 片，去重后探测点 %d 个（并发 %d，限速 %d QPS）",
		st.Segments, st.Pieces, st.Probes, opt.Workers, opt.QPS)

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

	// 5. 消费：每个探测点只作用于它覆盖的段内区间，按片累积到所属段
	aggs := make([]*segAgg, len(segs))
	var touched []int

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
				for _, part := range r.pr.parts {
					sg := &segs[part.idx]
					ng, kinds, ispConf := decidePart(sg.Geo, r.res, opt.Override)

					st.ProvFixed += kinds.prov
					st.CityFixed += kinds.city
					st.CountyCleared += kinds.countyCleared
					st.ISPFilled += kinds.isp

					// ISP 冲突只按段记一次，避免同一段多个片重复刷屏
					if ispConf && part.start == sg.Start && len(conflicts) < opt.MaxConflicts {
						st.Conflicts++
						confMu.Lock()
						conflicts = append(conflicts, fmt.Sprintf("%s\t%s|%s|%s\t%s|%s|%s\t%s",
							geo.Uint2IP(sg.Start), sg.Geo.Province, sg.Geo.City, sg.Geo.ISP,
							r.res.Province, r.res.City, r.res.ISP, r.res.Raw))
						confMu.Unlock()
					}

					src := sg.Source
					if ng != sg.Geo {
						src = TagSource(src, "+baidu")
					}
					a := aggs[part.idx]
					if a == nil {
						a = &segAgg{}
						aggs[part.idx] = a
						touched = append(touched, part.idx)
					}
					a.pieces = append(a.pieces, geo.Segment{
						Start: part.start, End: part.end, Geo: ng, Source: src,
					})
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

	// 5.5 落库：全段结论一致就 UPDATE，片间不一致就按片切开
	if !opt.DryRun {
		flushSegs(db, segs, aggs, touched, &st)
	} else {
		for _, idx := range touched {
			if len(aggs[idx].pieces) > 1 {
				st.SplitSegs++
			}
		}
	}

	// 6. GeoCN 补区县（放在百度之后：省市已是最终值，区县才能校验自洽）
	if opt.GeoCN != nil {
		// 上一步可能把段切开了，内存里的 segs 已过期。必须重新载入，否则
		// GeoCN 会按已经不存在的 (start,end) 去 DELETE，导致段表重叠。
		if !opt.DryRun {
			reloaded, err := db.LoadSegments()
			if err != nil {
				return st, fmt.Errorf("重新载入段表: %w", err)
			}
			segs = reloaded
		}
		g := applyGeoCN(db, segs, opt.GeoCN, opt.DryRun)
		st.CountyFilled, st.GeoCNConflict = g.county, g.conflict
		log.Printf("GeoCN 切分补区县: %d 段填区县，%d 段被切开（省 %d / 市 %d 段顺带补空）",
			g.county, g.split, g.prov, g.city)
		log.Printf("  片级放弃: 省市不一致 %d，跨度超限 %d，只有市级编码 %d，云段整段跳过 %d",
			g.conflict, g.tooWide, g.noDistrict, g.cloud)
	}

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

type kindCount struct{ prov, city, isp, countyCleared int }
type confInfo struct {
	n      int
	sample string
}

// geocnResult 是 GeoCN 预处理的统计。
type geocnResult struct {
	county, prov, city, conflict, cloud, split int
	noDistrict                                  int
	tooWide                                     int
}

// maxDistrictSpan 是可信区县的最大跨度。
//
// GeoCN 库里存在 "59.192.0.0/10（419 万 IP）→ 110102 西城区" 这种条目，
// 那是它扫描采样过稀造成的，不可能整段都是一个区。超过这个跨度的区县一律不采信，
// 省市仍然保留（省市信息不受影响）。
const maxDistrictSpan = 1 << 17 // 131072 个 IP

// applyGeoCN 用 GeoCN 补区县，按 GeoCN 的网段边界切分段。
//
// 为什么必须切：一个 /16 平均跨 200 多个 GeoCN 网段，"整段塞一个区县"是错的。
// 早先的实现只探段中点，于是 19,078 段（1.03 亿 IP）拿一个区县的标签套满整段。
// 现在按 GeoCN 自己的网段边界切，每片填自己片的区县，不需要任何网络请求。
//
// 硬规则（与之前一致）：
//   - 只补空，绝不覆盖已有区县（qqwry 的区县无证据说更差）
//   - 省市必须自洽：GeoCN 的省/市与我们已有的值不一致，那一片就不填区县，
//     否则会产出"吉林省长春市|玄武区"这种错位数据
//   - 云/CDN/IDC 段跳过：GeoCN 同样是扫 API 得来的，云段给的是 PoP 城市
func applyGeoCN(db *store.DB, segs []geo.Segment, gc *geocn.DB, dryRun bool) geocnResult {
	var res geocnResult

	for i := range segs {
		sg := &segs[i]
		if sg.Geo.Country != "中国" || sg.Geo.County != "" {
			continue
		}
		if regions.IsCloudISP(sg.Geo.ISP) {
			res.cloud++
			continue
		}
		slices := gc.Spans(sg.Start, sg.End)
		if len(slices) == 0 {
			continue
		}

		runs := buildRuns(sg, slices, &res)
		if len(runs) == 0 {
			continue
		}

		// 全段一个结论：直接 UPDATE，不用切
		if len(runs) == 1 && runs[0].start == sg.Start && runs[0].end == sg.End {
			r := runs[0]
			if r.county == "" {
				continue
			}
			up := store.SegmentUpdate{Start: sg.Start, Source: TagSource(sg.Source, "+geocn")}
			if sg.Geo.Province == "" && r.prov != "" {
				v := r.prov
				up.Province = &v
				res.prov++
			}
			if sg.Geo.City == "" && r.city != "" {
				v := r.city
				up.City = &v
				res.city++
			}
			v := r.county
			up.County = &v
			res.county++

			if !dryRun {
				if err := db.UpdateSegments([]store.SegmentUpdate{up}); err != nil {
					log.Printf("GeoCN 写库失败 %s: %v", geo.Uint2IP(sg.Start), err)
				}
			}
			sg.Geo.County = r.county
			continue
		}

		// 跨多个区县：按边界切片，每片带自己的区县
		pieces := make([]geo.Segment, 0, len(runs))
		any := false
		for _, r := range runs {
			g := sg.Geo
			if r.prov != "" {
				g.Province = r.prov
			}
			if r.city != "" {
				g.City = r.city
			}
			if r.county != "" {
				g.County = r.county
				res.county++
				any = true
			}
			src := sg.Source
			if r.county != "" {
				src = TagSource(src, "+geocn")
			}
			pieces = append(pieces, geo.Segment{Start: r.start, End: r.end, Geo: g, Source: src})
		}
		if !any {
			// 一片区县都没填上，切了也没意义
			continue
		}
		res.split++
		if !dryRun {
			if err := db.ReplaceSegment(sg.Start, sg.End, pieces); err != nil {
				log.Printf("GeoCN 切分段失败 %s: %v", geo.Uint2IP(sg.Start), err)
			}
		}
	}
	return res
}

// run 是段内一段归属一致的区间。
type run struct {
	start, end         uint32
	prov, city, county string
}

// buildRuns 把 GeoCN 的网段切片合并成"归属相同"的连续区间。
//
// 必须补齐首尾和中间的空洞（GeoCN 未收录的 IP），否则切分后段表会出现缝隙、
// 丢掉那部分 IP —— 之前只补了中间空洞，漏了首尾。
func buildRuns(sg *geo.Segment, slices []geocn.Slice, res *geocnResult) []run {
	cur := sg.Geo
	var runs []run
	push := func(r run) {
		if n := len(runs); n > 0 && runs[n-1].county == r.county &&
			runs[n-1].prov == r.prov && runs[n-1].city == r.city &&
			runs[n-1].end+1 == r.start {
			runs[n-1].end = r.end
			return
		}
		runs = append(runs, r)
	}

	for _, sl := range slices {
		// 空洞：GeoCN 没收录的区间，保持原归属（首尾空洞同样要补）
		prev := sg.Start
		if n := len(runs); n > 0 {
			prev = runs[n-1].end + 1
		}
		if prev < sl.Start {
			push(run{start: prev, end: sl.Start - 1})
		}
		info := sl.Info
		var r run
		r.start, r.end = sl.Start, sl.End
		switch {
		case !info.HasDistrict():
			res.noDistrict++
		case sl.End-sl.Start+1 > maxDistrictSpan:
			res.tooWide++
		case !geoConsistent(cur, info):
			res.conflict++
		default:
			r.prov, r.city, r.county = info.Province, info.City, info.District
		}
		push(r)
	}
	// 尾部空洞
	if n := len(runs); n > 0 && runs[n-1].end < sg.End {
		push(run{start: runs[n-1].end + 1, end: sg.End})
	}
	return runs
}

// geoConsistent 判断 GeoCN 的省市与我们已有的值是否对得上（空值不参与比较）。
func geoConsistent(cur geo.GeoInfo, in geocn.Info) bool {
	if cur.Province != "" && !sameName(cur.Province, in.Province) {
		return false
	}
	if cur.City != "" && !sameName(cur.City, in.City) {
		return false
	}
	return true
}

// applyISPTables 用运营商网段表设置 ISP。
//
// 规则：
//   - ISP 为空 → 直接填
//   - 网段表判定为云/IDC（优先级 >= 30）且当前是基础运营商 → 纠正
//     （分配方比转售线路更准确，如长城宽带的段租给腾讯云）
//   - 其余情况不动（运营商之间互换歧义大，不做判断）
func applyISPTables(db *store.DB, segs []geo.Segment, t *ispip.Table, dryRun bool) (filled, fixed int, err error) {
	var batch []store.SegmentUpdate
	flush := func() error {
		if dryRun || len(batch) == 0 {
			batch = batch[:0]
			return nil
		}
		if e := db.UpdateSegments(batch); e != nil {
			return e
		}
		batch = batch[:0]
		return nil
	}

	for i := range segs {
		sg := &segs[i]
		if sg.Geo.Country != "中国" {
			continue
		}
		// 必须用 uint64 求和：Start+End 会溢出 uint32（起点 >= 128.0.0.0 的段全中招）
		mid := uint32((uint64(sg.Start) + uint64(sg.End)) / 2)
		op := t.Lookup(mid)
		if op == "" {
			continue
		}
		if sg.Geo.ISP == "" {
			v := op
			batch = append(batch, store.SegmentUpdate{
				Start: sg.Start, ISP: &v, Source: TagSource(sg.Source, "+ispip"),
			})
			sg.Geo.ISP = op
			filled++
		} else if ispip.Priority(op) >= 30 && ispip.Priority(sg.Geo.ISP) < 30 {
			v := op
			batch = append(batch, store.SegmentUpdate{
				Start: sg.Start, ISP: &v, Source: TagSource(sg.Source, "+ispip"),
			})
			sg.Geo.ISP = op
			fixed++
		}
		if len(batch) >= 5000 {
			if e := flush(); e != nil {
				return filled, fixed, e
			}
		}
	}
	return filled, fixed, flush()
}

// TagSource 在来源串上追加标记，已存在则不重复添加。
func TagSource(src, tag string) string {
	if strings.Contains(src, tag) {
		return src
	}
	return src + tag
}

// SameGeo 判断两次查询结果是否指向同一省市（忽略 市/区 后缀差异与空值）。
// 供 refine 判断"三点是否一致"，两侧都为空也算一致（都无数据）。
func SameGeo(a, b provider.Result) bool {
	return trimAdminSuffix(a.Province) == trimAdminSuffix(b.Province) &&
		trimAdminSuffix(a.City) == trimAdminSuffix(b.City)
}

// SameName 比较两个行政区名，忽略 市/区/县 等后缀差异。
func SameName(a, b string) bool { return sameName(a, b) }

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

// clearCounty 在省市被改写后清空不再成立的区县/镇。
// 稍后 GeoCN 会按新的省市重新补，所以这里只是把错的先去掉。
func clearCounty(g *geo.GeoInfo, k *kindCount) {
	if g.County != "" {
		k.countyCleared++
	}
	g.County, g.Town = "", ""
}

// FillPair 按"省/市必须自洽"的原则补空，返回实际填了省、填了市。
//
// 只看空白字段独立填空会产出跨省组合：qqwry 给"江西省"、市为空，
// 百度给"广东省/广州市"，各填各的就成了"江西省|广州市"。
// 所以补任何一半，都要求另一半能与已有值对上：
//
//	省空+市空    → 成对填（都来自同一条百度结果，天然自洽）
//	省空+市有    → 只有百度给出的市与已有市一致时才补省
//	省有+市空    → 只有百度给出的省与已有省一致时才补市
//	省有+市有    → 不是"补空"，交给覆盖分支
func FillPair(g *geo.GeoInfo, res provider.Result) (provSet, citySet bool) {
	provEmpty := g.Province == ""
	cityEmpty := g.City == ""

	switch {
	case provEmpty && cityEmpty:
		if res.Province != "" {
			g.Province = res.Province
			provSet = true
		}
		if res.City != "" {
			g.City = res.City
			citySet = true
		}
	case provEmpty && !cityEmpty:
		if res.Province != "" && res.City != "" && sameName(res.City, g.City) {
			g.Province = res.Province
			provSet = true
		}
	case !provEmpty && cityEmpty:
		if res.City != "" && res.Province != "" && sameName(res.Province, g.Province) {
			g.City = res.City
			citySet = true
		}
	}
	return
}

// fillPair 补空并计数。
func fillPair(g *geo.GeoInfo, res provider.Result, k *kindCount) {
	provSet, citySet := FillPair(g, res)
	if provSet {
		k.prov++
	}
	if citySet {
		k.city++
	}
}

// decidePart 针对一个段内区间做决策，返回决策后的归属。
//
// 三种动作，优先级从高到低：
//  1. 补空：省/市/ISP 为空就填 —— 永远做，云段也做（这是 enrich 的主要价值）
//  2. 覆盖省市：仅 override=true 且非云段；百度说换省却给不出市 → 低置信度不动
//  3. ISP 冲突：两边都有值但不同 → 只记录不写库（qqwry 的"腾讯云/网宿"更细）
func decidePart(base geo.GeoInfo, res provider.Result, override bool) (geo.GeoInfo, kindCount, bool) {
	var (
		k         kindCount
		ispConflict bool
	)
	out := base
	if res.Province == "" && res.City == "" && res.ISP == "" {
		return out, k, false
	}

	provEmpty := out.Province == ""
	cityEmpty := out.City == ""

	// 1. 补空（省市必须成对自洽，否则会产出"江西省|广州市"这种错位组合）
	fillPair(&out, res, &k)

	// 2. 覆盖（云/CDN 段禁止覆盖：地理位置由路由决定，探到的是 PoP 城市）
	if override && !regions.IsCloudISP(out.ISP) {
		switch {
		case !provEmpty && res.Province != "" && !sameName(out.Province, res.Province):
			if out.City != "" && res.City == "" {
				break // 低置信度：说换省却给不出市，整条不动
			}
			oldCity := out.City
			out.Province = res.Province
			out.City = res.City // 省变了市必须同步，否则留下跨省组合
			k.prov++
			k.city++
			// 市变了，挂在旧市下的区县/镇不再成立，清掉（稍后由 GeoCN 按新省市重填）
			if oldCity != "" && !sameName(oldCity, res.City) {
				clearCounty(&out, &k)
			}
		case !cityEmpty && res.City != "" && res.Province != "" &&
			sameName(out.Province, res.Province) && !sameName(out.City, res.City):
			// 只换市：必须百度也给了省、且省与我们一致，否则无法验证归属
			out.City = res.City
			k.city++
			clearCounty(&out, &k)
		}
	}

	// 3. ISP：只补空，冲突只记录
	if out.ISP == "" && res.ISP != "" {
		out.ISP = res.ISP
		k.isp++
	} else if out.ISP != "" && res.ISP != "" && out.ISP != res.ISP {
		ispConflict = true
	}
	return out, k, ispConflict
}


// segAgg 累积一个段的所有片。
type segAgg struct {
	pieces []geo.Segment
}

// probeCount 决定一个段要布几个探测点。
//
// 只探一个中点会把一个城市的结论套到整段上：一个 /10（419 万 IP）只探一个点，
// 误差比任何数据源的问题都大。所以按段长阶梯加密：
//
//	≤ ProbeStep        → 1 个点
//	每 ProbeStep 一个点，封顶 ProbeCap 个
//
// 探测点还会按 /24 去重，同一个 /24 内的多片共用一次请求。
func probeCount(opt Options, sg *geo.Segment) int {
	if opt.ProbeStep <= 0 {
		return 1
	}
	span := uint64(sg.End) - uint64(sg.Start) + 1
	n := int((span + uint64(opt.ProbeStep) - 1) / uint64(opt.ProbeStep))
	if n < 1 {
		n = 1
	}
	if opt.ProbeCap > 0 && n > opt.ProbeCap {
		n = opt.ProbeCap
	}
	return n
}

// flushSegs 把累积的片写回库。
//
// 全段一个结论 → 一条 UPDATE（快且不改变段结构）；
// 片间结论不同 → ReplaceSegment 整段替换成多片（单事务删+插，保持连续不重不漏）。
func flushSegs(db *store.DB, segs []geo.Segment, aggs []*segAgg, touched []int, st *Stats) {
	var ups []store.SegmentUpdate
	for _, idx := range touched {
		sg := segs[idx]
		a := aggs[idx]
		if len(a.pieces) == 0 {
			continue
		}
		if len(a.pieces) == 1 {
			p := a.pieces[0]
			if p.Start != sg.Start || p.End != sg.End || p.Geo == sg.Geo {
				continue
			}
			ups = append(ups, diffUpdate(sg, p))
			if len(ups) >= 2000 {
				if err := db.UpdateSegments(ups); err != nil {
					log.Printf("写库失败: %v", err)
				}
				ups = ups[:0]
			}
			continue
		}

		// 多片：先看有没有结论变化，没变化就别白白切段
		changed := false
		for _, p := range a.pieces {
			if p.Geo != sg.Geo {
				changed = true
				break
			}
		}
		if !changed {
			continue
		}
		st.SplitSegs++
		if err := db.ReplaceSegment(sg.Start, sg.End, a.pieces); err != nil {
			log.Printf("切分段失败 %s: %v", geo.Uint2IP(sg.Start), err)
		}
	}
	if len(ups) > 0 {
		if err := db.UpdateSegments(ups); err != nil {
			log.Printf("写库失败: %v", err)
		}
	}
}

// diffUpdate 只把变化的列写进 UPDATE，避免无谓覆盖。
func diffUpdate(old geo.Segment, p geo.Segment) store.SegmentUpdate {
	up := store.SegmentUpdate{Start: old.Start, Source: p.Source}
	if p.Geo.Province != old.Geo.Province {
		v := p.Geo.Province
		up.Province = &v
	}
	if p.Geo.City != old.Geo.City {
		v := p.Geo.City
		up.City = &v
	}
	if p.Geo.County != old.Geo.County {
		v := p.Geo.County
		up.County = &v
	}
	if p.Geo.Town != old.Geo.Town {
		v := p.Geo.Town
		up.Town = &v
	}
	if p.Geo.ISP != old.Geo.ISP {
		v := p.Geo.ISP
		up.ISP = &v
	}
	return up
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
