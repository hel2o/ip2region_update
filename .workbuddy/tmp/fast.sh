#!/bin/bash
UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0"
ip="$1"
c=$(curl -sS -m 15 -o /dev/null -w '%{http_code}' "https://qifu.baidu.com/api/v1/ip-portrait/brief-info?ip=${ip}" -H "User-Agent: $UA" -H "Referer: https://qifu.baidu.com/" 2>/dev/null)
echo "$c"
