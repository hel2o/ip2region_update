package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const target = "https://qifu.baidu.com/api/v1/ip-portrait/brief-info?ip="

var ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0"

// 固定池：这些 IP 已知能返回完整数据，用于识别"200 但 data 为空"的软限流
var pool = []string{
	"183.1.0.0", "183.13.10.239", "8.8.8.8", "223.5.5.5", "114.114.114.114",
	"1.1.1.1", "119.29.29.29", "116.24.0.1", "115.28.0.1", "101.226.4.6",
	"202.108.22.5", "61.135.169.121", "180.76.76.76", "180.101.49.11", "211.136.17.1",
}

type win struct {
	total    int64
	httpOK   int64 // HTTP 200
	codeOK   int64 // 业务 code=200
	withData int64 // query_ip 非空
	empty    int64 // 200 但 data 全 null
	http4xx  int64
	http5xx  int64
	netErr   int64
	lat      []time.Duration
	others   map[string]int64
}

func main() {
	conc, _ := strconv.Atoi(os.Args[1])
	durSec, _ := strconv.Atoi(os.Args[2])
	interval := 15 * time.Second

	tr := &http.Transport{
		MaxIdleConns:        conc * 3,
		MaxIdleConnsPerHost: conc * 3,
		MaxConnsPerHost:     conc * 3,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
		// Proxy 留空 = 直连，不走本机 127.0.0.1:17460
	}
	client := &http.Client{Timeout: 25 * time.Second, Transport: tr}

	var (
		mu      sync.Mutex
		cur     win
		cum     win
		stop    int64
		firstAno string
	)

	newWin := func() win {
		return win{others: map[string]int64{}}
	}
	cur = newWin()
	cum = newWin()

	record := func(w *win, d time.Duration) {
		w.total++
		if d > 0 {
			w.lat = append(w.lat, d)
		}
	}

	for w := 0; w < conc; w++ {
		go func(seed int64) {
			rnd := rand.New(rand.NewSource(time.Now().UnixNano() + seed))
			n := 0
			for atomic.LoadInt64(&stop) == 0 {
				var ip string
				if rnd.Intn(10) < 8 {
					ip = pool[rnd.Intn(len(pool))]
				} else {
					n++
					ip = fmt.Sprintf("%d.%d.%d.%d", 110+rnd.Intn(30), rnd.Intn(250)+1, rnd.Intn(250)+1, rnd.Intn(250)+1)
				}
				req, _ := http.NewRequest("GET", target+ip, nil)
				req.Header.Set("User-Agent", ua)
				req.Header.Set("Referer", "https://qifu.baidu.com/")
				t0 := time.Now()
				resp, err := client.Do(req)
				d := time.Since(t0)
				if err != nil {
					mu.Lock()
					record(&cur, d)
					record(&cum, d)
					cur.netErr++
					cum.netErr++
					if firstAno == "" {
						firstAno = fmt.Sprintf("netErr@%s ip=%s err=%v", time.Now().Format("15:04:05"), ip, err)
					}
					mu.Unlock()
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()

				mu.Lock()
				record(&cur, d)
				record(&cum, d)
				switch {
				case resp.StatusCode == 200:
					cur.httpOK++
					cum.httpOK++
					var m struct {
						Code int             `json:"code"`
						Data json.RawMessage `json:"data"`
						Msg  string          `json:"message"`
					}
					json.Unmarshal(body, &m)
					if m.Code == 200 {
						cur.codeOK++
						cum.codeOK++
						var dd struct {
							QueryIP *string `json:"query_ip"`
						}
						json.Unmarshal(m.Data, &dd)
						if dd.QueryIP != nil && *dd.QueryIP != "" {
							cur.withData++
							cum.withData++
						} else {
							cur.empty++
							cum.empty++
						}
					} else {
						key := fmt.Sprintf("bizcode=%d", m.Code)
						cur.others[key]++
						cum.others[key]++
						if firstAno == "" {
							firstAno = fmt.Sprintf("biz@%s ip=%s resp=%s", time.Now().Format("15:04:05"), ip, trunc(body, 240))
						}
					}
				case resp.StatusCode >= 500:
					cur.http5xx++
					cum.http5xx++
					key := fmt.Sprintf("http=%d", resp.StatusCode)
					cur.others[key]++
					cum.others[key]++
					if firstAno == "" {
						firstAno = fmt.Sprintf("http@%s ip=%s resp=%s", time.Now().Format("15:04:05"), ip, trunc(body, 240))
					}
				default:
					cur.http4xx++
					cum.http4xx++
					key := fmt.Sprintf("http=%d", resp.StatusCode)
					cur.others[key]++
					cum.others[key]++
					if firstAno == "" {
						firstAno = fmt.Sprintf("http@%s ip=%s resp=%s", time.Now().Format("15:04:05"), ip, trunc(body, 240))
					}
				}
				mu.Unlock()
			}
		}(int64(w))
	}

	start := time.Now()
	deadline := start.Add(time.Duration(durSec) * time.Second)
	fmt.Printf("开始：并发=%d 时长=%ds 目标=%s\n", conc, durSec, time.Now().Format("15:04:05"))
	fmt.Println("时间      窗口数  窗口QPS  累计数   200  数据正常  空data  4xx  5xx  网络错  平均延迟  p50     p99     窗口最大")
	fmt.Println("---------------------------------------------------------------------------------------------------------")

	ticker := time.NewTicker(interval)
	for now := range ticker.C {
		mu.Lock()
		w := cur
		cur = newWin()
		cw := cum
		mu.Unlock()
		printRow(now, w, cw, interval)
		if now.After(deadline) {
			break
		}
	}
	ticker.Stop()
	atomic.StoreInt64(&stop, 1)
	time.Sleep(2 * time.Second)

	mu.Lock()
	fmt.Println("---------------------------------------------------------------------------------------------------------")
	el := time.Since(start)
	fmt.Printf("\n总计：时长=%.0fs 请求=%d 平均QPS=%.1f\n", el.Seconds(), cum.total, float64(cum.total)/el.Seconds())
	fmt.Printf("  HTTP200=%d  业务code=200=%d  数据正常=%d  空data=%d\n", cum.httpOK, cum.codeOK, cum.withData, cum.empty)
	fmt.Printf("  4xx=%d  5xx=%d  网络错误=%d  其他=%v\n", cum.http4xx, cum.http5xx, cum.netErr, cum.others)
	if firstAno != "" {
		fmt.Printf("  !! 首个异常：%s\n", firstAno)
	} else {
		fmt.Printf("  !! 全程零异常（无 4xx/5xx/业务错误码/网络错误）\n")
	}
	mu.Unlock()
}

func printRow(now time.Time, w, c win, iv time.Duration) {
	avg, p50, p99, mx := stat(w.lat)
	qps := float64(w.total) / iv.Seconds()
	fmt.Printf("%s  %6d  %7.1f  %7d  %5d  %8d  %6d  %4d  %4d  %6d  %8s  %6s  %6s  %8s\n",
		now.Format("15:04:05"), w.total, qps, c.total, w.httpOK, w.withData, w.empty,
		w.http4xx, w.http5xx, w.netErr, avg, p50, p99, mx)
}

func stat(l []time.Duration) (avg, p50, p99, mx time.Duration) {
	if len(l) == 0 {
		return
	}
	var s time.Duration
	for _, d := range l {
		s += d
	}
	avg = s / time.Duration(len(l))
	cp := make([]time.Duration, len(l))
	copy(cp, l)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	p50 = cp[len(cp)/2]
	p99 = cp[len(cp)*99/100]
	mx = cp[len(cp)-1]
	return
}

func trunc(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
