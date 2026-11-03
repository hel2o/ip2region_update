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
data/ip2region.db (SQLite)   ← 段表 + 请求缓存，缓存即断点，重跑零请求
   │  enrich    全段 /24 去重 + 中点探测，只纠正省市/ISP
   │  refine    大段二分收敛细分（3 点探测，不一致就切开）
   ▼
build/ip2region.txt
   │  merge     叠加 data/custom/*.txt（优先级最高）
   │  build     调官方 maker
   ▼
build/ip2region.xdb
```

## 命令

```bash
# 1. 解析 qqwry.dat → 段表（约 10s）
go run ./cmd/ip2region parse -src data/qqwry.dat -db data/ip2region.db

# 2. 百度增强（可反复跑，命中缓存零请求；Ctrl-C 随时中断，重跑接着来）
go run ./cmd/ip2region enrich -limit 200 -dry-run     # 试跑 200 次请求
go run ./cmd/ip2region enrich -qps 30 -workers 8      # 全量约 11 万次请求 / 1 小时

# 3. 大段细分（只处理 >4096 IP 的段，约 3 次请求/段，可反复跑）
go run ./cmd/ip2region refine -threshold 4096 -depth 2 -qps 20

# 4. 叠加自定义覆盖 → txt，并用 txt 生成 xdb
go run ./cmd/ip2region merge -out build/ip2region.txt
go run ./cmd/ip2region build -from txt -src build/ip2region.txt -out build/ip2region.xdb

# 5. 校验 + 与旧库对拍
go run ./cmd/ip2region verify -n 50000

# 6. 查询
go run ./cmd/ip2region look -xdb build/ip2region.xdb 183.3.149.185
```

### enrich 的规则（别改错方向）

- 区/镇 **永不改**，只认 qqwry
- 省/市：百度非空且与 qqwry 不同才改；改省时市必须同步（否则会留下跨省组合）
- ISP：只在原有为空时填充；两边都有值但不同**只记冲突不写库**
  （qqwry 的"腾讯云/网宿"比百度的"电信"更细）
- 同 /24 共用一次查询结果（`-mask /16` 可再放大，`none` 关闭去重）
- 冲突样例写到 `build/enrich-conflicts.tsv`，可人工抽检

大段为什么必须细分：19 万中国段里有 18,685 段大于 /20，合计覆盖中国 83% 的地址空间。
只探中点会把一个城市的结论套到几百万个 IP 上。refine 对这类段探 起点/中点/终点，
三点一致才整段确认，不一致就二分切开（深度受限，最多 4 片）。

实测：`1.15.0.0/16` 原标"天津"，细分后是 北京/北京/北京/大连 四片。

实测效果（1.12.0.0/14 段，qqwry 标"广东广州/腾讯云"，百度实测在各地方正宽带）：

```
增强前: 中国|广东省|广州|||腾讯云
增强后: 中国|北京市|北京市|||腾讯云      ← 地理位置被纠正，归属方信息保留
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
internal/provider/ 百度 opendata 接口（可插拔，换高德/ip138 只加一个文件）
internal/enrich/   /24 去重、限流、缓存复用、冲突裁定
internal/refine/   大段二分收敛细分
internal/xdbmaker/ 官方 maker + searcher（Apache 2.0，收编进仓库自包含）
data/              qqwry.dat、custom 覆盖表
testdata/golden/   回归基准
_legacy/           旧实现归档（不进编译，已 gitignore）
```
