package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hel2o/ip2region/internal/geo"
	"github.com/hel2o/ip2region/internal/xdbmaker"
)

//go:embed web/index.html
var webFS embed.FS

// lookupItem 是一条查询结果。
type lookupItem struct {
	Query    string `json:"query"`         // 用户输入的原始串
	IP       string `json:"ip"`            // 实际查询的 IP
	OK       bool   `json:"ok"`            // 是否查到
	Err      string `json:"err,omitempty"` // 出错原因
	Country  string `json:"country,omitempty"`
	Province string `json:"province,omitempty"`
	City     string `json:"city,omitempty"`
	County   string `json:"county,omitempty"`
	Town     string `json:"town,omitempty"`
	ISP      string `json:"isp,omitempty"`
	Region   string `json:"region,omitempty"` // region 原始串（6 段）
	Start    string `json:"start,omitempty"`  // 所在段起点
	End      string `json:"end,omitempty"`    // 所在段终点
	Size     uint64 `json:"size,omitempty"`   // 段内 IP 数
}

// runServe 启动一个本地查询页面，用于人工核验数据准确性。
func runServe(args []string) error {
	fs := newFlagSet("serve", "启动本地查询页面（核验数据用）")
	addr := fs.String("addr", "127.0.0.1:8899", "监听地址，默认只监听本机")
	xdbPath := fs.String("xdb", "build/ip2region.xdb", "xdb 路径")
	_ = fs.Parse(args)

	data, err := os.ReadFile(*xdbPath)
	if err != nil {
		return fmt.Errorf("读取 xdb: %w", err)
	}
	s, err := xdbmaker.NewWithBuffer(data)
	if err != nil {
		return fmt.Errorf("打开 xdb: %w", err)
	}
	defer s.Close()

	sample := inspectXDB(s)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, err := webFS.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("/api/meta", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"file":   *xdbPath,
			"bytes":  len(data),
			"sample": sample,
		})
	})

	mux.HandleFunc("/api/lookup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
			http.Error(w, "请求体解析失败: "+err.Error(), http.StatusBadRequest)
			return
		}
		items := lookupText(s, req.Text)
		writeJSON(w, map[string]any{"results": items, "count": len(items)})
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("查询页面: http://%s/", *addr)
	log.Printf("库文件 %s（%.1f MB）", *xdbPath, float64(len(data))/1024/1024)
	return srv.ListenAndServe()
}

// lookupText 把整段文本拆成若干查询并逐个解析。
func lookupText(s *xdbmaker.Searcher, text string) []lookupItem {
	items := make([]lookupItem, 0, 32)
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == '\t' || r == ' '
		}) {
			if tok = strings.TrimSpace(tok); tok != "" {
				items = append(items, lookupOne(s, tok))
			}
			if len(items) >= 2000 {
				return items
			}
		}
	}
	return items
}

// lookupOne 支持三种写法：单个 IP、CIDR、起止 IP（a-b）。
func lookupOne(s *xdbmaker.Searcher, q string) lookupItem {
	it := lookupItem{Query: q}

	var probe uint32
	if strings.Contains(q, "/") {
		// CIDR：查网络地址本身
		_, ipnet, err := net.ParseCIDR(q)
		if err != nil {
			it.Err = "CIDR 格式不对"
			return it
		}
		probe = ipToUint(ipnet.IP)
	} else if strings.Contains(q, "-") {
		// 起止 IP：查中点
		parts := strings.SplitN(q, "-", 2)
		a, err1 := parseIPv4(strings.TrimSpace(parts[0]))
		b, err2 := parseIPv4(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			it.Err = "起止 IP 格式不对"
			return it
		}
		if a > b {
			a, b = b, a
		}
		probe = uint32((uint64(a) + uint64(b)) / 2)
	} else {
		v, err := parseIPv4(q)
		if err != nil {
			it.Err = "IP 格式不对"
			return it
		}
		probe = v
	}

	it.IP = geo.Uint2IP(probe)
	region, start, end, err := s.SearchWithRange(probe)
	if err != nil {
		it.Err = err.Error()
		return it
	}
	if region == "" {
		it.Err = "库里没有这个地址"
		return it
	}

	it.OK = true
	it.Region = region
	it.Start = geo.Uint2IP(start)
	it.End = geo.Uint2IP(end)
	it.Size = uint64(end) - uint64(start) + 1

	parts := strings.Split(region, "|")
	for len(parts) < 6 {
		parts = append(parts, "")
	}
	it.Country, it.Province, it.City = parts[0], parts[1], parts[2]
	it.County, it.Town, it.ISP = parts[3], parts[4], parts[5]
	return it
}

func parseIPv4(s string) (uint32, error) {
	ip := net.ParseIP(s)
	if ip == nil {
		return 0, fmt.Errorf("invalid ip")
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0, fmt.Errorf("not ipv4")
	}
	return ipToUint(v4), nil
}

func ipToUint(ip net.IP) uint32 {
	v4 := ip.To4()
	if v4 == nil {
		return 0
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// inspectXDB 抽几个典型 IP 做自检（库坏了能立刻从首页看出来）。
func inspectXDB(s *xdbmaker.Searcher) []map[string]string {
	sampleIPs := []string{"1.1.1.1", "8.8.8.8", "223.5.5.5", "183.3.149.185", "114.114.114.114"}
	out := make([]map[string]string, 0, len(sampleIPs))
	for _, ip := range sampleIPs {
		v, err := parseIPv4(ip)
		if err != nil {
			continue
		}
		region, _, _, err := s.SearchWithRange(v)
		if err != nil || region == "" {
			continue
		}
		out = append(out, map[string]string{"ip": ip, "region": region})
	}
	return out
}
