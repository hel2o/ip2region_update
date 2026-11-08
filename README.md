# ip2region 归属库构建工具

把纯真 `qqwry.dat` 转成 [ip2region](https://github.com/lionsoul2014/ip2region) 的 `.xdb`，
中间用百度 IP 接口补齐/纠正省/市/ISP、用 GeoCN 补区县、用 ispip 港澳台表修归属，
再叠加你自己的手工覆盖规则。

产出是一个约 60MB 的单文件库（300 万段），覆盖全部 42.9 亿个 IPv4 地址，无缝隙、无重叠。

```
$ ip2region look --xdb build/ip2region.xdb 183.3.149.185
183.3.149.185    中国 | 广东省 | 广州市 | 番禺区 | 钟村 | 中国电信
```

> **注意：区域串是 6 段，不是官方默认的 5 段。**
> 官方 SDK 的 `Region` 结构按 5 段解析，直接套用会字段错位。查询侧必须自己按 6 段切（见 [查询侧接入](#查询侧接入)）。

---

## 目录

- [快速开始](#快速开始)
- [一键重建](#一键重建)
- [环境要求](#环境要求)
- [命令总览](#命令总览)
- [流水线详解](#流水线详解)
  - [fetch：下载最新数据](#fetch下载最新数据)
  - [parse：解析成段表](#parse解析成段表)
  - [enrich：百度判省市 + 港澳台纠正](#enrich百度补空)
  - [refine：边界递归收敛](#refine边界递归收敛)
  - [merge：叠加自定义覆盖](#merge叠加自定义覆盖)
  - [build：生成-xdb](#build生成-xdb)
  - [verify：校验与对拍](#verify校验与对拍)
  - [stats：质量报表](#stats质量报表)
  - [look：查询调试](#look查询调试)
  - [serve：查询页面（核验数据）](#serve查询页面核验数据)
- [发布更新](#发布更新github-release--同步上线)
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

# 2. 重新解析 + 增强（有缓存，约 1 分钟）
go run ./cmd/ip2region parse
go run ./cmd/ip2region enrich

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
go run ./cmd/ip2region enrich      # 百度判省市 + 港澳台纠正（默认 100 QPS 自适应，45~50 分钟）
go run ./cmd/ip2region refine -qps 60 # 边界递归收敛到 /24
go run ./cmd/ip2region merge          # 叠加 data/custom/*.txt
go run ./cmd/ip2region build -from txt
go run ./cmd/ip2region verify -n 50000
```

---

## 一键重建

日常出库只需要一条命令，内部就是按顺序跑完 9 步（fetch → parse → legacy → enrich →
refine → merge → build → verify → stats），任一步失败立即停下并报出是第几步。

Windows（双击或命令行）：

```bat
rebuild.bat
```

Linux / macOS / Git Bash：

```bash
./rebuild.sh
```

两个脚本都只是「编译 + 调 `all`」，所以要定制参数就直接往后传：

```bash
./rebuild.sh -skip-fetch                  # 不下载，用本地已有数据
./rebuild.sh -skip-enrich -skip-refine    # 只重排数据，完全不出网（30 秒）
./rebuild.sh -only-custom                 # 只改了 data/custom/*.txt（30 秒，不碰段表）
./rebuild.sh -qps 120 -workers 16         # 加大并发
./rebuild.sh -out build/test.xdb          # 输出到别处，先小规模验证
```

等价于直接调二进制：

```bash
go run ./cmd/ip2region all                # 或 build/ip2region all
```

`all` 的关键行为：

| 行为 | 说明 |
|---|---|
| 下载失败不中断 | 拿不到新 qqwry.dat 就用本地已有的，只是数据不新（日志会打 `!! 下载失败`） |
| 旧库补丁可缺省 | `build/compare/old.xdb` 不存在就跳过 legacy，不报错 |
| 重跑很便宜 | enrich/refine 的请求缓存按 /24 存在 SQLite 的 `ip_cache` 表，数据没变时第二次跑几乎零请求 |
| 缓存即断点 | 中途 Ctrl+C 后重跑，已请求过的网段直接命中缓存 |
| 结尾给摘要 | 输出文件大小、md5、总耗时，md5 可直接填进发布说明 |

常用开关：`-skip-fetch`、`-skip-legacy`、`-skip-enrich`、`-skip-refine`、`-verify-n 0`
（跳过对拍）、`-only-custom`（只跑 merge + build）、`-db` / `-txt` / `-out`（换路径）、
`-qps` / `-workers`（限速与并发）、`-probe-step`（默认 256）/ `-probe-jitter`（默认 0.6）/ `-mask`（默认 /24）。`all -h` 看全部。

> 注意：`all` 里的 `parse` 会重建段表（等于清空后重灌），自定义覆盖在 `merge` 阶段
> 重新叠加，所以不用担心丢规则；但**手工改过 SQLite 段表**的话会被覆盖，改完请另行备份。

#### 只改了 `data/custom/*.txt`（不动数据源）

一条命令：

```bat
rebuild.bat -only-custom        :: 或 ./rebuild.sh -only-custom
```

它等于只跑 `merge` + `build` 两步（约 30 秒，不联网）：叠加自定义覆盖 → 重建 xdb。
**不会**跑 `parse`——`parse` 会重灌段表，而百度/GeoCN 的增强结果只存在段表里，
跳过 enrich 就等于全丢。

要推到 99.26 再加一条 `sync.bat`。手工两步等价写法：

```bash
./build/ip2region.exe merge            # 叠加自定义覆盖 → build/ip2region.txt（约 2 秒）
./build/ip2region.exe build -from txt  # 重建 xdb（约 19 秒）
./sync.sh                              # 推到 99.26（可选）
```

`-only-custom` 模式下 `verify` / `stats` 会自动跳过：它们读的是 SQLite 段表，段表没动过，
跑一遍只会重复报旧数字。想确认改动生效，用 `look -xdb build/ip2region.xdb <IP>` 查
自定义范围内的地址即可（不带动 `-xdb` 时查的是段表，看不到自定义覆盖的结果）。

> xdb 头部第 5~8 字节是**生成时间戳**，所以同样的数据每次 build 出来的 md5 都不同
> （字节级比对只差这 1 个字节），不要用 md5 判断数据是否变化。


出库后要推到各项目用，接着跑 `./sync.sh`（见[同步到 3 台机器的 7 个目录并重载](#同步到-3-台机器的-7-个目录并重载)）。

---

## 环境要求

- Go 1.26（见 `go.mod`）。下限被 `modernc.org/sqlite` 和 `golang.org/x/text` 拉高了，
  想降到更低版本需要同时降这两个依赖
- 无需 CGO：SQLite 用纯 Go 驱动 `modernc.org/sqlite`，Windows 上直接编译，不用装 gcc
- 能访问 `raw.githubusercontent.com`、`github.com`（下载 qqwry 与 GeoCN）、
  `ispip.m27.tech`（下载运营商/港澳台网段表）、`qifu.baidu.com` + `opendata.baidu.com`（查询源）
- 磁盘约 200MB（qqwry.dat 27MB + SQLite 300MB 左右，缓存表是主要占用）

```bash
go build ./cmd/ip2region    # 也可以编译成单二进制
```

---

## 命令总览

| 命令 | 作用 | 产物 | 耗时（实测） |
|---|---|---|---|
| `all` | 一键跑完整条流水线（fetch→parse→legacy→enrich→refine→merge→build→verify） | `build/ip2region.xdb` | 首次 45~60 分钟，缓存后 1 分钟 |
| `fetch` | 下载 `qqwry.dat` + 网段表（含港澳台三页）+ GeoCN | `data/qqwry.dat`、`data/ispip/`、`data/geocn/` | 40 秒~1 分钟 |
| `parse` | 解析 dat，拆省市区镇，落 SQLite | `data/ip2region.db` | 10 秒 |
| `legacy` | 用旧版 xdb 补境外缺失的省市 | 改写段表 | 30 秒 |
| `enrich` | 调百度判省市/ISP（含港澳台地域表纠正） | 改写段表 + `ip_cache` | 首次 45~50 分钟，缓存后约 1 分钟 |
| `refine` | 大段边界递归收敛到 /24 | 段表拆分 | 首次约 20 秒（多为缓存命中） |
| `merge` | 叠加 `data/custom/*.txt` + 注入版本探针 | `build/ip2region.txt` | 2 秒 |
| `build` | 调官方 maker 生成 xdb | `build/ip2region.xdb` | 20 秒 |
| `verify` | 校验全覆盖 + 与旧库对拍 | 控制台报告 | 5 秒 |
| `stats` | 字段覆盖率报表 | 控制台报告 | 1 秒 |
| `look` | 查询单个 IP | 控制台输出 | 即时 |
| `serve` | 启动本地查询页面（核验数据） | Web 页面 | 1 秒 |

每个命令都支持 `-h` 查看完整参数。

---

## 流水线详解

```
data/qqwry.dat
   │  parse     GBK→UTF8，拆省/市/区/镇/ISP，规范化成全覆盖段表
   ▼
data/ip2region.db (SQLite)     ← 段表 + 请求缓存，缓存即断点
   │  enrich    按 GeoCN 边界切片 → 片内 step=256 布点（可抖动）→ 百度判省/市；
   │            港澳台段按 ispip 地域表强制省份、ISP 以百度为准；
   │            最后用运营商网段表补空 ISP
   │  refine    大段边界递归收敛，只往"结论不同"的半边收敛到 /24
   ▼
build/ip2region.txt
   │  merge     叠加 data/custom/*.txt（优先级最高），注入版本探针
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

一次做三件事：

1. 从 `metowolf/qqwry.dat` 的 release 拉最新 dat，先下到临时文件，校验体积（≥10MB）和 md5
   后才原子替换，内容没变就不动
2. 从 `ispip.m27.tech` 下载 21 个运营商/云厂商网段表 + 3 个港澳台地域表到 `data/ispip/`
   （约 768KB）；港澳台三页不做 ISP，enrich 用来定归属地，见下文
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

实测：`151 万原始记录 → 141 万段`（enrich 按边界切分后 366 万段），其中中国 4.06 亿 IP。

### legacy：把旧版 xdb 的境外省市搬回来

```bash
go run ./cmd/ip2region legacy -xdb /path/to/old.xdb            # 只补境外（默认）
go run ./cmd/ip2region legacy -xdb old.xdb -dry-run            # 只统计
go run ./cmd/ip2region legacy -xdb old.xdb -include-cn         # 连国内一起（不建议）
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `-db` | `data/ip2region.db` | 当前库 |
| `-xdb` | `build/compare/old.xdb` | 旧版 xdb 路径 |
| `-include-cn` | false | 也处理中国大陆段 |
| `-dry-run` | false | 只统计不写库 |

**为什么需要它**：新版 qqwry（2026-10）把境外记录大幅简化了。同一个 IP：

```
旧库: 日本|Shimane|松江市|||Enecom,Inc.
新库: 日本||||||Energia通讯          ← 省市没了
```

原始 dat 里确实只剩 `国家="日本" 地区="Energia通讯"`，省市不是我们丢的，是数据源没了。
旧库那批信息只能从旧库搬。

规则刻意保守：**只补空字段，绝不覆盖已有值**；默认只处理境外段。补完会按旧库的边界
切分原段，让每片带自己那片的归属。

实测：处理 26.99 万段（其中 7.5 万段被切开），补省 89.5 万处、补市 93.5 万处，
涉及 21.5 亿 IP。境外省覆盖率 20.6% → **51.7%**，旧库有而我们没有的**全部归零**。

### enrich：外部源判断省市 + 补 ISP

查询源默认是 **qifu**（`https://qifu.baidu.com/api/v1/ip-portrait/brief-info`），
它无需 AK/Cookie，只要浏览器 UA + qifu 域 Referer；返回结构化字段
（country/province/city/isp/scene/risk_score），比 opendata 的 `"广东省广州市 电信"`
一个串更好解析。某个 IP 查不到或 qifu 异常时，自动回退旧的 opendata 接口。

两个源共用 `ip_cache`，换源不会导致整库重查；要用新源把旧结果**全部重查一遍**时加
`-refresh`（忽略历史 provider 的缓存，但仍复用本次已写入的，所以能断点续跑）。

> qifu 实测（2026-11，约 6.3 万次）：无配额、无 429、不封 IP，恒速 40 QPS 连跑 5 分钟
> 零错误；持续超 60 QPS 会出现尾部延迟爆炸 + 偶发 25s 超时，建议控在 20~45 QPS。
> 软限流可能返回 HTTP 200 + 空 body，客户端已按失败处理。

**港澳台归属优先级：自定义 > ispip 港澳台表 > qifu/百度 > qqwry。**

`data/ispip` 里的 `hongkong.html` / `macau.html` / `taiwan.html` 是该地区的全部 CIDR
（每天更新），enrich 默认加载它们做"归属地第二优先级"：探测点落在这三个表里，
省份就强制为 `中国|香港特别行政区` / `中国|澳门特别行政区` / `中国|台湾省`，
即使 qifu/百度/qqwry 把它判成别的国家（真实案例：72.255.243.92 在香港表的
`72.255.224.0/19` 里，但被 qqwry/qifu 判成美国加州）。纠正时挂错的市/区县/镇
一并清掉。与表有交集但归属不符的段会自动进入全量 enrich；一次性集中修正用
`enrich -only-hmt`。

**ISP 在港澳台例外地以百度为准**：大陆是"百度补空/基础运营商之间才覆盖 → 网段表补空 → 纯真兜底"，
非基础运营商的值不被覆盖（百度 ISP 常混公司名）；港澳台反过来——百度给的是干净运营商名
（电讯盈科/中华电信/澳门电讯），而纯真常留陈年公司名甚至境外 ISP
（Charter Communications、iBAHN网络），所以只要百度有 ISP 就直接用它。

**探测布线：GeoCN 边界优先，片内再按 IP 数等分。**

按 IP 数机械等分的问题在于切点跟真实归属无关。GeoCN 的网段边界来自真实分配记录，
在它上面布点更可能落在真实的归属变化处。实测 `111.55.0.0/16`：

```
GeoCN 切出 1678 片（西宁 / 广州 / 重庆 / 南宁 混杂，每片 256 IP）
机械等分（step 2048）只布 32 个点，绝大多数 /24 根本探不到
```

所以顺序是：

```
段 → 按 GeoCN 网段边界切 → 合并 < 2048 IP 的碎片 → 片内每 256 IP 布一点 → 段内封顶 1024 点
```

切出的每一格独立探测、独立决策，结论不同就把原段切开。GeoCN 未收录的区间保持原样。

实测港澳台定向刷新（`-only-hmt`）：17.2 万段 → 20.2 万片 → **19.7 万个去重探测点**，
缓存命中 19.46 万（98.7%），1 分 5 秒跑完，真实请求只剩 2,617 次。
全量重建（`-probe-step 256`）约 366 万段、340 万级探测点，45~50 分钟。

> GeoCN 也有它看不出来的大段：`59.192.0.0/10`（419 万 IP）在 GeoCN 里就是一片
> "北京市西城区"，百度 83 个探测点也全回北京。这段无解，任何数据源都认为它整段在北京。

```bash
# 试跑，只发 200 次请求，不写段表
go run ./cmd/ip2region enrich -limit 200 -dry-run

# 全量（默认值即可，自适应限速会自己爬）
go run ./cmd/ip2region enrich

# 只处理省市为空的段
go run ./cmd/ip2region enrich -only-empty
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `-qps` | 100 | 请求速率上限（自适应模式下是天花板） |
| `-workers` | 16 | 并发数 |
| `-adaptive` | **true** | 拥塞控制式动态限速：健康时爬升，延迟劣化/出错自动退避；传 `-adaptive=false` 退回固定限速 |
| `-primary` | `qifu` | 主查询源：`qifu`（失败自动回退 opendata）/ `opendata` |
| `-refresh` | false | 忽略换源前的历史缓存，全部重查（本次写入的缓存仍可断点续跑） |
| `-limit` | 0 | 最多请求数，0 表示不限（试跑用） |
| `-mask` | `/24` | 网段去重粒度，可选 `/24` `/16` `none` |
| `-dry-run` | false | 只统计不写段表（**仍会写缓存**，避免重复烧请求） |
| `-override` | **true** | 用查询源覆盖已有省市；传 `-override=false` 退回只补空 |
| `-ispip` | `data/ispip` | 运营商网段表目录，空串表示不用（建表前先跑 `fetch`） |
| `-geocn` | `data/geocn/GeoCN.mmdb` | GeoCN 离线库，空串表示不补区县 |
| `-amap-key` | `$AMAP_KEY` | 高德 Web 服务 Key，只用于补百度缺失的省市（留空不启用） |
| `-probe-step` | **256** | 片内每多少 IP 布一个探测点（256 = 每个 /24 一个点，是 /24 去重的有效上限；0 = 每片只探中点） |
| `-probe-cap` | 1024 | 每个段最多几个探测点（0 = 不封顶） |
| `-probe-jitter` | **0.6** | 探测点在片内的随机偏移比例 0~1：默认 0.6（每次采样点不同）；0=固定取中点（结果可复现），1=整片随机（边缘歧义大） |
| `-probe-seed` | 0 | 抖动种子：0=每次运行随机，固定值可复现同一次采样 |
| `-min-piece` | 2048 | GeoCN 切片小于该值就与相邻片合并 |
| `-only-empty` | false | 只处理省市缺失的段 |
| `-only-hmt` | false | 定向刷新：只处理与港澳台地域表有交集的段（修归属 + ISP 以百度为准）；配合缓存约 1 分钟 |
| `-conflicts` | `build/enrich-conflicts.tsv` | ISP 冲突样例输出，空字符串则不写 |
| `-timeout` | 10s | 单请求超时 |
| `-progress` | 2000 | 每 N 条打一行进度 |

**核心规则（改代码前务必读）：**

- **省市默认允许覆盖（`-override=true`），但有门控**：百度说换省却给不出市 → 整条不动
  （低置信度）；云/CDN/IDC 段永远只补空（`regions.IsCloudISP`）。腾讯云、阿里云、
  CloudFlare、Akamai、BGP 多线、IDC 机房这类段的归属方是云厂商，流量落在哪个 PoP 由路由
  决定，百度探到的是 PoP 城市。实测 `1.12.0.0/14`（腾讯云）百度说北京，另两个独立源都说广东。
- **港澳台不在大陆这套规则里**：省份以 ispip 地域表为准，ISP 只要百度有值就直接覆盖纯真
  （见上文"港澳台归属优先级"）。
- **省市被改写时，旧区县和镇一律清空**。挂在旧市下面的区县（如"长春市|玄武区"）在新市下
  不成立，先清掉再由 GeoCN 按新省市重填。
- **区/镇永不改动。** 百度和 GeoCN 都给不出可靠的镇级信息。
- **ISP 顺序（大陆）：百度补空/基础运营商之间才覆盖 → 网段表最后补空 → 纯真兜底。**
  网段表是 APNIC 分配记录，但它**不主动覆盖纯真**——实测 30 万段里 77% 一致、20% 未收录、
  2.5% 冲突，冲突样本全是纯真更细（"歌华有线" vs "中国广电"、"华为云" vs "中国电信"），
  硬覆盖是降级。两边都有值又不同只记冲突，不写库。

请求量：`-probe-step 256` 下约 1 个 /24 一个探测点，全量 340 万级探测点（港澳台表内段也会进），
缓存命中后重跑约 1 分钟。所有结果落 `ip_cache` 表，**重跑零请求**。

### 运营商网段表（ispip）

`data/ispip/*.html` 来自 [ispip.m27.tech](https://ispip.m27.tech/)，数据由 APNIC 分配记录 + whois
每日生成，每个运营商/云厂商一个页面，正文是纯 CIDR 列表。

它和百度的本质区别：**这是分配归属，不是探测点位置**。所以 `1.12.0.0/14`（腾讯云）不会被
说成"北京电信"，也不受 anycast / PoP 影响。

**执行顺序（enrich 里）：百度判省市 + 填 ISP → GeoCN 补区县 → 网段表最后只补空 ISP。**
GeoCN 在百度之后，因为它填区县前要先校验省市自洽，必须拿百度的最终值去校验；
网段表放最后，是因为它只该填空、不该改写已有结论。

| 情况 | 动作 |
|---|---|
| 段的 ISP 为空，网段表有值 | 填（实测累计补过 18,522 段 / 723 万 IP） |
| 两边都有值但不同 | **不动**，只写入 `enrich-conflicts.tsv` |
| 网段表没覆盖 | 保留百度/纯真的值 |

**为什么不主动覆盖**：30 万个国内段抽样（段中点查表）——77.1% 一致、20.4% 未收录、
**2.5% 冲突**，而冲突样本基本是纯真更细：

```
纯真=歌华有线   网段表=中国广电     歌华是广电旗下，纯真更准
纯真=华为云     网段表=中国电信     华为云段被电信的 /14 包住
纯真=方正宽带   网段表=腾讯云       方正宽带是运营商，不是云
```

硬按"网段表 > 纯真"覆盖会把这 2.5% 退化成大桶名。它的价值在**补空**（20% 的段查不到）。

优先级设计：云厂商/IDC（40）> 教育网/科技网/广电（20）> 基础运营商（10）。
不这样排的话，运营商列表会把云厂商的段抢走——同一个 `1.12.0.0/14` 在电信和腾讯云两个列表里都有。

**`othernet` / `hongkong` / `macau` / `taiwan` 四页不当 ISP**：它们是地域兜底桶，不是运营商，
拿来当 ISP 会把"台湾某固网"写成"台湾"。其中港澳台三页另有用途——enrich 用它们定归属地
（见上文"港澳台归属优先级"），ISP 则用百度。

### 备用源：高德（只补百度缺的省市）

百度 opendata 接口免费但不保证给省市——部分大运营商段只返回 `中国 移动`。
实测全库 49 万个探测点里，有 **17,615 个（3.6%）没有省市**，对应 2 万段 / 2,127 万个 IP。

这些段如果只靠 qqwry，会整片错。`111.55.211.0/19`（11,520 个 IP，实为广东/重庆）
就被 qqwry 标成了"山西省"。

所以加了一级兜底：**百度没给省市时，查一次高德**。

| 项 | 数值 |
|---|---|
| 高德 IP 定位价格 | 30 元/万次（基础 LBS 服务） |
| 个人认证免费额度 | 15 万次/月 |
| 企业认证免费额度 | 300 万次/月 |
| 我们单次重建消耗 | **17,615 次** |
| 结论 | 免费额度内，**一年 0 成本** |

全量走 340 万探测点的话一次就超个人额度，所以**只在缺口上发力**——这是关键设计。

启用方式（Key 去 https://console.amap.com/dev/key/app 申请，选「Web服务」类型）：

```bash
export AMAP_KEY=你的key
go run ./cmd/ip2region enrich -qps 60

# 或直接传参
go run ./cmd/ip2region enrich -qps 60 -amap-key 你的key
```

高德的结果也进 `ip_cache`（provider=amap），重跑不会重复烧配额。空结果同样缓存。

注意：高德只支持国内 IPv4，局域网 IP 返回"局域网"（会被当成空值）。

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
| 百度没给到**市级**（只回"中国 移动"或只给到省）+ 非云段 | **GeoCN 与 qqwry 比层级深度，深的胜**（实测 2.8 万片） |
| 我们区县为空 + **市对得上**（省不比较）+ 非云段 | 填区县（实测 57.9 万片） |
| 我们省市为空 | 顺手补上（保证与区县自洽） |
| 我们区县已存在 | **不动**（qqwry 的区县无证据说更差） |
| GeoCN 的市与我们不同 | **该片放弃**（23.9 万片），否则产出"吉林省长春市\|玄武区"这种错位 |
| GeoCN 的区县跨度 > 131072 IP | **放弃**（5 片）——见下 |
| 云/CDN/IDC 段 | 跳过（4,785 段），GeoCN 也是扫 API 来的，云段给的是 PoP 城市 |

**百度没给到市级时怎么裁决**：百度 opendata 接口对大运营商段经常只返回
"中国 移动"（连省都没有），或只给到省。这时比较 GeoCN 和 qqwry 的**层级深度**：

```
省+市  >  只有省  >  空
```

层级更深的胜出；层级相同时**保留 qqwry**（两个源都有市但说法不同，没有证据判断谁更准，
动它只会引入随机抖动）。

典型例子 `111.55.211.0/19`（11,520 个 IP，实为广东/重庆）被 qqwry 整段标成"山西省"
（只有省，层级 1），GeoCN 给出 440100 广州 / 500000 重庆（层级 2）→ GeoCN 胜出。

这条规则的收益：市级覆盖率 93.04% → **95.21%**。

注意：**百度给了市级就完全按百度的来**，GeoCN 只补区县。

**为什么要有跨度上限**：GeoCN 库里确实存在 `59.192.0.0/10（419 万 IP）→ 110102 西城区`
这种条目，那是它扫描采样过稀造成的。一个区不可能有 419 万个 IP。超过 13 万 IP 的
区县一律不采信（省市保留）。

效果：**区县覆盖率 4.67% → 40.75%**（1885 万 → 1.64 亿 IP）。

已知短板：

- 更新"随缘"（README 原话），当前库是 2026-04 的，可能滞后
- ISP 只有 7 种粗标签（电信/联通/移动/鹏博士/教育网/广电），比网段表粗，所以不用它判 ISP
- 港澳台有编码，但命名与库内不一致（`香港` vs `香港特别行政区`），过不了省市自洽校验，实际不在港澳台生效
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

### merge：叠加自定义覆盖

```bash
go run ./cmd/ip2region merge
go run ./cmd/ip2region merge -custom data/custom -out build/ip2region.txt
```

读 `data/custom/` 下所有 `*.txt`，按 [自定义覆盖规则](#自定义覆盖规则) 解析，
用 `Apply` 覆盖到段表上，然后 `Compact` 合并相邻同值段，最后 `Validate` 校验。

任何一步出错（字段过多、IP 非法、起止倒置、合并后出现缝隙）都会**直接报错停下**，
不会产出坏库。

### 版本探针（255.255.255.255）

**每次出库自动写入，不需要手工改任何 txt。**

`255.255.255.255` 是广播地址，任何真实查询都不会命中，用它当版本哨兵。出库时把
原本覆盖它的那段切成两段（`[x..255.255.255.254]` 保持原值 + `[255.255.255.255]`
单独成段），region 用**标准 6 段格式，国家留空**：

```
255.255.255.255|255.255.255.255||20261105|2026-11-05 15:31|||
```

即查询返回 `|20261105|2026-11-05 15:31|||`：省字段=版本日期（YYYYMMDD），
市字段=生成时刻，其余全空。

两条出库路径都会自动注入，时间取**执行出库的那一刻**：

| 命令 | 注入位置 |
|---|---|
| `merge`（`all` / `-only-custom` 都经过） | 写 `build/ip2region.txt` 时 |
| `build -from db`（跳过 merge 的直出） | 导出 txt 时 |

想写入自定义版本号：`merge -version "2026-11-05 16:00"`，不传则自动取当前时间。

加载端读法：

```go
region, _ := searcher.Search([]byte("255.255.255.255"))
parts := strings.Split(region, "|")
// 标准 6 段：国家留空 + 省字段为 8 位日期
if len(parts) >= 3 && parts[0] == "" && isVersionDay(parts[1]) && parts[2] != "" {
    version := parts[1] // 20261105，机器可比
    builtAt := parts[2] // 2026-11-05 15:31
}
```

**判定依据是 `parts[0]=="" 且 parts[1] 为 8 位数字`**：真实段的国家不可能为空，
老探针库返回 `版本|YYYYMMDD|...|||ip2region`，更老的纯真库返回
`纯真网络|||||保留地址`——三种都能区分开，不会误判。

验证方式：`ip2region.exe look -xdb build/ip2region.xdb 255.255.255.255`
（不加 `-xdb` 查的是 SQLite 段表，那里没有探针）。

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

实测：366 万段 → 约 60MB xdb，20 秒。

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

### serve：查询页面（核验数据）

命令行查几个 IP 还行，成批核验就得看表。这个命令起一个本地页面：

```bash
# 直接跑（首次会编译）
go run ./cmd/ip2region serve

# 或者先编译好，之后随时启动
go build -o build/ip2region.exe ./cmd/ip2region
./build/ip2region.exe serve

# 换端口 / 换库
./build/ip2region.exe serve -addr 127.0.0.1:9000 -xdb build/ip2region.xdb
```

打开 http://127.0.0.1:8899/ 即可。**默认只监听 127.0.0.1**，不会暴露到局域网。

| 参数 | 默认 | 说明 |
|---|---|---|
| `-addr` | `127.0.0.1:8899` | 监听地址 |
| `-xdb` | `build/ip2region.xdb` | 库文件（启动时整个读进内存，29MB） |

**页面上能做什么：**

- 单 IP 快速查（带搜索历史，↑↓ 选择，回车复查，Esc 清空）
- 批量查：每行一个，或用逗号/空格分隔，`Ctrl+Enter` 提交
- 支持 `1.1.1.1`、`1.0.0.0/24`、`1.0.0.0-1.0.0.255` 三种写法
- 结果表会显示**该 IP 所属段的起止和大小**，这是核验准确性的关键：
  段小（几个 IP）说明定位精确；段大（≥65536 会标黄）说明这个归属覆盖范围很广，
  可信度要打折
- 顶部汇总条统计多少条带省 / 市 / 区县 / 镇 / ISP
- 点击任意一行复制该行 TSV，方便粘回表格对比

**直接链接到某个查询**（方便记下问题 IP）：

```text
http://127.0.0.1:8899/?ip=183.3.149.185
http://127.0.0.1:8899/?text=1.1.1.1%0A8.8.8.8
```

**注意**：服务纯本地只读，不写任何数据；查的就是当前 `build/ip2region.xdb`，
重新出库后要重启进程才会加载新库。

---

## 发布更新（GitHub Release + 同步上线）

xdb 约 60MB，**不进仓库**，按版本打 GitHub Release 分发。注意 GitHub 上
`hel2o/ip2region` 会重定向到实际仓库 `hel2o/ip2region_update`，API 调用两种写法都行。

### 一次完整更新

```bash
# ① 生成（等价于一条 ./rebuild.sh，展开写便于定位问题）
./rebuild.sh                            # = fetch → parse → legacy → enrich → refine → merge → build → verify → stats

# 也可以逐步跑，方便单步调参：
go run ./cmd/ip2region fetch            # 打印数据版本号，例如 20261028
go run ./cmd/ip2region parse
go run ./cmd/ip2region enrich           # 默认 100 QPS 自适应，45~50 分钟
go run ./cmd/ip2region merge            # 注入版本探针
go run ./cmd/ip2region build -from txt
go run ./cmd/ip2region verify -n 50000  # 必须通过（覆盖 IP = 4294967296）
go run ./cmd/ip2region stats            # 记下覆盖率，要写进 Release 说明

# ② 推送源码（两个远程一起推；内网 git 要绕代理）
git push origin master
NO_PROXY=git.clifford.cn no_proxy=git.clifford.cn git push origin master

# ③ 打 Release 并上传资产（没装 gh CLI，走 API；token 从 git 凭据取）
TOKEN=$(printf "protocol=https\nhost=github.com\n\n" | git credential fill | grep ^password= | cut -d= -f2)
curl -sL -X POST https://api.github.com/repos/hel2o/ip2region/releases \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"tag_name":"v<数据版本>","target_commitish":"master","name":"v<数据版本>","body":"变更 + md5 + 探针 + verify 指标"}'
curl -sL -X POST "https://uploads.github.com/repos/hel2o/ip2region_update/releases/<release_id>/assets?name=ip2region_<数据版本>.xdb" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/octet-stream" \
  --data-binary @build/ip2region.xdb

# ④ 同步上线（见下节）
./sync.sh
```

两个坑：API 首次调用会 301 到 `repositories/<id>/...`，curl 必须带 `-L`；
Release 资产命名统一 `ip2region_<数据版本>.xdb`（`v20261106` 那次传的是
`ip2region.xdb`，下次起带上版本号）。

### 两条硬要求

1. **文件名用 qqwry 数据版本，不是打包日期**。`fetch` 输出的 `远端最新版本: 20261028`
   就是版本号。下游靠文件名判断新旧。
2. **`verify` 不通过不许发**。`覆盖 IP 总数` 必须等于 `4294967296`（2^32），
   说明全覆盖无缝隙；不等于就是库坏了。

### 下游怎么升级

替换 xdb 文件后重启进程即可。用 `NewWithBuffer` 把文件读进内存的方式，必须重启才会重新加载。

### 同步到 3 台机器的 7 个目录并重载

`sync.sh`（Windows 用 `sync.bat`）把 `build/ip2region.xdb` 推到 3 台机器，文件全部到位
后再调各服务的重载接口，让进程立刻用上新库：

| 机器 | 目标路径 | 部署形态 |
|---|---|---|
| `192.168.99.26` | `/home/work/management-system/xdb/ip2region.xdb` | 管理平台运行目录 |
| `192.168.99.26` | `/home/work/workspace/src/github.com/hel2o/management-system/xdb/ip2region.xdb` | 管理平台源码目录 |
| `192.168.99.26` | `/home/work/workspace/src/github.com/hel2o/sniffer/ip2region.xdb` | sniffer 源码目录 |
| `192.168.99.26` | `/home/work/workspace/src/github.com/hel2o/english_school/ip2region.xdb` | english_school 源码目录 |
| `192.168.99.26` | `/home/work/go-syslog/ip2region.xdb` | go-syslog 运行目录 |
| `172.25.0.19` | `/home/sniffer/ip2region.xdb` | sniffer 运行目录 |
| `172.19.99.1` | `/home/english_school/ip2region.xdb` | english_school 运行目录 |

重载接口（文件全部同步成功后才调；任一目标同步失败就整轮不重载，避免新旧混读）。
2026-11-04 起四个服务统一为 `GET /xdb/reload`，返回同一套 JSON：

```json
{"file":"ip2region.xdb","msg":"success","mtime":"2025-08-21 15:01:54","reloadTime":"2026-11-04 20:45:39","size":44407644}
```

| 服务 | 接口 |
|---|---|
| sniffer | `http://172.25.0.19:8080/xdb/reload` |
| english_school | `https://cs.clifford.cn/xdb/reload` |
| management-system | `https://network.clifford.cn/xdb/reload` |
| go-syslog | `https://network.clifford.cn:1993/xdb/reload` |

脚本会解析返回里的 `size`，与本地文件字节数比对：**对不上就报 `[不一致]`**，说明服务加载的
不是刚推上去的那份（通常是路径不对或进程工作目录不同）。`mtime` 也会一并打印，方便确认生效时间。
HTTP 404 会提示「服务端还没发布 /xdb/reload」；`management-system` 配了
`/api/admin/xdb/reload`、`/api/xdb/reload` 两个备选前缀，主路径 404 时自动回退尝试。

```bash
./sync.sh              # 同步 + 重载
./sync.sh -n           # dry-run：只比 md5，报「已是同一版本 / 将更新 / 目标不存在」
./sync.sh -src build/test.xdb   # 换源文件
./sync.sh -keep 5      # 每个目标保留 5 份历史备份（默认 3）
./sync.sh -skip-reload          # 只传文件
./sync.sh -strict-reload        # 重载失败也算失败（默认只告警）
./sync.sh -ms-cookie 'token=..;account=..'   # 可选：给重载接口带登录 Cookie
```

> 四个接口 2026-11-04 起都免登录。`management-system` 的那个原本挂在 `AuthMiddleware` 后面
> （`/api/admin/reloadIp2Region`），现在统一到 `/xdb/reload` 并放在免鉴权区；以后若改回鉴权，
> 脚本会打出 `[失败] … {"code":4001,...}`，这时用 `-ms-cookie`（或环境变量 `IPREGION_MS_COOKIE`）
> 把浏览器的 `token` + `account` 传进去即可。Cookie 会过期，不建议写死进脚本。
>
> 路径大小写敏感，`/xdb/reload` 写错就是 gin 的 `404 page not found`。`go-syslog` 必须带
> `:1993`（它自己起的 HTTPS，不是 443 上的管理平台）；源码在 `hel2o/go-syslog` 的 `http/router.go`，
> 对应 `db.ReloadIP2Region()`。

实现要点（都是为了避免线上读到半截文件）：

- **整台跳过**：先并行问一遍各目标 md5，全等于源 md5 就整台跳过，连上传都不做。
- **只上传一次**：需要更新时先 `scp` 到该机 `/tmp/ip2region.<md5>.xdb`，再在远端 `cp`
  到本机所有目标，47MB 只走一遍网络（同 md5 的临时文件已存在时连上传都跳过）。
- **原子替换**：写同目录的 `ip2region.xdb.tmp.<pid>`，`chown`/`chmod` 对齐旧文件后 `mv -f`，
  不会出现读到写一半的情况。
- **自动备份**：覆盖前 `cp -p` 成 `ip2region.xdb.bak.<时间戳>`，每个目标只留最近 `-keep` 份。
- **回读校验**：每个目标传完重新 `md5sum` 比对，不一致退出码非 0。
- **防呆**：源文件小于 5MB 直接拒绝推（半截文件/空占位别砸线上）。

三台机器都要免密（`~/.ssh/config` 里 `192.168.99.26` 用 `es_deploy`，`172.25.0.19` 与
`172.19.99.1` 也已配好）。某台连不上时脚本打 `!! ssh ... 连不上` 跳过该机、继续处理其它机器，
最后退出码非 0 提醒你；该机的文件和重载都没动，仍是旧的一致版本，修好免密重跑一次补齐即可。

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
| 省 | 95.84% | **99.95%** | qqwry + 百度（覆盖/补空）+ 港澳台表 |
| 市 | 81.18% | **99.17%** | qqwry + 百度（覆盖/补空） |
| 区县 | 4.67% | **44.02%** | qqwry + GeoCN 按边界切分补空 |
| 乡镇 | 15.01% | 11.24% | 只有 qqwry 有（GeoCN 没有镇级），省市改写时会清掉再补不回来 |
| ISP | 85.00% | **96.26%** | qqwry + 百度 + 网段表补空 |

> 2026-11-06 实测，中国空间 4.06 亿 IP（1,529,220 段）加权。

### 三个硬限制，先说清楚

1. **乡镇覆盖率低（11.24%），这是数据源的天花板。**
   区县靠 GeoCN 提到了 44.02%，但**镇级没有任何源提供**——qqwry 的 area 字段偶尔有，
   百度、GeoCN、运营商网段表都没有。要镇级精度只能靠自定义覆盖。
   剩下的 56% 区县空缺，是因为 GeoCN 本身只到市级，或省市与我们不一致而主动放弃。

2. **百度的覆盖必须带门控。**
   交叉验证（ipwho.is / ipapi.is / ip-api 三个独立源）显示，百度与 qqwry 在省市上是
   同一量级的噪声源，所以覆盖只在"百度给出了完整省市"时生效（换省必须同时给市），
   云段、港澳台以外的 ISP 都另有规则兜底——不是无脑覆盖。

3. **境外段没有增强。**
   百度对境外只返回国家（`8.8.8.8 → "美国"`），比 qqwry 的
   `美国|加利福尼亚州|圣克拉拉|山景城| |谷歌公司DNS服务器` 差得多，查了等于主动降级。

### 数据清洗（水印 / 重复层级）

纯真免费版会给**境外记录**塞版权水印 `CZ88.NET`——不太老的库干脆把地区串整个写成
`CZ88.NET`，于是它被当成 ISP（95 万段）、市（3103 段）、镇（3446 段）写进库里，
还有 `沙田区/CZ88.NET` 这种和地名粘在一起的形态。

另外还有一层冗余：**同名层级重复**。典型是直辖市/特别行政区——`regions.Split` 会把
省级回填成市级，于是出现 `北京市|北京市`、`香港特别行政区|香港特别行政区`；
境外也有 `美国|哥伦比亚特区|华盛顿`（省市同名）、`美国|加利福尼亚州|洛杉矶|洛杉矶`
（市县同名），拼成一句话就是"香港特别行政区香港特别行政区"这种重复。

清洗规则统一为 **同名时清掉"市"**（省最粗、区县最细，两头都保留）：

| 情况 | 例子 | 清洗后 |
|---|---|---|
| 市 == 省（直辖市/特区） | `中国\|北京市\|北京市` | `中国\|北京市\|\|` |
| 市 == 区县（市县同名） | `美国\|加利福尼亚州\|洛杉矶\|洛杉矶` | `美国\|加利福尼亚州\|\|洛杉矶` |
| 区县 == 省（三层全同名） | `德国\|汉堡\|汉堡\|汉堡` | `德国\|汉堡\|\|\|` |

清洗分三层，任何一层漏了后面都能兜住：

1. `regions.StripWatermark` + `regions.Split`：解析入口先摘水印，避免它参与拆分
   （否则 `CZ88.NET` 会占住"市"字段，挡住 GeoCN/百度补空）
2. `legacy`：从旧 xdb 搬省市之前先清洗，避免旧库的脏值被搬进来
3. `merge` / `build -from db`：导出前整段扫描兜底（水印 + 重复层级），
   清完再 `Compact`，让清空同名字段后相邻的段能合并回一段

实测一次全量清洗：水印 95.8 万段、重复层级 15.0 万段（市==省 9.7 万、市==区 2.3 万）。

**去重对全量重建同样生效**：`all` 的链路是 `… → merge → build -from txt`，`merge` 是成品
txt 的唯一出口，清洗就挂在那里，所以 `rebuild.bat` 全量跑完的 xdb 必然是去重后的。
`merge` 出口还加了硬断言——清洗后若仍有水印或同名层级，直接构建失败，不会把脏数据落库。
`verify` 统计的是 SQLite 中间表，那里 `省==市` 属于正常状态（直辖市/特区，`enrich`
比较层级时要用），所以只打印数量不做告警。

> 副作用（预期内）：ISP 字段清空后 `legacy` 的"只补空"规则会把旧库里的**真实境外
> 运营商**搬回来（境外 ISP 唯一值 1.0 万 → 6.8 万），区串唯一数 20.4 万 → 36.1 万，
> xdb 从 47MB 涨到 57MB。体积换的是真实数据，嫌大的话可以只保留结构化运营商、
> 丢掉长尾组织名，但会损失精度。

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

内存占用就是文件大小（约 60MB）。注意 `Searcher` 不是并发安全的，每个 goroutine 一个实例。

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
A：可以。跳过 `enrich` 和 `refine`，直接 `merge` + `build`。代价是市覆盖率 81% 而不是 99%。

**Q：能不能换高德/ip138 数据源？**
A：`internal/provider` 是接口化的。实现 `Provider`（`Name()` + `Lookup()`），
在 `cmd/ip2region/enrich.go` 里换一行构造，enrich/refine 主流程不用改。

**Q：xdb 多大？能压吗？**
A：约 60MB（300 万段），主要是 region 字符串。每个段都要存完整区域串，压不动多少。
想小一点只能用 `build -policy btree`（索引略小，查询稍慢）。

**Q：`verify` 的"真冲突 1.31%"要不要处理？**
A：可以人工抽查。冲突样例在 `build/enrich-conflicts.tsv`，觉得该改的写进 `data/custom/`。

---

## 目录结构

```
cmd/ip2region/          唯一入口，子命令分发
  all.go                一键流水线（按序调下面各步）
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
internal/legacy/        旧版 xdb 读取（境外省市补丁）
internal/ispip/         运营商网段表（APNIC 分配归属）+ 港澳台地域表（hmt.go）
internal/geocn/         GeoCN MMDB 读取 + 行政区划编码翻译
internal/enrich/        /24 去重、限流、缓存复用、冲突裁定
internal/refine/        大段二分收敛细分
cmd/seedfill/           一次性工具：用历史库回填省市（人工跑）
cmd/atest/              固定 vs 自适应限速的吞吐对比（人工跑）
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

rebuild.bat             Windows 一键重建（双击可用）
rebuild.sh              Linux/macOS 一键重建
sync.bat                Windows 一键同步 + 重载（双击可用）
sync.sh                 同步 xdb 到 3 台机器 7 个目录并调重载接口
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
