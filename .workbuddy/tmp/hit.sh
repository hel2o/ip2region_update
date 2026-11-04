#!/bin/bash
# 单次请求：输出 "HTTPCODE code message"
IP="$1"
TOKEN='1793764940454_1793792627320_cSZdR3jszY/jyIUuf183xpueM4pkqLg0ylOEyoJ4hooqs5/GMtUND1J3wZUa28r9/sF+1foHgVX/VWSzWVwyXdp+CNPuc+wTWX1bucZh6gk3tqVh28WcNDZcbarVJiK/QQTnCoUTahdGKjCN/kgR6hv/TwsKDcMxWkcpokc6i3d3/jJO7jAGStrd5s/M1wgG6f7dsewef/30Y2R5aTAA6viAhDBx4bO/t5l23n53scxEKtuX/ZKGZxxNKW/LUlWwmy+Vng6mLkVmn/Gtgl3JnFJhdxSXHTVCnin70UA6xe5b2Giw7bxgSbNhMjwqo75ecWnupfo1DuIDu4+APaXhmOlp5p9QPwDb9R2r3dSyXhlgp2vD46SMktIW5nTqKi4073Ay0hrexDcMQEFvRgDs4SnU+SFdWgr9bI9nORWDT0A5nyJTLLRqKszp1GA6w3z3'
COOKIE='MAWEBCUID=web_JfKSiBXROOsxNEvuVCNAdUOjfWZypGDNpMDwKAzbpvmiMyAHAf; BAIDUID=34A4E228F42F9C16EC2F704449FBE73B:FG=1; BDUSS=m9VYTNIRTVmZlZBNGZXWUhacnNpc0Jta2JRTkRSNVI4akY4c3I4WnNZNVRyQ1pxRVFBQUFBJCQAAAAAAAAAAAEAAADsjOs-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAFMf~2lTH~9pe; BDUSS_BFESS=m9VYTNIRTVmZlZBNGZXWUhacnNpc0Jta2JRTkRSNVI4akY4c3I4WnNZNVRyQ1pxRVFBQUFBJCQAAAAAAAAAAAEAAADsjOs-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAFMf~2lTH~9pe; MCITY=-257%3A; PSTM=1791426579; BIDUPSID=8DB1FEC240F4A500C142CCBF776BD5A7; HMACCOUNT=85408BF53A01E887; bce-sessionid=001c3216b1a37c14512b89ed86c7cb5ee6e; accountId=c3a6b726544d47c287de1651bfc90b47; bce-passport-stoken=b6cc4e7b65f377c9d2fdb4ccdb9a950c69064655929b626f59f002223f5def09; bce-session=c3a6b726544d47c287de1651bfc90b471fc29bddaa9c4efca2f919f621ce6312|b8ed4f3df801a2bd533fb2ee11d7e483; loginUserId=1055624428; bce-login-accountid=c3a6b726544d47c287de1651bfc90b47; BDORZ=B490B5EBF6F3CD402E515D22BCDA1598; ZFY=QpH7TCiTaSY7fvnbusVFIsLLK6:B7oJfJCfq6ithxxXo:C; PSINO=6'

out=$(curl -sS -m 20 -o /tmp/bd_$$.json -w '%{http_code}' \
  --url "https://qifu.baidu.com/api/v1/ip-portrait/brief-info?ip=${IP}" \
  -H "Accept: */*" \
  -H "Content-Type: application/json" \
  -H "Acs-Token: ${TOKEN}" \
  -H "Referer: https://qifu.baidu.com/?activeKey=SEARCH_IP&trace=apistore_ip_aladdin&activeId=SEARCH_IP_ADDRESS&ip=${IP}" \
  -H "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0" \
  -H "X-Request-By: RestClient" \
  -b "${COOKIE}" 2>/dev/null)

inner=$(grep -o '"code":[0-9]*' /tmp/bd_$$.json 2>/dev/null | head -1 | cut -d: -f2)
msg=$(grep -o '"message":"[^"]*"' /tmp/bd_$$.json 2>/dev/null | head -1 | cut -d'"' -f4)
rm -f /tmp/bd_$$.json
echo "http=${out:-ERR} code=${inner:-?} msg=${msg:--}"
