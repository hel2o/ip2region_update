#!/usr/bin/env bash
# ip2region.xdb 一键同步到 99.26 上的四个项目目录
#
#   ./sync.sh                同步 build/ip2region.xdb 到全部 4 个目标
#   ./sync.sh -n             只看会做什么，不实际传（dry-run）
#   ./sync.sh -src build/test.xdb        换源文件
#   ./sync.sh -host 192.168.99.26        换目标机
#   ./sync.sh -keep 5        每个目标保留 5 份历史备份（默认 3）
#
# 做法：先 scp 一份到远端 /tmp（按 md5 命名，已存在则跳过上传），
# 再在远端循环分发——备份旧文件 → 写同目录临时文件 → chown/chmod 对齐 →
# mv 原子替换 → 校验 md5。任一目标 md5 不符则退出码非 0。
#
# 只动文件，不重启任何服务。正在跑的进程若不支持热加载，需要你自己 restart。

set -euo pipefail
cd "$(dirname "$0")"

HOST="192.168.99.26"
SRC="build/ip2region.xdb"
KEEP=3
DRY=0

TARGETS=(
    "/home/work/management-system/xdb/ip2region.xdb"
    "/home/work/workspace/src/github.com/hel2o/management-system/xdb/ip2region.xdb"
    "/home/work/workspace/src/github.com/hel2o/sniffer/ip2region.xdb"
    "/home/work/workspace/src/github.com/hel2o/english_school/ip2region.xdb"
)

while [ $# -gt 0 ]; do
    case "$1" in
        -n|--dry-run) DRY=1; shift ;;
        -src)  SRC="$2"; shift 2 ;;
        -host) HOST="$2"; shift 2 ;;
        -keep) KEEP="$2"; shift 2 ;;
        -h|--help) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1（-h 看用法）" >&2; exit 2 ;;
    esac
done

[ -f "$SRC" ] || { echo "!! 源文件不存在: $SRC" >&2; exit 1; }

SIZE=$(wc -c < "$SRC" | tr -d ' ')
# 防呆：正常的库 20MB 以上；太小说明是半截文件或空占位，别推上去砸线上
if [ "$SIZE" -lt 5000000 ]; then
    echo "!! 源文件只有 $SIZE 字节，明显不是正常的 xdb，拒绝同步" >&2
    exit 1
fi

MD5=$(md5sum "$SRC" | cut -d' ' -f1)
REMOTE_TMP="/tmp/ip2region.$MD5.xdb"

echo "==== 同步 ip2region.xdb ===="
echo "源文件 : $SRC"
echo "大小   : $SIZE 字节（$(awk -v n="$SIZE" 'BEGIN{printf "%.1f MB", n/1048576}')）"
echo "md5    : $MD5"
echo "目标机 : $HOST"
echo "目标数 : ${#TARGETS[@]}"
for t in "${TARGETS[@]}"; do echo "  - $t"; done
echo

if [ "$DRY" = "1" ]; then
    echo "[dry-run] 只检查可达性与现有版本，不上传"
    ssh -o BatchMode=yes "$HOST" true || { echo "!! ssh $HOST 不通" >&2; exit 1; }
    for t in "${TARGETS[@]}"; do
        cur=$(ssh "$HOST" "if [ -f '$t' ]; then md5sum '$t' | cut -d' ' -f1; else echo MISSING; fi")
        if [ "$cur" = "$MD5" ]; then echo "  已是同一版本  $t"
        elif [ "$cur" = "MISSING" ]; then echo "  目标不存在    $t"
        else echo "  将更新        $t  (当前 $cur)"; fi
    done
    exit 0
fi

# ① 上传一份到远端临时目录（同 md5 的已在就不重复传）
if ssh "$HOST" "test -f '$REMOTE_TMP'" 2>/dev/null; then
    echo "远端已有同版本临时文件，跳过上传"
else
    echo "上传中 ..."
    scp -q "$SRC" "$HOST:$REMOTE_TMP"
    echo "上传完成"
fi

# ② 远端分发（脚本走 stdin，避免多层引号地狱）
echo "分发中 ..."
DIST_OUT=$(ssh "$HOST" bash -s -- "$REMOTE_TMP" "$MD5" "$KEEP" "${TARGETS[@]}" <<'REMOTE'
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
    [ -n "$owner" ] && chown "$owner" "$newf" || true
    [ -n "$mode" ]  && chmod "$mode"  "$newf" || true
    [ -z "$owner" ] && chmod 644 "$newf" || true
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
)

printf '%s\n' "$DIST_OUT" | while IFS='|' read -r st path hash; do
    case "$st" in
        OK)   printf '  [更新] %s\n' "$path" ;;
        SAME) printf '  [最新] %s\n' "$path" ;;
        BAD)  printf '  [校验失败] %s  md5=%s\n' "$path" "$hash" ;;
    esac
done

if printf '%s\n' "$DIST_OUT" | grep -q '^BAD|'; then
    echo
    echo "!! 有目标校验不通过，见上面 [校验失败] 行" >&2
    exit 1
fi

echo
echo "==== 完成：$(printf '%s\n' "$DIST_OUT" | grep -c '^OK|') 个更新，$(printf '%s\n' "$DIST_OUT" | grep -c '^SAME|') 个已是最新 ===="
echo "提示：正在运行的服务需要重启才会重新加载 xdb（本例不自动重启）。"
