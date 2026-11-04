package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const target = "https://qifu.baidu.com/api/v1/ip-portrait/brief-info?ip="

var ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0"

type result struct {
	code200 int64
	code403 int64
	code429 int64
	other   int64
	errCnt  int64
	inner   sync.Map // inner code -> count
	latMu   sync.Mutex
	lats    []time.Duration
}

func main() {
	conc, _ := strconv.Atoi(os.Args[1])
	total, _ := strconv.Atoi(os.Args[2])

	tr := &http.Transport{
		MaxIdleConns:        conc * 2,
		MaxIdleConnsPerHost: conc * 2,
		MaxConnsPerHost:     conc * 2,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: tr}

	var idx int64
	var r result
	var wg sync.WaitGroup
	start := time.Now()

	fetch := func() {
		n := atomic.AddInt64(&idx, 1)
		a := int(n%250) + 1
		b := int((n*7)%250) + 1
		c := int((n*13)%250) + 1
		ip := fmt.Sprintf("116.%d.%d.%d", a, b, c)
		req, _ := http.NewRequest("GET", target+ip, nil)
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Referer", "https://qifu.baidu.com/")
		t0 := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			atomic.AddInt64(&r.errCnt, 1)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		d := time.Since(t0)
		r.latMu.Lock()
		r.lats = append(r.lats, d)
		r.latMu.Unlock()

		switch resp.StatusCode {
		case 200:
			atomic.AddInt64(&r.code200, 1)
			var m struct {
				Code int `json:"code"`
			}
			json.Unmarshal(body, &m)
			v, _ := r.inner.LoadOrStore(m.Code, new(int64))
			atomic.AddInt64(v.(*int64), 1)
		case 403:
			atomic.AddInt64(&r.code403, 1)
		case 429:
			atomic.AddInt64(&r.code429, 1)
		default:
			atomic.AddInt64(&r.other, 1)
		}
	}

	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for atomic.AddInt64(&idx, 1) <= int64(total) {
				atomic.AddInt64(&idx, -1)
				fetch()
			}
		}()
	}
	wg.Wait()
	el := time.Since(start)

	fmt.Printf("并发=%d 总量=%d 耗时=%.1fs 实际QPS=%.1f\n", conc, total, el.Seconds(), float64(total)/el.Seconds())
	fmt.Printf("http200=%d http403=%d http429=%d 其它=%d 网络错误=%d\n", r.code200, r.code403, r.code429, r.other, r.errCnt)
	r.inner.Range(func(k, v any) bool {
		fmt.Printf("  业务码 code=%v 次数=%d\n", k, *v.(*int64))
		return true
	})
	if len(r.lats) > 0 {
		var sum time.Duration
		min, max := r.lats[0], r.lats[0]
		for _, l := range r.lats {
			sum += l
			if l < min {
				min = l
			}
			if l > max {
				max = l
			}
		}
		fmt.Printf("延迟 平均=%v 最小=%v 最大=%v\n", sum/time.Duration(len(r.lats)), min, max)
	}
}
