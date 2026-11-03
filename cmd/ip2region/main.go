// ip2region 构建工具：把纯真 qqwry.dat 转成 ip2region 的 xdb 库，
// 过程中用百度接口纠正省市/ISP，最后叠加自定义 CIDR 覆盖。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

const usage = `ip2region 构建工具

用法: ip2region <command> [flags]

命令:
  fetch     下载最新 qqwry.dat 到 data/qqwry.dat
  parse     解析 qqwry.dat，规范化后写入 SQLite 段表
  enrich    调百度接口纠正省市与 ISP（带缓存、限流、断点续跑）
  merge     叠加自定义 CIDR 覆盖，输出最终 txt
  build     调 xdb maker 生成 .xdb
  verify    校验段表连续性/全覆盖，并与参考 xdb 抽样对拍
  look      查询单个 IP（调试用）

每个命令都支持 -h 查看专属参数。
`

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "fetch":
		err = runFetch(args)
	case "parse":
		err = runParse(args)
	case "enrich":
		err = runEnrich(args)
	case "merge":
		err = runMerge(args)
	case "build":
		err = runBuild(args)
	case "verify":
		err = runVerify(args)
	case "look":
		err = runLook(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("%s 失败: %v", cmd, err)
	}
}

func newFlagSet(name, desc string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "%s\n\n参数:\n", desc)
		fs.PrintDefaults()
	}
	return fs
}

func notImplemented(phase string) error {
	return fmt.Errorf("尚未实现（计划在 %s）", phase)
}

func runFetch(args []string) error {
	fs := newFlagSet("fetch", "下载最新 qqwry.dat")
	out := fs.String("out", "data/qqwry.dat", "保存路径")
	_ = fs.Parse(args)
	_ = out
	return notImplemented("阶段 1")
}


func runEnrich(args []string) error {
	fs := newFlagSet("enrich", "调百度接口纠正省市与 ISP")
	db := fs.String("db", "data/ip2region.db", "SQLite 路径")
	qps := fs.Int("qps", 20, "全局请求速率上限")
	_ = fs.Parse(args)
	_ = db
	_ = qps
	return notImplemented("阶段 3")
}

func runMerge(args []string) error {
	fs := newFlagSet("merge", "叠加自定义覆盖并输出最终 txt")
	db := fs.String("db", "data/ip2region.db", "SQLite 路径")
	custom := fs.String("custom", "data/custom", "自定义 CIDR 目录")
	out := fs.String("out", "build/ip2region.txt", "输出 txt")
	_ = fs.Parse(args)
	_ = db
	_ = custom
	_ = out
	return notImplemented("阶段 5")
}



