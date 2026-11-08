#!/usr/bin/env bash
# Send every profile type from the demo app to a Grafana Pyroscope server's
# OTLP endpoint (run as a container) and check, through Pyroscope's query
# API, that CPU, heap, goroutine and contention profiles are stored with the
# demo's functions on top.
#
#   scripts/ci-pyroscope.sh [pyroscope image]
set -euo pipefail
image=${1:-grafana/pyroscope:latest}
here=$(cd "$(dirname "$0")/.." && pwd)
docker rm -f otelprofile-ci-pyroscope > /dev/null 2>&1 || true
docker run -d --name otelprofile-ci-pyroscope -p 127.0.0.1:4040:4040 "$image" > /dev/null
trap 'docker logs otelprofile-ci-pyroscope > "$here/pyroscope.log" 2>&1; docker rm -f otelprofile-ci-pyroscope > /dev/null' EXIT
for _ in $(seq 60); do curl -sf localhost:4040/ready > /dev/null && break; sleep 2; done
start=$(date +%s)
(cd "$here/examples/demo" && go run . -profiler=otel -service=ci-pyroscope -endpoint=http://localhost:4040 \
  -duration=35s -interval=10s -types=all -tracing=false > /dev/null 2>&1)
sleep 10
end=$(date +%s)
fail=0
check() { # profile type ID, function expected among the top three by self value
  curl -s -X POST localhost:4040/querier.v1.QuerierService/SelectMergeStacktraces -H 'content-type: application/json' \
    -d "{\"profileTypeID\":\"$1\",\"labelSelector\":\"{service_name=\\\"ci-pyroscope\\\"}\",\"start\":$(( (start-60)*1000 )),\"end\":$(( (end+60)*1000 )),\"format\":1}" |
  python3 -c '
import json,sys
pt,want=sys.argv[1],sys.argv[2]
fg=json.load(sys.stdin).get("flamegraph") or {}
names,total=fg.get("names",[]),int(fg.get("total",0) or 0)
self={}
for lv in fg.get("levels",[]):
    v=[int(x) for x in lv.get("values",[])]
    for i in range(0,len(v),4): self[names[v[i+3]]]=self.get(names[v[i+3]],0)+v[i+2]
top=[n for n,_ in sorted(self.items(),key=lambda x:-x[1])[:3]]
ok=total>0 and any(want in n for n in top)
print(("ok  " if ok else "FAIL"), pt, "total", total, "top", top)
sys.exit(0 if ok else 1)' "$1" "$2" || fail=1
}
check process_cpu:cpu:nanoseconds:cpu:nanoseconds main.spinLCG
check space:alloc_space:bytes:space:bytes main.allocLarge
check space:inuse_space:bytes:space:bytes main.allocLarge
check goroutine:goroutine:count:goroutine:count runtime.gopark
check contentions:delay:nanoseconds:contentions:count "sync.(*Mutex)"
exit $fail
