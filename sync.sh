#!/usr/bin/env bash
# ip2region.xdb 一键同步到 3 台机器的 6 个目录，并调用各服务的重载接口
#
#   ./sync.sh                同步 build/ip2region.xdb 到全部目标，成功后自动重载
#   ./sync.sh -n             只看会做什么，不实际传（dry-run）
#   ./sync.sh -src build/test.xdb     换源文件
#   ./sync.sh -keep 5                 每个目标保留 5 份历史备份（默认 3）
#   ./sync.sh -skip-reload            只传文件，不调重载接口
#   ./sync.sh -strict-reload          重载失败也算失败（默认只告警）
#   ./sync.sh -ms-cookie 'token=..;account=..'
#                                     调 management-system 重载所需的登录 Cookie
#                                     （也可用环境变量 IPREGION_MS_COOKIE）
#
# 目标（host|路径）：
#   192.168.99.26  /home/work/management-system/xdb/ip2region.xdb
#   192.168.99.26  /home/work/workspace/src/github.com/hel2o/management-system/xdb/ip2region.xdb
#   192.168.99.26  /home/work/workspace/src/github.com/hel2o/sniffer/ip2region.xdb
#   192.168.99.26  /home/work/workspace/src/github.com/hel2o/english_school/ip2region.xdb
#   172.25.0.19    /home/sniffer/ip2region.xdb
#   172.19.99.1    /home/english_school/ip2region.xdb
#
# 做法：每台机器先 scp 一份到远端 /tmp（按 md5 命名，已存在则跳过上传），
# 再在远端循环分发——备份旧文件 → 写同目录临时文件 → chown/chmod 对齐 →
# mv 原子替换 → 校验 md5。文件全部到位后才调用重载接口。

set -euo pipefail
cd "$(dirname "$0")"

SRC="build/ip2region.xdb"
KEEP=3
DRY=0
SKIP_RELOAD=0
STRICT_RELOAD=0
MS_COOKIE="${IPREGION_MS_COOKIE:-}"

MAIN_HOST="192.168.99.26"

# 目标清单：host|远端的 xdb 路径
TARGETS=(
    "192.168.99.26|/home/work/management-system/xdb/ip2region.xdb"
    "192.168.99.26|/home/work/workspace/src/github.com/hel2o/management-system/xdb/ip2region.xdb"
    "192.168.99.26|/home/work/workspace/src/github.com/hel2o/sniffer/ip2region.xdb"
    "192.168.99.26|/home/work/workspace/src/github.com/hel2o/english_school/ip2region.xdb"
    "172.25.0.19|/home/sniffer/ip2region.xdb"
    "172.19.99.1|/home/english_school/ip2region.xdb"
)

# 重载接口：名称|URL|是否需要登录 Cookie
RELOADS=(
    "sniffer|http://172.25.0.19:8080/reloadIp2Region|0"
    "english_school|https://cs.clifford.cn/reloadxdb|0"
    "management-system|https://network.clifford.cn/api/admin/reloadIp2Region|1"
)

# 调试用：IPREGION_TARGETS="host|path;host|path" / IPREGION_RELOADS="name|url|0;name2|url2|1"
# 可临时只同步某一台或只测某几个重载接口，不影响默认清单。
if [ -n "${IPREGION_TARGETS:-}" ]; then
    IFS=';' read -r -a TARGETS <<< "$IPREGION_TARGETS"
fi
if [ -n "${IPREGION_RELOADS:-}" ]; then
    IFS=';' read -r -a RELOADS <<< "$IPREGION_RELOADS"
fi

while [ $# -gt 0 ]; do
    case "$1" in
        -n|--dry-run)   DRY=1; shift ;;
        -src)           SRC="$2"; shift 2 ;;
        -keep)          KEEP="$2"; shift 2 ;;
        -host|-main-host) MAIN_HOST="$2"; shift 2 ;;
        -skip-reload|--no-reload) SKIP_RELOAD=1; shift ;;
        -strict-reload) STRICT_RELOAD=1; shift ;;
        -ms-cookie)     MS_COOKIE="$2"; shift 2 ;;
        -h|--help)      sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1（-h 看用法）" >&2; exit 2 ;;
    esac
done

[ -f "$SRC" ] || { echo "!! 源文件不存在: $SRC" >&2; exit 1; }

# -main-host 只影响 99.26 那一组（另外两台是各自的业务机，不跟着换）
if [ "$MAIN_HOST" != "192.168.99.26" ]; then
    for i in "${!TARGETS[@]}"; do
        case "${TARGETS[$i]}" in
            192.168.99.26\|*) TARGETS[$i]="$MAIN_HOST|${TARGETS[$i]#*|}" ;;
        esac
    done
fi

SIZE=$(wc -c < "$SRC" | tr -d ' ')
# 防呆：正常的库 20MB 以上；太小说明是半截文件或空占位，别推上去砸线上
if [ "$SIZE" -lt 5000000 ]; then
    echo "!! 源文件只有 $SIZE 字节，明显不是正常的 xdb，拒绝同步" >&2
    exit 1
fi

MD5=$(md5sum "$SRC" | cut -d' ' -f1)
REMOTE_TMP="/tmp/ip2region.$MD5.xdb"

# 按出现顺序去重出主机列表
HOSTS=()
for spec in "${TARGETS[@]}"; do
    h="${spec%%|*}"
    found=0
    for x in ${HOSTS[@]+"${HOSTS[@]}"}; do [ "$x" = "$h" ] && found=1 && break; done
    [ "$found" = "0" ] && HOSTS+=("$h")
done

echo "==== 同步 ip2region.xdb ===="
echo "源文件 : $SRC"
echo "大小   : $SIZE 字节（$(awk -v n="$SIZE" 'BEGIN{printf "%.1f MB", n/1048576}')）"
echo "md5    : $MD5"
echo "目标数 : ${#TARGETS[@]}（${#HOSTS[@]} 台机器）"
for spec in "${TARGETS[@]}"; do
    printf '  - %-15s %s\n' "${spec%%|*}" "${spec#*|}"
done
echo

# 下载/分发脚本走 stdin，避免多层引号地狱
read -r -d '' REMOTE_DIST <<'REMOTE' || true
set -euo pipefail
tmp="$1"; want="$2"; keep="$3"; shift 3

for t in "$@"; do
    dir=$(dirname "$t")
    mkdir -p "$dir"

    if [ -f "$t" ] && [ "$(md5sum "$t" | cut -d' ' -f1)" = "$want" ]; then
        echo "SAME|$t|$want"
        continue
    fi

    # 备份旧文件（保留属主/权限/时间）
    owner=""; mode=""
    if [ -f "$t" ]; then
        owner=$(stat -c '%u:%g' "$t")
        mode=$(stat -c '%a' "$t")
        cp -p "$t" "$t.bak.$(date +%Y%m%d%H%M%S)"
    fi

    # 同目录写临时文件再 mv，避免读到写一半的文件
    newf="$t.tmp.$$"
    cp "$tmp" "$newf"
    if [ -n "$owner" ]; then chown "$owner" "$newf" 2>/dev/null || true; fi
    if [ -n "$mode" ];  then chmod "$mode"  "$newf" 2>/dev/null || true; fi
    if [ -z "$owner" ]; then chmod 644 "$newf" 2>/dev/null || true; fi
    mv -f "$newf" "$t"

    got=$(md5sum "$t" | cut -d' ' -f1)
    if [ "$got" = "$want" ]; then echo "OK|$t|$got"; else echo "BAD|$t|$got"; fi
done

# 备份轮转：每个目标只留最近 $keep 份
for t in "$@"; do
    ls -1t "$t".bak.* 2>/dev/null | tail -n +$((keep + 1)) | while read -r f; do rm -f "$f"; done
done || true

rm -f "$tmp"
REMOTE

FAILED=0
TOTAL_OK=0
TOTAL_SAME=0

for host in "${HOSTS[@]}"; do
    # 这台机器的路径
    paths=()
    for spec in "${TARGETS[@]}"; do
        [ "${spec%%|*}" = "$host" ] && paths+=("${spec#*|}")
    done

    echo "[$host] ${#paths[@]} 个目标"

    if [ "$DRY" = "1" ]; then
        if ! ssh -o BatchMode=yes -o ConnectTimeout=8 "$host" true 2>/dev/null; then
            echo "  !! ssh $host 不通（跳过检查）"; FAILED=1; echo; continue
        fi
        for t in "${paths[@]}"; do
            cur=$(ssh "$host" "if [ -f '$t' ]; then md5sum '$t' | cut -d' ' -f1; else echo MISSING; fi")
            if [ "$cur" = "$MD5" ]; then echo "  已是同一版本  $t"
            elif [ "$cur" = "MISSING" ]; then echo "  目标不存在    $t"
            else echo "  将更新        $t  (当前 $cur)"; fi
        done
        echo; continue
    fi

    # ① 先问一遍现状：全部同 md5 就整台跳过（省一次 47MB 上传）
    CUR_MD5S=$(ssh -o BatchMode=yes -o ConnectTimeout=8 "$host" bash -s -- "${paths[@]}" <<'REMOTE_MD5' 2>/dev/null || true
for t in "$@"; do
    if [ -f "$t" ]; then md5sum "$t" | cut -d' ' -f1; else echo MISSING; fi
done
REMOTE_MD5
)
    ALL_SAME=1
    idx=0
    for t in "${paths[@]}"; do
        idx=$((idx + 1))
        cur=$(printf '%s\n' "$CUR_MD5S" | sed -n "${idx}p")
        [ "$cur" = "$MD5" ] || ALL_SAME=0
    done

    if [ "$ALL_SAME" = "1" ]; then
        for t in "${paths[@]}"; do printf '  [最新] %s\n' "$t"; done
        TOTAL_SAME=$((TOTAL_SAME + ${#paths[@]}))
        echo; continue
    fi

    # ② 上传一份到远端临时目录（同 md5 的已在就不重复传）
    if ssh -o BatchMode=yes -o ConnectTimeout=8 "$host" "test -f '$REMOTE_TMP'" 2>/dev/null; then
        echo "  远端已有同版本临时文件，跳过上传"
    else
        echo -n "  上传中 ... "
        if ! scp -q -o BatchMode=yes -o ConnectTimeout=8 "$SRC" "$host:$REMOTE_TMP"; then
            echo "失败"
            echo "  !! scp 到 $host 失败（检查免密/网络）"
            FAILED=1; echo; continue
        fi
        echo "完成"
    fi

    # ③ 远端分发
    echo -n "  分发中 ... "
    if ! DIST_OUT=$(ssh -o BatchMode=yes "$host" bash -s -- "$REMOTE_TMP" "$MD5" "$KEEP" "${paths[@]}" <<<"$REMOTE_DIST" 2>&1); then
        echo "失败"
        printf '%s\n' "$DIST_OUT" | sed 's/^/    /'
        FAILED=1; echo; continue
    fi
    echo "完成"

    printf '%s\n' "$DIST_OUT" | while IFS='|' read -r st path hash; do
        case "$st" in
            OK)   printf '  [更新] %s\n' "$path" ;;
            SAME) printf '  [最新] %s\n' "$path" ;;
            BAD)  printf '  [校验失败] %s  md5=%s\n' "$path" "$hash" ;;
        esac
    done

    if printf '%s\n' "$DIST_OUT" | grep -q '^BAD|'; then
        echo "  !! $host 有目标校验不通过"
        FAILED=1
    fi
    TOTAL_OK=$((TOTAL_OK + $(printf '%s\n' "$DIST_OUT" | grep -c '^OK|' || true)))
    TOTAL_SAME=$((TOTAL_SAME + $(printf '%s\n' "$DIST_OUT" | grep -c '^SAME|' || true)))
    echo
done

if [ "$DRY" = "1" ]; then
    echo "[dry-run] 结束（未上传、未重载）"
    exit $FAILED
fi

echo "==== 文件同步：$TOTAL_OK 个更新，$TOTAL_SAME 个已是最新 ===="

if [ "$FAILED" != "0" ]; then
    echo "!! 有目标同步失败，跳过重载（避免服务读到新旧混合的数据）" >&2
    exit 1
fi

if [ "$SKIP_RELOAD" = "1" ]; then
    echo "（-skip-reload：不调用重载接口）"
    exit 0
fi

# ③ 文件全部到位后才重载，顺序与依赖无关
echo
echo "==== 调用重载接口 ===="
RELOAD_FAILED=0
for spec in "${RELOADS[@]}"; do
    name="${spec%%|*}"; rest="${spec#*|}"
    url="${rest%%|*}"; need_auth="${rest##*|}"

    if [ "$need_auth" = "1" ] && [ -z "$MS_COOKIE" ]; then
        printf '  [跳过] %-17s %s\n         需要登录 Cookie：-ms-cookie 或 IPREGION_MS_COOKIE\n' "$name" "$url"
        continue
    fi

    if [ -n "$MS_COOKIE" ] && [ "$need_auth" = "1" ]; then
        RAW=$(curl -sS -k -m 15 -w '\n%{http_code}' -H "Cookie: $MS_COOKIE" "$url" 2>&1) || true
    else
        RAW=$(curl -sS -k -m 15 -w '\n%{http_code}' "$url" 2>&1) || true
    fi

    code=$(printf '%s\n' "$RAW" | tail -n 1)
    body=$(printf '%s\n' "$RAW" | sed '$d' | tr -d '\r')

    if [ "$code" = "200" ] && ! printf '%s' "$body" | grep -q '"code":400[0-9]'; then
        printf '  [重载] %-17s ok  %s\n' "$name" "$(printf '%s' "$body" | head -c 100)"
    else
        printf '  [失败] %-17s HTTP %s  %s\n' "$name" "$code" "$(printf '%s' "$body" | head -c 120)"
        RELOAD_FAILED=1
    fi
done

echo
if [ "$RELOAD_FAILED" = "1" ]; then
    echo "!! 有重载接口调用失败，对应服务可能仍在用旧库（文件已更新，可稍后手动重载）" >&2
    [ "$STRICT_RELOAD" = "1" ] && exit 1
    exit 0
fi
echo "==== 完成 ===="
