# ip2region 归属库构建工具

把纯真 `qqwry.dat` 转成 [ip2region](https://github.com/lionsoul2014/ip2region) 的 `.xdb`，
中间用百度 IP 接口补齐缺失的省/市/ISP，再叠加你自己的手工覆盖规则。

产出是一个 22MB 左右的单文件库，覆盖全部 42.9 亿个 IPv4 地址，无缝隙、无重叠。

```
$ ip2region look --xdb build/ip2region.xdb 183.3.149.185
183.3.149.185    中国 | 广东省 | 广州市 | 番禺区 | 钟村 | 中国电信
```

> **注意：区域串是 6 段，不是官方默认的 5 段。**
> 官方 SDK 的 `Region` 结构按 5 段解析，直接套用会字段错位。查询侧必须自己按 6 段切（见 [查询侧接入](#查询侧接入)）。

---

## 目录

- [快速开始](#快速开始)
- [环境要求](#环境要求)
- [命令总览](#命令总览)
- [流水线详解](#流水线详解)
  - [fetch：下载最新数据](#fetch下载最新数据)
  - [parse：解析成段表](#parse解析成段表)
  - [enrich：百度补空](#enrich百度补空)
  - [refine：边界递归收敛](#refine边界递归收敛)
  - [merge：叠加自定义覆盖](#merge叠加自定义覆盖)
  - [build：生成-xdb](#build生成-xdb)
  - [verify：校验与对拍](#verify校验与对拍)
  - [stats：质量报表](#stats质量报表)
  - [look：查询调试](#look查询调试)
- [发布更新](#发布更新同步到-xdb-仓库)
- [自定义覆盖规则](#自定义覆盖规则)
- [缓存与断点续跑](#缓存与断点续跑)
- [数据质量与已知限制](#数据质量与已知限制)
- [查询侧接入](#查询侧接入)
- [常见问题](#常见问题)
- [目录结构](#目录结构)
- [开发与测试](#开发与测试)

---

## 快速开始

已经跑过一遍的话，日常只需要这几步：

```bash
# 1. 更新纯真数据（可选，一个月一次足够）
go run ./cmd/ip2region fetch

# 2. 重新解析 + 补空（有缓存，几十秒）
go run ./cmd/ip2region parse
go run ./cmd/ip2region enrich -qps 60

# 3. 叠加自定义覆盖 → 生成 xdb
go run ./cmd/ip2region merge
go run ./cmd/ip2region build -from txt

# 4. 验收
go run ./cmd/ip2region verify -n 50000
go run ./cmd/ip2region stats
```

产物在 `build/ip2region.xdb`。

从零开始跑一遍完整流程（第一次会调百度接口，约 30~60 分钟，之后重跑几乎瞬时）：

```bash
go run ./cmd/ip2region fetch          # 下载 qqwry.dat（约 27MB）
go run ./cmd/ip2region parse          # 解析 → 141 万段，约 10 秒
go run ./cmd/ip2region enrich -qps 60 # 百度判省市 + 补空，25 万探测点
go run ./cmd/ip2region refine -qps 60 # 边界递归收敛到 /24
go run ./cmd/ip2region merge          # 叠加 data/custom/*.txt
go run ./cmd/ip2region build -from txt
go run ./cmd/ip2region verify -n 50000
```

---

## 环境要求

- Go 1.26（见 `go.mod`）。下限被 `modernc.org/sqlite` 和 `golang.org/x/text` 拉高了，
  想降到更低版本需要同时降这两个依赖
- 无需 CGO：SQLite 用纯 Go 驱动 `modernc.org/sqlite`，Windows 上直接编译，不用装 gcc
- 能访问 `raw.githubusercontent.com`、`github.com`（下载 qqwry 与 GeoCN）、
  `ispip.m27.tech`（下载运营商网段表）、`opendata.baidu.com`（补空）
- 磁盘约 200MB（qqwry.dat 27MB + SQLite 300MB 左右，缓存表是主要占用）

```bash
go build ./cmd/ip2region    # 也可以编译成单二进制
```

---

## 命令总览

| 命令 | 作用 | 产物 | 耗时（实测） |
|---|---|---|---|
| `fetch` | 下载 `qqwry.dat` + 网段表 + GeoCN | `data/qqwry.dat`、`data/ispip/`、`data/geocn/` | 40 秒 |
| `parse` | 解析 dat，拆省市区镇，落 SQLite | `data/ip2region.db` | 10 秒 |
| `enrich` | 调百度补省市/ISP | 改写段表 + `ip_cache` | 首次 30 分钟，缓存后 25 秒 |
| `refine` | 大段边界递归收敛到 /24 | 段表拆分 | 首次约 20 秒（多为缓存命中） |
| `merge` | 叠加 `data/custom/*.txt` | `build/ip2region.txt` | 1 秒 |
| `build` | 调官方 maker 生成 xdb | `build/ip2region.xdb` | 9 秒 |
| `verify` | 校验全覆盖 + 与旧库对拍 | 控制台报告 | 5 秒 |
| `stats` | 字段覆盖率报表 | 控制台报告 | 1 秒 |
| `look` | 查询单个 IP | 控制台输出 | 即时 |

每个命令都支持 `-h` 查看完整参数。

---

## 流水线详解

```
data/qqwry.dat
   │  parse     GBK→UTF8，拆省/市/区/镇/ISP，规范化成全覆盖段表
   ▼
data/ip2region.db (SQLite)     ← 段表 + 请求缓存，缓存即断点
   │  enrich    全段 /24 去重 + 中点探测，只补空
   │  refine    大段边界递归收敛，只往"结论不同"的半边收敛到 /24
   ▼
build/ip2region.txt
   │  merge     叠加 data/custom/*.txt（优先级最高）
   │  build     调官方 xdb maker
   ▼
build/ip2region.xdb
```

### fetch：下载最新数据

```bash
go run ./cmd/ip2region fetch
go run ./cmd/ip2region fetch -out /tmp/qqwry.dat   # 下到别处
go run ./cmd/ip2region fetch -force                # 内容相同也重写
```

一次做两件事：

1. 从 `metowolf/qqwry.dat` 的 release 拉最新 dat，先下到临时文件，校验体积（≥10MB）和 md5
   后才原子替换，内容没变就不动
2. 从 `ispip.m27.tech` 下载 21 个运营商/云厂商网段表到 `data/ispip/`（约 768KB）
3. 从 `ljxi/GeoCN` 下载离线库 `GeoCN.mmdb`（8.6MB）与行政区划编码表 `full.txt` 到 `data/geocn/`

```bash
go run ./cmd/ip2region fetch -skip-qqwry     # 只更新网段表
go run ./cmd/ip2region fetch -ispip ""        # 只更新 qqwry.dat
```

**不会自动跑 parse**：换库会让所有段重新编号，得重新增强，所以要不要换由你决定。

### parse：解析成段表

```bash
go run ./cmd/ip2region parse
go run ./cmd/ip2region parse -src data/qqwry.dat -db data/ip2region.db
```

做的事：

1. 按 LumaQQ/CZ88 格式解析索引区（7 字节：4 字节起始 IP + 3 字节偏移，小端序），
   正确处理 `0x01`（整体跳转）和 `0x02`（仅国家跳转，地区在 `mode+4`）两种模式
2. GB18030 解码，清理控制字符和竖线污染
3. 用 34 个省级行政区表拆 `中国–广东–广州–番禺–钟村` 这类串，
   同时兼容原版连写格式 `中国广东省广州市番禺区`
4. 规范化成"从 0.0.0.0 到 255.255.255.255 全覆盖、无重叠、无缝隙"的段表

实测：`151 万原始记录 → 141 万段`，其中中国 19.2 万段 / 4.03 亿 IP。

### enrich：百度判断省市 + 补 ISP

**探测密度按段长自适应。** 一个段只探一个中点会把一个城市的结论套到整段上——
`59.192.0.0/10`（419 万 IP）原本只探一个点。现在按段长切：

```
段长 ≤ 2048 IP        → 1 个探测点
段长 2048~131072 IP   → 每 2048 IP 一个点
段长 > 131072 IP      → 封顶 64 个点
```

切出的每一片独立探测、独立决策，片间结论不同就把原段切开。

实测 19.2 万中国段 → 32.9 万片 → **25.1 万个去重探测点**（按 /24 合并），
修正省 8,615 段、修正市 48,925 段。

```bash
# 试跑，只发 200 次请求，不写段表
go run ./cmd/ip2region enrich -limit 200 -dry-run

# 全量
go run ./cmd/ip2region enrich -qps 60 -workers 12

# 只处理省市为空的段
go run ./cmd/ip2region enrich -only-empty
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `-qps` | 20 | 全局请求速率上限 |
| `-workers` | 8 | 并发数 |
| `-limit` | 0 | 最多请求数，0 表示不限（试跑用） |
| `-mask` | `/24` | 网段去重粒度，可选 `/24` `/16` `none` |
| `-dry-run` | false | 只统计不写段表（**仍会写缓存**，避免重复烧请求） |
| `-override` | **true** | 用百度覆盖已有省市；传 `-override=false` 退回只补空 |
| `-ispip` | `data/ispip` | 运营商网段表目录，空串表示不用（建表前先跑 `fetch`） |
| `-geocn` | `data/geocn/GeoCN.mmdb` | GeoCN 离线库，空串表示不补区县 |
| `-probe-step` | 2048 | 每多少 IP 布一个探测点（0 = 退回"每段只探中点"） |
| `-probe-cap` | 64 | 每个段最多几个探测点 |
| `-only-empty` | false | 只处理省市缺失的段 |
| `-conflicts` | `build/enrich-conflicts.tsv` | ISP 冲突样例输出，空字符串则不写 |
| `-timeout` | 10s | 单请求超时 |
| `-progress` | 2000 | 每 N 条打一行进度 |

**核心规则（改代码前务必读）：**

- **默认只补空，不覆盖。** 实测百度在省市上的准确率和 qqwry 是同一量级
  （40 个改动样本交叉验证：新值 82.1%、旧值 82.1%，完全打平），覆盖等于抛硬币。
  真正的收益全在填空。
- **云/CDN/IDC 段永远只补空**（`regions.IsCloudISP`）。腾讯云、阿里云、CloudFlare、
  Akamai、BGP 多线、IDC 机房这类段的 IP 归属方是云厂商，但流量落在哪个 PoP 由路由决定，
  百度探到的是 PoP 城市而不是网段归属地。实测 `1.12.0.0/14`（腾讯云）百度说北京，
  qqwry 和另外两个独立源都说广东。这类段共 3,948 段 / 3,489 万 IP（中国空间 8.7%）。
- **省市被改写时，旧区县和镇一律清空**。挂在旧市下面的区县（如"长春市|玄武区"）在新市下
  不成立，先清掉再由 GeoCN 按新省市重填。
- **ISP 先看运营商网段表，再考虑百度。** `data/ispip/` 是 APNIC 分配记录，判"归属方"比百度
  可靠（见下节）。百度只负责填空。
- **区/镇永不改动。** 百度和 qqwry 都给不出可靠的区镇信息。
- **ISP 只补空。** 两边都有值但不同只记冲突，不写库——qqwry 的"腾讯云/网宿"
  比百度的"电信"更细。

请求量：19.2 万中国段按 `/24` 去重后是 11.4 万个探测点，每个点查一次百度。
30 QPS 约 1 小时，60 QPS 约 30 分钟。所有结果落 `ip_cache` 表，**重跑零请求**。

### 运营商网段表（ispip）

`data/ispip/*.html` 来自 [ispip.m27.tech](https://ispip.m27.tech/)，数据由 APNIC 分配记录 + whois
每日生成，每个运营商/云厂商一个页面，正文是纯 CIDR 列表。

它和百度的本质区别：**这是分配归属，不是探测点位置**。所以 `1.12.0.0/14`（腾讯云）不会被
说成"北京电信"，也不受 anycast / PoP 影响。

**执行顺序：网段表定 ISP → 百度改省市 → GeoCN 补区县。** GeoCN 放在百度之后，
因为它填区县前要先校验省市自洽，必须拿百度的最终值去校验。

enrich 会先用它定 ISP：

| 情况 | 动作 |
|---|---|
| 段的 ISP 为空，网段表有值 | 填（实测 1,437 段） |
| 网段表判定为云/IDC（腾讯云、阿里云、华为云、网宿…），原值是基础运营商 | 纠正（实测 339 段） |
| 两边都是基础运营商（电信↔联通） | 不动，歧义太大 |
| 网段表没覆盖 | 交给百度填空 |

优先级设计：云厂商/IDC（40）> 教育网/科技网/广电（20）> 基础运营商（10）。
不这样排的话，运营商列表会把云厂商的段抢走——同一个 `1.12.0.0/14` 在电信和腾讯云两个列表里都有。

**不收录 `othernet` / `hongkong` / `macau` / `taiwan` 四个页面**：它们是地域兜底桶，不是运营商，
拿来当 ISP 会把"台湾某固网"写成"台湾"。

实测它的判断和 qqwry 的 ISP 一致率 91.1%（有覆盖的段中），分歧里有 284 段是
"qqwry 说长城宽带、网段表说腾讯云"——这类是真实转售关系，按优先级改对了。

### GeoCN 区县补全（按边界切分）

[ljxi/GeoCN](https://github.com/ljxi/GeoCN) 是 MMDB 格式的离线库，只有两个字段：
`division_code`（国标行政区划编码）和 `isp`（7 种粗标签）。

**它的价值只在区县**——这是唯一能碰到区县级的源。数据实测：

| 指标 | 数值 |
|---|---|
| IPv4 网段数 | 111.3 万（索引合并后 85.8 万条） |
| 覆盖中国 IP | 3.45 亿（占中国空间 86%） |
| 其中区县级编码 | 1.87 亿 IP（54%） |
| 与我们省份一致率 | 96.4% |
| 与我们城市一致率 | 81.0% |

**关键设计：按 GeoCN 的网段边界切分段，而不是整段塞一个区县。**

一个 /16 平均跨 200 多个 GeoCN 网段。早先的实现在段中点查一次就把结论套满整段，
结果 19,078 段（1.03 亿 IP）拿一个区县的标签套了几十万个 IP——那是假数据。

现在 `geocn.Spans()` 把段按 GeoCN 边界切开，每片填自己那片的区县，纯本地计算、
零网络请求。切完再合并"区县相同"的相邻片，段数只增 3 万而不是 100 万。

**合并规则（只补不覆盖）：**

| 情况 | 动作 |
|---|---|
| 我们区县为空 + 省市与 GeoCN 一致 + 非云段 | 填区县（实测 57.5 万片） |
| 我们省市为空 | 顺手补上（保证与区县自洽） |
| 我们区县已存在 | **不动**（qqwry 的区县无证据说更差） |
| GeoCN 的省市与我们不一致 | **该片放弃**（24 万片），否则产出"吉林省长春市\|玄武区"这种错位 |
| GeoCN 的区县跨度 > 131072 IP | **放弃**（5 片）——见下 |
| 云/CDN/IDC 段 | 跳过（4,785 段），GeoCN 也是扫 API 来的，云段给的是 PoP 城市 |

**为什么要有跨度上限**：GeoCN 库里确实存在 `59.192.0.0/10（419 万 IP）→ 110102 西城区`
这种条目，那是它扫描采样过稀造成的。一个区不可能有 419 万个 IP。超过 13 万 IP 的
区县一律不采信（省市保留）。

效果：**区县覆盖率 4.67% → 40.65%**（1885 万 → 1.64 亿 IP）。

已知短板：

- 更新"随缘"（README 原话），当前库是 2026-04 的，可能滞后
- ISP 只有 7 种粗标签（电信/联通/移动/鹏博士/教育网/广电），比网段表粗，所以不用它判 ISP
- 不覆盖港澳台（只含中国大陆）
- 云厂商段同样会给出 PoP 城市，与百度一个毛病

### refine：边界递归收敛

```bash
go run ./cmd/ip2region refine -threshold 4096 -min-span 256 -qps 60
go run ./cmd/ip2region refine -only-empty            # 只处理省市缺失的段
go run ./cmd/ip2region refine -limit 100 -dry-run    # 试跑
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `-threshold` | 4096 | 只处理长度 ≥ 该值的段（4096 = /20） |
| `-min-span` | 256 | 收敛到这个 IP 数就停（256 = /24），再细百度也分不出来 |
| `-depth` | 20 | 递归深度安全阀，正常不会触发 |
| `-seg-probes` | 128 | 单个段最多探测几次，防止归属混乱的段烧请求 |
| `-qps` / `-workers` | 20 / 4 | 限速与并发 |
| `-report` | `build/refine-report.tsv` | 细分明细输出 |
| `-only-empty` | false | 只处理省市缺失的段 |

**核心：只往"结论不同"的那半边收敛，一直收敛到 /24。**

```
探 起点 lo / 中点 mid / 终点 hi
  ├─ 三点一致       → 整段一个结论，结束
  ├─ lo=mid ≠ hi    → 左半已确认，出一片，只收敛右半 [mid, hi]
  ├─ lo ≠ mid=hi    → 右半已确认，只收敛左半 [lo, mid-1]
  └─ 三点都不同     → 两半都要收敛
```

这比"三点不一致就在 mid 处二分、两半都递归"省一半以上请求：一半区间已经确认了，
没必要再探。

**成本对比。** 把一个边界定位到 256 IP 的精度：

| 方法 | 请求数（/16 段） | 请求数（/10 段） | 边界精度 |
|---|---|---|---|
| 均匀采样（enrich 的做法） | 256 个点 | 16384 个点 | = 采样间隔 |
| 递归收敛 | **8 轮 ≈ 10 次** | **14 轮 ≈ 16 次** | ≤ 256 IP |

实测一个 /16 段（前 3/4 北京、后 1/4 大连），**10 次请求把边界定位到 1 个 IP 误差**
（`internal/refine/refine_test.go: TestRefineConvergesToBoundary`）。

真实数据上的效果：

```
1.83.229.113-1.84.0.255  原标"陕西省西安市"
  → 1.83.229.113-1.84.0.33   陕西省西安市
  → 1.84.0.34-1.84.0.255     陕西省汉中市     ← 边界精确到单 IP

112.84.127.0-112.84.143.62  原标"江苏省徐州市"
  → 112.84.127.0-112.84.128.2   江苏省徐州市
  → 112.84.128.3-112.84.143.62  江苏省扬州市
```

细分产生的子段用"删+插"单事务替换原段，保持连续、不重不漏。收敛会切出一些
同归属的碎片，`merge` 的 Compact 会合并回去，不影响最终产物。

实测：15,141 个大段里 14,868 个三点一致直接确认，273 个收敛切开、新增 1,679 片，
真实请求仅 1,143 次（其余 4.9 万次命中 enrich 留下的 /24 缓存）。

**为什么有些大段收敛不动？** 例如 `59.192.0.0/10`（419 万 IP，中国移动）整段仍是
"北京市"。查过缓存：这段的 83 个探测点，百度全部返回 `北京市北京市` ——
整个区间没有变化点，递归收敛无从下手。这是数据源的天花板，不是算法问题。

### merge：叠加自定义覆盖### merge：叠加自定义覆盖

```bash
go run ./cmd/ip2region merge
go run ./cmd/ip2region merge -custom data/custom -out build/ip2region.txt
```

读 `data/custom/` 下所有 `*.txt`，按 [自定义覆盖规则](#自定义覆盖规则) 解析，
用 `Apply` 覆盖到段表上，然后 `Compact` 合并相邻同值段，最后 `Validate` 校验。

任何一步出错（字段过多、IP 非法、起止倒置、合并后出现缝隙）都会**直接报错停下**，
不会产出坏库。

### build：生成 xdb

```bash
# 推荐：吃 merge 的产物（含自定义覆盖）
go run ./cmd/ip2region build -from txt -src build/ip2region.txt -out build/ip2region.xdb

# 直接从 SQLite 段表构建（跳过自定义覆盖，调试用）
go run ./cmd/ip2region build -from db

# 换索引策略
go run ./cmd/ip2region build -from txt -policy btree
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `-from` | `db` | `db`（导出段表再构建）或 `txt`（用 merge 产物） |
| `-src` | `build/ip2region.txt` | `-from txt` 时为输入，`-from db` 时为导出路径 |
| `-out` | `build/ip2region.xdb` | 输出 xdb |
| `-policy` | `vector` | 索引策略 `vector` 或 `btree` |

实测：141 万段 → 22.1MB xdb，9 秒。

### verify：校验与对拍

```bash
go run ./cmd/ip2region verify
go run ./cmd/ip2region verify -n 100000                    # 抽样 10 万个
go run ./cmd/ip2region verify -golden ""                   # 跳过对拍
go run ./cmd/ip2region verify -golden other.xdb -n 50000   # 换基准库
```

输出三部分：

1. **完整性**：段数、覆盖 IP 总数（应等于 4294967296）、归属块数
2. **抽样**：几个固定 IP 的查询结果
3. **对拍**：随机 N 个 IP 同时查当前库和 golden 库，比较国家与省级，并把分歧分成三类

```
国家一致率          : 99.03%
省级一致率          : 95.19%（按双方均判为中国的 4724 个 IP）
  其中 旧库为空/新库补上: 165（3.49%，属改进）
  其中 双方都有值但不同  : 62（1.31%，需人工抽查）
```

第三类（真冲突）才是需要关注的部分，前两类分别是一致和"我们补得更多"。

### stats：质量报表

末尾还会做一次**省市自洽检查**：用行政区划编码表里的从属关系，找出"市不属于该省"的段。

```
省市自洽检查        : 通过（无跨省组合）
```

这条检查与数据源无关，任何来源（qqwry 原始数据、百度、GeoCN、自定义覆盖）引入的错位都能发现。
当前库只剩 1 段：`27.222.69.0 山东省|德阳市`（qqwry 原始数据错误，德阳在四川）。

```bash
go run ./cmd/ip2region stats
go run ./cmd/ip2region stats -db other.db
```

按 **IP 数加权**（不是段数加权——一个段可能覆盖 400 万个 IP，也可能只有 1 个）输出字段覆盖率：

```
中国                : 199504 段 / 4.03 亿 IP（占全域 9.4%）
字段覆盖率（IP 加权）:
  省     99.08%  █████████████████████████████░  (399718229 IP)
  市     91.54%  ███████████████████████████░░░  (369281990 IP)
  区县     4.67%  █░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  (18850674 IP)
  乡镇    15.01%  ████░░░░░░░░░░░░░░░░░░░░░░░░░░  (60540714 IP)
  ISP   89.56%  ██████████████████████████░░░░  (361287063 IP)
```

还会输出段来源分布、省份 Top10、最大的 10 个中国段（用来发现该细分而没细分的残留）。

### look：查询调试

```bash
# 查生成的 xdb
go run ./cmd/ip2region look --xdb build/ip2region.xdb 183.3.149.185 8.8.8.8

# 查 SQLite 段表（能看出未合并的原始段和来源标记）
go run ./cmd/ip2region look 183.3.149.185
```

```
183.3.149.185    中国 | 广东省 | 广州市 | 番禺区 | 钟村 | 中国电信
8.8.8.8          美国 | 加利福尼亚州 | 圣克拉拉 | 山景城 |  | 谷歌公司DNS服务器
```

---

## 发布更新（同步到 xdb 仓库）

xdb 产物放在独立仓库 `ip2region_update`（`ip2region_update/` 目录，独立的 git 仓库，
已在本仓库 `.gitignore` 里排除），源码仓库不存 22MB 的二进制。

### 一次完整更新

```bash
# ① 在源码仓库生成
go run ./cmd/ip2region fetch            # 打印数据版本号，例如 20261028
go run ./cmd/ip2region parse
go run ./cmd/ip2region enrich -qps 60
go run ./cmd/ip2region refine -qps 60
go run ./cmd/ip2region merge
go run ./cmd/ip2region build -from txt
go run ./cmd/ip2region verify -n 50000  # 必须通过（覆盖 IP = 4294967296）
go run ./cmd/ip2region stats            # 记下覆盖率，要写进发布 README

# ② 拷进发布仓库
cd ip2region_update
rm -f ip2region_*.xdb                                # 只保留最新一份
cp ../build/ip2region.xdb ./ip2region_<数据版本>.xdb   # 用 fetch 打印的版本号，不是当天日期
# 更新 README.md 里的版本号 / 文件大小 / 覆盖率
git add -A && git commit -m "更新 xdb: <数据版本>" && git push origin main
```

### 两条硬要求

1. **文件名用 qqwry 数据版本，不是打包日期**。`fetch` 输出的 `远端最新版本: 20261028`
   就是版本号。下游靠文件名判断新旧。
2. **`verify` 不通过不许发**。`覆盖 IP 总数` 必须等于 `4294967296`（2^32），
   说明全覆盖无缝隙；不等于就是库坏了。

### 下游怎么升级

替换 xdb 文件后重启进程即可。用 `NewWithBuffer` 把文件读进内存的方式，必须重启才会重新加载。

---

## 自定义覆盖规则

放在 `data/custom/` 下，所有 `*.txt` 都会被 merge 读取，**优先级最高**：压过 qqwry 和百度的一切结论。

### 格式

每行一条，`#` 开头为注释，空行忽略。支持两种地址写法：

```
# 起止 IP 写法
183.3.149.185|183.3.149.190|中国|广东省|广州市|番禺区|钟村|中国电信

# CIDR 写法
119.29.64.0/18|中国|广东省|广州市|||腾讯云
```

区域串固定 6 段：`国|省|市|区|镇|ISP`：

```
字段不足 6 段 → 自动右补空
字段超过 6 段 → 直接报错（防止字段里混入竖线导致静默错位）
```

### 规则

- **同一 IP 被多条命中时，靠后的行胜出**（按 start_ip 排序后应用），想压别人就写在文件末尾
- 覆盖段会把原有段切开，覆盖范围之外不受影响
- merge 会自动校验合并结果的全覆盖与连续性

### 现有文件

| 文件 | 内容 |
|---|---|
| `manual.txt` | 手工维护的规则（番禺钟村机房/出口段等 36 条） |

### 常见坑

- **别把大段整段标成某个区**。像 `183.13.128.0|183.13.168.255`（4 万 IP）直接标"广州市番禺区"，
  会把同段里其他区的地盘一起改掉。尽量缩到真实范围。
- **省名用全称**。`广东省` 而不是 `广东`，混用会让按省统计对不上。
- **字段缺失要留空位**。`...|兴国县|中国移动` 只有 7 段，会被解析成"镇=中国移动、ISP=空"，
  正确写法是 `...|兴国县||中国移动`。

---

## 缓存与断点续跑

所有外部查询结果都落在 SQLite 的 `ip_cache` 表（主键 `ip` + `provider`）：

- **缓存即断点**：进程被杀、断网、Ctrl-C 都没关系，重跑时已查过的 IP 直接命中缓存，不发请求
- **无数据也缓存**：百度回"没有数据"的 IP 也会写一行空记录，避免每次重跑都重新烧一遍
- **网段复用**：enrich 按 `/24` 去重，同网段多个段共用一个查询结果
- **refine 大量命中**：它的起点/中点探测点多数已被 enrich 查过，第二次跑通常 0 请求

实测：整条流水线重跑一次（parse + enrich + refine + merge + build）不到 1 分钟，0 次外部请求。

缓存在 `data/ip2region.db` 里，想彻底重来就删掉这个文件重新 `parse`。

---

## 数据质量与已知限制

### 实测指标（中国 IP 加权）

| 字段 | 纯 qqwry | 跑完 enrich | 来源 |
|---|---|---|---|
| 省 | 95.84% | **99.08%** | qqwry + 百度补空 |
| 市 | 81.18% | **91.54%** | qqwry + 百度补空 |
| 区县 | 4.67% | **40.65%** | qqwry + GeoCN 按边界切分补空 |
| 乡镇 | 15.01% | 13.95% | 只有 qqwry 有（GeoCN 没有镇级） |
| ISP | 85.00% | **90.48%** | qqwry + 网段表 + 百度补空 |

### 三个硬限制，先说清楚

1. **乡镇覆盖率低（14.55%），这是数据源的天花板。**
   区县靠 GeoCN 提到了 40.65%，但**镇级没有任何源提供**——qqwry 的 area 字段偶尔有，
   百度、GeoCN、运营商网段表都没有。要镇级精度只能靠自定义覆盖。
   剩下的 59% 区县空缺，是因为 GeoCN 本身只到市级（10.4 万片），
   或省市与我们不一致而主动放弃（24 万片）。

2. **百度不能用来覆盖已有值。**
   交叉验证（ipwho.is / ipapi.is / ip-api 三个独立源）显示，百度与 qqwry 在省市上
   是同一量级的噪声源，覆盖等于抛硬币。所以默认只补空。

3. **境外段没有增强。**
   百度对境外只返回国家（`8.8.8.8 → "美国"`），比 qqwry 的
   `美国|加利福尼亚州|圣克拉拉|山景城| |谷歌公司DNS服务器` 差得多，查了等于主动降级。

### 数据来源

| 数据 | 来源 | 更新频率 |
|---|---|---|
| 基础归属 | [metowolf/qqwry.dat](https://github.com/metowolf/qqwry.dat) | 每周 |
| 运营商归属 | [ispip.m27.tech](https://ispip.m27.tech/)（APNIC + whois） | 每日 |
| 区县补全 | [ljxi/GeoCN](https://github.com/ljxi/GeoCN)（扫描公开 API + 国标编码） | 随缘 |
| 省市/ISP 补空 | 百度 opendata IP 接口（`resource_id=6006`） | 实时 |
| 人工规则 | `data/custom/*.txt` | 你说了算 |

百度这个接口是未公开的，没有文档、没有 SLA、没有配额承诺。
代码里对 429/403 的处理是**立即停止并保留缓存**，不会硬刚。真被封了也不亏——
已有的结果都在库里，换数据源只需要新增一个 `internal/provider` 的实现。

---

## 查询侧接入

xdb 的 region 是 **6 段字符串**，官方 SDK 的 `Region` 结构按 5 段解析，**不能用它的便捷方法**。

### Go

```go
import (
    "fmt"
    "strings"

    "github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

func main() {
    searcher, err := xdb.NewWithFileOnly("build/ip2region.xdb")
    if err != nil {
        panic(err)
    }
    defer searcher.Close()

    region, err := searcher.SearchByStr("183.3.149.185")
    if err != nil {
        panic(err)
    }

    p := strings.SplitN(region, "|", 6)
    // p[0] 国家  p[1] 省  p[2] 市  p[3] 区  p[4] 镇  p[5] ISP
    fmt.Printf("%s %s %s %s %s %s\n", p[0], p[1], p[2], p[3], p[4], p[5])
}
```

> ⚠️ 用官方 binding 前先确认 `SearchByStr` 没被改过。本仓库之前踩过：
> 某个版本的 `SearchByStr` 加了一个 replacer 把 `|`、`0`、`中国` 全删了，
> 连 IP 里的 0 都会被删，查出来的结果全是残缺的。
> 本仓库 `internal/xdbmaker/searcher.go` 里有一份未改动的官方实现，可直接引用。

### 高并发场景

`NewWithFileOnly` 每次查询要 read 系统调用。高并发时把整个文件读进内存：

```go
cBuff, err := xdb.LoadContentFromFile("build/ip2region.xdb")
searcher, err := xdb.NewWithBuffer(cBuff)
```

内存占用就是文件大小（约 22MB）。注意 `Searcher` 不是并发安全的，每个 goroutine 一个实例。

---

## 常见问题

**Q：`parse` 报"非法区间"或段数异常少？**
A：多半是 `qqwry.dat` 下坏了（下到了错误页）。用 `fetch` 重新下，它会校验体积和 md5。

**Q：`enrich` 卡在某个进度不动？**
A：正常，进度每 2000 条才打一行。真卡住看是不是被限流（日志会打"被百度限流/封禁"）。
缓存不会丢，降 `-qps` 重跑即可。

**Q：`enrich` 跑完发现结果不对，想重来？**
A：删掉 `data/ip2region.db` 重新 `parse`。只想丢掉外部查询结果的话，清空 `ip_cache` 表即可。

**Q：能不能只用 qqwry，不调百度？**
A：可以。跳过 `enrich` 和 `refine`，直接 `merge` + `build`。代价是市覆盖率 81% 而不是 91%。

**Q：能不能换高德/ip138 数据源？**
A：`internal/provider` 是接口化的。实现 `Provider`（`Name()` + `Lookup()`），
在 `cmd/ip2region/enrich.go` 里换一行构造，enrich/refine 主流程不用改。

**Q：xdb 多大？能压吗？**
A：22MB，主要是 region 字符串。每个段都要存完整区域串，压不动多少。
想小一点只能用 `build -policy btree`（索引略小，查询稍慢）。

**Q：`verify` 的"真冲突 1.31%"要不要处理？**
A：可以人工抽查。冲突样例在 `build/enrich-conflicts.tsv`，觉得该改的写进 `data/custom/`。

---

## 目录结构

```
cmd/ip2region/          唯一入口，子命令分发
  fetch.go              下载 qqwry.dat
  parse.go              解析落库
  enrich.go             百度补空
  refine.go             边界递归收敛
  merge.go              叠加自定义覆盖
  build.go              生成 xdb
  verify.go             校验与对拍
  stats.go              质量报表
  look.go               查询调试
  main.go               命令注册

internal/geo/           GeoInfo(6段) / Segment / 区间运算（Normalize/Apply/Compact/Validate/Find）
internal/qqwry/         qqwry.dat 解析（GB18030 解码、0x01/0x02 跳转）
internal/regions/       行政区划切分、ISP 归一（含港澳台运营商）、云厂商识别、百度 location 解析
internal/store/         SQLite：段表 / ip_cache / meta
internal/provider/      外部数据源接口 + 百度实现
internal/ispip/         运营商网段表解析（APNIC 分配归属）
internal/geocn/         GeoCN MMDB 读取 + 行政区划编码翻译
internal/enrich/        /24 去重、限流、缓存复用、冲突裁定
internal/refine/        大段二分收敛细分
internal/xdbmaker/      官方 xdb maker + searcher（Apache 2.0，收编进仓库自包含）

data/
  qqwry.dat             原始库（27MB，不进 git）
  ip2region.db          SQLite（段表 + 缓存，不进 git）
  custom/*.txt          自定义覆盖规则（进 git）

build/                  产物目录（不进 git）
  ip2region.txt         merge 输出的中间 txt
  ip2region.xdb         最终库
  enrich-conflicts.tsv  ISP 冲突样例
  refine-report.tsv     细分明细

testdata/golden/        旧版 xdb，作回归对拍基准（不进 git）
_legacy/                旧实现归档，不参与编译（不进 git）
```

---

## 开发与测试

```bash
go build ./...
go vet ./...
go test ./...
```

测试覆盖的是**规则**而不是数据：

| 包 | 测试内容 |
|---|---|
| `internal/geo` | 区间规范化/覆盖/合并/校验，跨多段的覆盖裁剪、缝隙填充 |
| `internal/qqwry` | 合成 dat 的 4 种记录形态（含 GBK 编码的测试数据） |
| `internal/regions` | 双格式切分（dash 预分隔 / 原版连写）、百度 location 解析 16 个用例 |
| `internal/provider` | httptest 模拟接口：正常/空数据/429/5xx 的分类 |
| `internal/enrich` | 补空、覆盖、云段保护、低置信度保护、ISP 冲突 8 个子用例 |
| `internal/refine` | 大段切分、三点一致不细分、云段不被覆盖、区镇保留 |
| `internal/ispip` | CIDR 解析、云厂商优先于运营商、兜底桶不当 ISP、/14 边界 |
| `internal/geocn` | 编码表解析、省级编码不带区县、真实库抽样冒烟 |

改 `decide` / `applyTo` 里的规则前，先看对应测试——那些用例记录了踩过的坑。

---

## 致谢

- [ip2region](https://github.com/lionsoul2014/ip2region)（Apache 2.0）：xdb 格式与 maker/searcher
- [metowolf/qqwry.dat](https://github.com/metowolf/qqwry.dat)：持续维护的纯真数据
