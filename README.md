# IP2Region XDB 数据库（20261028）

基于纯真 `qqwry.dat` 构建的离线 IP 归属库，用百度接口补齐缺失的省/市/ISP，再叠加人工覆盖规则。

**当前只有一份文件，没有 `_i` / `_m` 变体了**——爱奇艺、美团两个接口都已停用。

| 文件 | 大小 | 基础数据 | 说明 |
|---|---|---|---|
| `ip2region_20261028.xdb` | 22.1 MB | qqwry 20261028 | 百度补空 + 人工覆盖，覆盖全部 42.9 亿 IPv4 |

---

## 历史沿革

| 时间 | 变更 |
|---|---|
| 2025-07-21 | 三份库：`_i.xdb`（qqwry + 爱奇艺）、`_m.xdb`（qqwry + 美团）、`_qqwry.xdb`（纯 qqwry） |
| 2025-07 底 | **爱奇艺接口被限流**，`_i.xdb` 停止更新 |
| 2026-11 | **美团接口也停了**，`_m.xdb` 停止更新；改为只走百度接口 |
| 2026-11-04 | 重新构建：单一 xdb、统一 6 段格式、加人工覆盖规则 |

`_i.xdb` / `_m.xdb` 已删除，不要再引用。

---

## ⚠️ 区域串是 6 段，不是官方默认的 5 段

这是最容易踩的坑。官方 SDK 的 `Region` 结构按 **5 段**解析，直接用它的便捷方法会字段错位。

```
183.3.149.185 → 中国|广东省|广州市|番禺区|钟村|中国电信
                └国┘└─省──┘└─市──┘└─区──┘└镇┘└─ISP──┘
                 0    1      2      3     4     5
```

查询侧必须自己切 6 段：

```go
p := strings.SplitN(region, "|", 6)
// p[0] 国家  p[1] 省  p[2] 市  p[3] 区  p[4] 镇  p[5] ISP
```

空字段表示该级没有数据（区/镇缺失很常见，qqwry 本身就是城市级库）。

### Go 示例

```go
import "github.com/lionsoul2014/ip2region/binding/golang/xdb"

searcher, err := xdb.NewWithFileOnly("ip2region_20261028.xdb")
if err != nil {
    panic(err)
}
defer searcher.Close()

region, err := searcher.SearchByStr("183.3.149.185")
// → "中国|广东省|广州市|番禺区|钟村|中国电信"
```

> 用官方 binding 前确认 `SearchByStr` 没被改过。曾经有个版本在里面加了 replacer
> 把 `|`、`0`、`中国` 全删了，连 IP 里的 0 都会删，查出来全是残缺结果。

高并发场景把文件读进内存：

```go
cBuff, err := xdb.LoadContentFromFile("ip2region_20261028.xdb")
searcher, err := xdb.NewWithBuffer(cBuff)
```

---

## 数据质量

中国 IP 按 IP 数加权统计：

| 字段 | 覆盖率 | 来源 |
|---|---|---|
| 省 | 99.08% | qqwry + 百度补空 |
| 市 | 91.54% | qqwry + 百度补空 |
| 区县 | 40.67% | qqwry + GeoCN 补空 |
| 乡镇 | 14.55% | 仅 qqwry 有（无源提供镇级） |
| ISP | 90.48% | qqwry + APNIC 网段表 + 百度补空 |

乡镇覆盖率低是数据源的天花板——**没有任何源提供镇级**。要镇级精度只能靠人工覆盖。

---

## 与旧版的差异

| 项目 | 旧版（20250721） | 当前（20261028） |
|---|---|---|
| 文件数 | 3 份（`_i` / `_m` / `_qqwry`） | 1 份 |
| 数据源 | qqwry + 爱奇艺 API / 美团 API | qqwry + APNIC 网段表 + GeoCN + 百度接口 |
| 区域串 | 不统一（部分是 `广东–广州腾讯云` 这种未拆分串） | 统一 6 段 |
| 区县 | 0% | 40.67% |
| 人工覆盖 | 无 | `data/custom/*.txt`，36 条 |

---

## 如何更新这份库

完整流程在构建仓库 `hel2o/ip2region`（不是本仓库）里跑，本仓库只放产物。

### 1. 在构建仓库里生成

```bash
cd /path/to/hel2o/ip2region

go run ./cmd/ip2region fetch            # 下载最新 qqwry.dat（会打印版本号）
go run ./cmd/ip2region parse            # 解析成段表，约 10 秒
go run ./cmd/ip2region enrich -qps 60   # 百度补空，有缓存则几十秒
go run ./cmd/ip2region refine -qps 60   # 大段细分
go run ./cmd/ip2region merge            # 叠加 data/custom/*.txt
go run ./cmd/ip2region build -from txt  # → build/ip2region.xdb
go run ./cmd/ip2region verify -n 50000  # 验收：全覆盖 + 与旧库对拍
go run ./cmd/ip2region stats            # 看字段覆盖率
```

`verify` 必须通过（`覆盖 IP 总数 = 4294967296`）才能发布。

### 2. 按数据版本号命名

文件名用 **qqwry 数据版本**，不是当天日期。`fetch` 会打印版本号：

```
06:10:37 远端最新版本: 20261028
```

对应文件就是 `ip2region_20261028.xdb`。下游靠文件名判断新旧。

### 3. 拷进本仓库并提交

```bash
cd /path/to/hel2o/ip2region/ip2region_update
rm -f ip2region_*.xdb                                   # 只保留最新一份
cp ../build/ip2region.xdb ./ip2region_<数据版本>.xdb
# 同步更新本 README 里的版本号、大小、覆盖率数字
git add -A
git commit -m "更新 xdb: <数据版本>"
git push origin main
```

### 4. 下游更新

替换 xdb 文件后重启进程即可。用 `NewWithBuffer` 把文件读进内存的方式，必须重启才会重新加载。

---

## 构建仓库

数据怎么来的、规则为什么这么定，见构建仓库 `hel2o/ip2region` 的 README。要点：

- **百度只补空，不覆盖已有值**：实测两者省市准确率打平（40 样本交叉验证 82.1% vs 82.1%），
  覆盖等于抛硬币
- **云/CDN/IDC 段永远不覆盖**：腾讯云、阿里云、CloudFlare 这类段的流量落点由路由决定，
  探到的只是 PoP 城市
- **ISP 先看 APNIC 网段表**（ispip.m27.tech）：分配归属比探测位置可靠
- **区县来自 GeoCN**（ljxi/GeoCN）：唯一能到区县级的源，只补空且要求省市自洽
- **区/镇永不改动**：只认 qqwry
