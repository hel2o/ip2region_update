#!/usr/bin/env bash
# ip2region 一键重建（Linux / macOS / Git Bash）
#
#   ./rebuild.sh                            全量重建（下载 + 百度增强 + 收敛）
#   ./rebuild.sh -skip-fetch                用本地已有数据，不下载
#   ./rebuild.sh -skip-enrich -skip-refine  只重排数据，不出网（30 秒级）
#
# 产物：build/ip2region.xdb

set -euo pipefail
cd "$(dirname "$0")"

echo "[1/2] 编译构建工具 ..."
go build -o build/ip2region ./cmd/ip2region

echo "[2/2] 运行流水线 ..."
echo
set +e
./build/ip2region all "$@"
rc=$?
set -e

echo
if [ $rc -ne 0 ]; then
    echo "!! 重建失败，退出码 $rc"
else
    echo "重建完成，产物: build/ip2region.xdb"
fi
exit $rc
