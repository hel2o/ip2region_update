package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hel2o/ip2region/internal/ispip"
)

const (
	qqwryVersionURL  = "https://raw.githubusercontent.com/metowolf/qqwry.dat/main/version.json"
	qqwryDownloadURL = "https://github.com/metowolf/qqwry.dat/releases/latest/download/qqwry.dat"

	geocnMMDBURL  = "https://github.com/ljxi/GeoCN/releases/latest/download/GeoCN.mmdb"
	geocnCodesURL = "https://raw.githubusercontent.com/ljxi/GeoCN/main/data/full.txt"
)

// runFetch 下载最新的 metowolf 版 qqwry.dat。
//
// 只做下载 + 原子替换，不自动跑 parse：换库会让所有段重新编号，
// 是不是要用新数据应该由人决定（毕竟 enrich 已经按旧段跑过一轮）。
func runFetch(args []string) error {
	fs := newFlagSet("fetch", "下载最新 qqwry.dat")
	out := fs.String("out", "data/qqwry.dat", "保存路径")
	ispDir := fs.String("ispip", "data/ispip", "运营商网段表目录（空串=不下）")
	geocnDir := fs.String("geocn", "data/geocn", "GeoCN 目录（空串=不下）")
	skipQqwry := fs.Bool("skip-qqwry", false, "跳过 qqwry.dat 下载")
	force := fs.Bool("force", false, "即使内容相同也重写")
	_ = fs.Parse(args)

	client := &http.Client{Timeout: 3 * time.Minute}

	if *ispDir != "" {
		if err := fetchISPIP(client, *ispDir); err != nil {
			return err
		}
	}
	if *geocnDir != "" {
		if err := fetchGeoCN(client, *geocnDir); err != nil {
			return err
		}
	}
	if *skipQqwry {
		return nil
	}

	// 1. 远端最新版本号（拿不到不致命，只影响日志）
	if v, err := fetchVersion(client); err == nil && v != "" {
		log.Printf("远端最新版本: %s", v)
	} else if err != nil {
		log.Printf("获取版本信息失败（继续下载）: %v", err)
	}

	// 2. 已有文件的 md5
	oldMD5 := ""
	if fi, err := os.Stat(*out); err == nil {
		if sum, err := fileMD5(*out); err == nil {
			oldMD5 = sum
			log.Printf("现有文件: %s (%.1f MB, md5 %s)", *out, float64(fi.Size())/1024/1024, sum[:8])
		}
	}

	// 3. 下载到临时文件，校验后再原子替换
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	tmp := *out + ".tmp"
	if err := download(client, qqwryDownloadURL, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	fi, err := os.Stat(tmp)
	if err != nil {
		return err
	}
	// qqwry.dat 一直 20MB 以上，明显偏小说明下到了错误页
	if fi.Size() < 10<<20 {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载内容异常（仅 %d 字节），已丢弃", fi.Size())
	}

	newMD5, err := fileMD5(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}

	if oldMD5 == newMD5 && !*force {
		_ = os.Remove(tmp)
		log.Printf("内容与现有文件一致（md5 %s），无需更新", newMD5[:8])
		return nil
	}

	if err := os.Rename(tmp, *out); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换文件: %w", err)
	}

	log.Printf("已更新: %s (%.1f MB, md5 %s)", *out, float64(fi.Size())/1024/1024, newMD5[:8])
	log.Printf("下一步: go run ./cmd/ip2region parse  (注意：换库后旧段的增强结果需重新跑)")
	return nil
}

// fetchISPIP 下载 ispip.m27.tech 的各运营商网段表。
// 页面正文就是纯 CIDR 列表，直接原样存盘，交给 ispip 包解析。
func fetchISPIP(c *http.Client, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keys := ispip.PageKeys()
	ok, fail := 0, 0
	for _, k := range keys {
		url := ispip.SourceURL + k + ".html"
		path := filepath.Join(dir, k+".html")
		if err := download(c, url, path); err != nil {
			log.Printf("  下载 %s 失败: %v", k, err)
			fail++
			continue
		}
		if fi, err := os.Stat(path); err == nil && fi.Size() < 200 {
			log.Printf("  %s 内容异常（%d 字节），跳过", k, fi.Size())
			fail++
			continue
		}
		ok++
	}
	log.Printf("运营商网段表: 成功 %d 个页面，失败 %d 个 → %s", ok, fail, dir)
	if ok == 0 {
		return fmt.Errorf("网段表全部下载失败，检查网络或站点是否可用")
	}
	return nil
}

// fetchGeoCN 下载 GeoCN 离线库（MMDB）与行政区划编码表。
// 数据源：https://github.com/ljxi/GeoCN
func fetchGeoCN(c *http.Client, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	mmdb := filepath.Join(dir, "GeoCN.mmdb")
	tmp := mmdb + ".tmp"
	if err := download(c, geocnMMDBURL, tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载 GeoCN.mmdb: %w", err)
	}
	fi, err := os.Stat(tmp)
	if err != nil {
		return err
	}
	// GeoCN.mmdb 约 9MB，明显偏小说明下到了错误页
	if fi.Size() < 1<<20 {
		_ = os.Remove(tmp)
		return fmt.Errorf("GeoCN.mmdb 体积异常（%d 字节）", fi.Size())
	}
	if err := os.Rename(tmp, mmdb); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	codes := filepath.Join(dir, "full.txt")
	if err := download(c, geocnCodesURL, codes+".tmp"); err != nil {
		_ = os.Remove(codes + ".tmp")
		return fmt.Errorf("下载行政区划编码表: %w", err)
	}
	if err := os.Rename(codes+".tmp", codes); err != nil {
		return err
	}

	log.Printf("GeoCN: GeoCN.mmdb (%.1f MB) + full.txt → %s", float64(fi.Size())/1024/1024, dir)
	return nil
}

// fetchVersion 取远端最新版本号。
func fetchVersion(c *http.Client) (string, error) {
	resp, err := c.Get(qqwryVersionURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var v struct {
		Latest string `json:"latest"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", err
	}
	return v.Latest, nil
}

// download 下载到指定路径。
func download(c *http.Client, url, path string) error {
	resp, err := c.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("写入失败: %w", err)
	}
	return f.Close()
}

func fileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
