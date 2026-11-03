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
)

const (
	qqwryVersionURL  = "https://raw.githubusercontent.com/metowolf/qqwry.dat/main/version.json"
	qqwryDownloadURL = "https://github.com/metowolf/qqwry.dat/releases/latest/download/qqwry.dat"
)

// runFetch 下载最新的 metowolf 版 qqwry.dat。
//
// 只做下载 + 原子替换，不自动跑 parse：换库会让所有段重新编号，
// 是不是要用新数据应该由人决定（毕竟 enrich 已经按旧段跑过一轮）。
func runFetch(args []string) error {
	fs := newFlagSet("fetch", "下载最新 qqwry.dat")
	out := fs.String("out", "data/qqwry.dat", "保存路径")
	force := fs.Bool("force", false, "即使内容相同也重写")
	_ = fs.Parse(args)

	client := &http.Client{Timeout: 3 * time.Minute}

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
