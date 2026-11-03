# ip2region 归属库构建工具

把纯真 `qqwry.dat` 转成 [ip2region](https://github.com/lionsoul2014/ip2region) 的 `.xdb`，
过程中用百度接口纠正省市与 ISP，最后叠加自定义 CIDR 覆盖。

输出区域串固定 6 段：

```
183.3.149.185|183.3.149.190|中国|广东省|广州市|番禺区|钟村|中国电信
                         └────────── 区域串 ──────────┘
```

## 字段来源约定

| 字段 | 主来源 | 兜底 |
|---|---|---|
| 国家 / 省 / 市 | 百度 opendata IP 接口 | qqwry |
| 区 / 镇 | **只用 qqwry** | 空 |
| ISP | 百度 | `ispip.m27.tech` CIDR 表 → qqwry |

百度 opendata 接口只到城市级（偶尔到区），**给不出区镇**。所以区镇一律保留纯真原值，
不用 API 结果覆盖。旧实现"API 查不到就整段覆盖"是导致归属降级的根因。

## 流水线

```
data/qqwry.dat
   │  parse     GBK→UTF8，拆省市区镇 ISP，规范化成全覆盖段表
   ▼
data/ip2region.db (SQLite)   ← 段表 + 请求缓存 + 覆盖表，支持断点续跑
   │  enrich    二分收敛 + 多数投票，只纠正省市/ISP
   ▼
build/ip2region.txt
   │  merge     叠加 data/custom/*.txt
   │  build     调官方 maker
   ▼
build/ip2region.xdb
```

## 命令

```bash
go run ./cmd/ip2region parse  -src data/qqwry.dat -db data/ip2region.db
go run ./cmd/ip2region enrich -db data/ip2region.db -qps 20
go run ./cmd/ip2region merge  -db data/ip2region.db -out build/ip2region.txt
go run ./cmd/ip2region build  -src build/ip2region.txt -out build/ip2region.xdb
go run ./cmd/ip2region verify -txt build/ip2region.txt
go run ./cmd/ip2region look   1.2.3.4
```

## 自定义覆盖

`data/custom/*.txt`，每行 `CIDR|区域串`，优先级最高：

```
181.0.0.0/12|中国|广东省|广州市|番禺区|钟村|中国电信
203.0.113.7|中国|北京市|北京市|||中国联通
```

## 查询侧注意

xdb 的 region 是 6 段字符串，**官方 SDK 的 `Region` 结构按 5 段解析，不能用**。
自行分列：

```go
parts := strings.SplitN(region, "|", 6)
// parts[0] 国家 parts[1] 省 parts[2] 市 parts[3] 区 parts[4] 镇 parts[5] ISP
```

## 测试

```bash
go test ./...
```

`testdata/golden/ip2region_20250721_qqwry.xdb` 是旧实现产出的基准库，用于回归对拍。

## 目录

```
cmd/ip2region/     唯一入口，子命令分发
internal/geo/      GeoInfo / Segment / 区间运算（覆盖、合并、校验）
internal/qqwry/    dat 解析
internal/store/    SQLite 持久化
internal/provider/ 百度等外部数据源
internal/enrich/   采样收敛、限流、断点
internal/xdbbuild/ 官方 maker 封装
data/              qqwry.dat、custom 覆盖表
testdata/golden/   回归基准
_legacy/           旧实现归档（不进编译，已 gitignore）
```
