#!/bin/bash
N=${1:-20}
for i in $(seq 1 $N); do
  ip="183.$i.$((i%256)).$((i*7%256))"
  code=$(bash baidu_ip_probe.sh "$ip" 2>/dev/null | grep -o 'HTTP:[0-9]*' | cut -d: -f2)
  body=$(bash baidu_ip_probe.sh "$ip" 2>/dev/null | head -c 160)
  echo "[$i] ip=$ip -> ${code} | ${body}"
done
