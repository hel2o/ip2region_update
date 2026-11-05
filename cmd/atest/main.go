// 命令行工具：对比固定限速与自适应限速的真实吞吐。
//
//	atest -mode fixed -qps 60 -workers 16 -sec 60
//	atest -mode adaptive -qps 60 -workers 16 -sec 60
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hel2o/ip2region/internal/adaptive"
	"github.com/hel2o/ip2region/internal/provider"
)

func main() {
	mode := flag.String("mode", "adaptive", "fixed | adaptive")
	qps := flag.Int("qps", 60, "速率上限")
	workers := flag.Int("workers", 16, "并发")
	sec := flag.Int("sec", 60, "运行秒数")
	flag.Parse()

	p := provider.NewQifu(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*sec)*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		latencies []float64 // 秒
	)
	var ok, fail int64
	curQPS := make(chan float64, 64)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				curQPS <- 0
			}
		}
	}()

	var actl *adaptive.Controller
	var tokens chan struct{}
	if *mode == "adaptive" {
		actl = adaptive.New(*qps)
		actl.Start(ctx)
		tokens = actl.Tokens()
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					actl.Adjust()
					curQPS <- actl.Current()
				}
			}
		}()
	} else {
		tokens = make(chan struct{}, 8)
		go func() {
			iv := time.Second / time.Duration(*qps)
			t := time.NewTicker(iv)
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
	}

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				select {
				case <-ctx.Done():
					return
				case <-tokens:
				}
				ip := uint32(rand.Uint32())
				t0 := time.Now()
				_, err := p.Lookup(ctx, ip)
				rt := time.Since(t0)
				f := err != nil
				atomic.AddInt64(&reqN, 1)
				if f {
					atomic.AddInt64(&fail, 1)
				} else {
					atomic.AddInt64(&ok, 1)
					mu.Lock()
					latencies = append(latencies, rt.Seconds())
					mu.Unlock()
				}
				if actl != nil {
					actl.Record(rt, f)
				}
			}
		}()
	}

	// 打印速率曲线
	go func() {
		last := int64(0)
		for {
			select {
			case <-ctx.Done():
				return
			case q := <-curQPS:
				n := atomic.LoadInt64(&reqN)
				if *mode == "adaptive" || q > 0 {
					if q == 0 {
						fmt.Printf("[%3ds] 实际 %d req/s  累计 %d  失败 %d\n",
							int(time.Since(t0s).Seconds()), n-last, n, atomic.LoadInt64(&fail))
					} else {
						fmt.Printf("[%3ds] 实际 %d req/s  目标 %.0f  累计 %d  失败 %d\n",
							int(time.Since(t0s).Seconds()), n-last, q, n, atomic.LoadInt64(&fail))
					}
				}
				last = n
			}
		}
	}()

	wg.Wait()

	sort.Float64s(latencies)
	pct := func(p float64) float64 {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[int(float64(len(latencies)-1)*p)]
	}
	fmt.Printf("\n==== %s qps上限=%d ====\n成功 %d  失败 %d  (%.1f%%)\n延迟 p50=%.0fms  p90=%.0fms  p99=%.0fms\n",
		*mode, *qps, atomic.LoadInt64(&ok), atomic.LoadInt64(&fail),
		float64(atomic.LoadInt64(&fail))/float64(atomic.LoadInt64(&ok)+atomic.LoadInt64(&fail))*100,
		pct(0.5)*1000, pct(0.9)*1000, pct(0.99)*1000)
}

var reqN int64
var t0s = time.Now()
