#!/bin/bash
# 并发压测：只发请求，输出 http code + body 里的 code
source ./baidu_ip_probe_env.sh 2>/dev/null

IP="$1"
TOKEN="$ACS_TOKEN"
COOKIE="$CK"

curl -sS -o /tmp/bd_body_$$.json -w '%{http_code}' \
  --url "https://qifu.baidu.com/api/v1/ip-portrait/brief-info?ip=${IP}" \
  -H "Accept: */*" \
  -H "Content-Type: application/json" \
  -H "Acs-Token: ${TOKEN}" \
  -H "Referer: https://qifu.baidu.com/?activeKey=SEARCH_IP&trace=apistore_ip_aladdin&activeId=SEARCH_IP_ADDRESS&ip=${IP}" \
  -H "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0" \
  -H "X-Request-By: RestClient" \
  -b "${COOKIE}" 2>/dev/null
echo -n " "
head -c 200 /tmp/bd_body_$$.json | tr -d '\n'
echo ""
rm -f /tmp/bd_body_$$.json
