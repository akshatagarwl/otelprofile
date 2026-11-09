#!/usr/bin/env bash
# otelprofile vs opentelemetry-ebpf-profiler on one Linux kernel (Docker):
# two copies of examples/demo run side by side for <minutes>:
#   demo-otel  profiles itself with otelprofile (all types, trace links)
#   demo-none  links no profiler
# With EBPF=1 the eBPF profiler (official otelcol-ebpf-profiler image,
# privileged, host PID namespace) profiles the whole machine at the same
# time. Everything goes through one collector (raw JSON copy in <outdir>)
# into Grafana Pyroscope on localhost:4040.
#
#   EBPF=1 HZ=97 scripts/vs-ebpf/run.sh <minutes> <outdir>
# HZ is the eBPF sampling rate (receiver default 20; Go's profiler is 100).
set -euo pipefail
mins=${1:-15}; out=${2:-/tmp/vsebpf/run}; ebpf=${EBPF:-1}
here=$(cd "$(dirname "$0")/../.." && pwd); cfg=$here/scripts/vs-ebpf
mkdir -p "$out" "$here/bin/linux"; chmod 777 "$out"
(cd "$here/examples/demo" &&
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags nootel,nopyro -o ../../bin/linux/demo-none . &&
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags nopyro -o ../../bin/linux/demo-otel .)
docker network create vsebpf > /dev/null 2>&1 || true
docker rm -f vs-hub vs-ebpf vs-demo-otel vs-demo-none vs-bpfstats > /dev/null 2>&1 || true
docker ps --format '{{.Names}}' | grep -qx vs-pyroscope ||
  docker run -d --name vs-pyroscope --network vsebpf -p 127.0.0.1:4040:4040 grafana/pyroscope:2.4.3 > /dev/null
docker run -d --name vs-hub --network vsebpf -v "$cfg/hub.yaml":/etc/hub.yaml:ro -v "$out":/out \
  otel/opentelemetry-collector-contrib:0.165.0 --config /etc/hub.yaml --feature-gates=service.profilesSupport > /dev/null
sleep 3
if [ "$ebpf" = 1 ]; then
  # kernel.bpf_stats_enabled makes the kernel count run time per BPF program.
  docker run -d --name vs-bpfstats --privileged --pid=host -v "$out":/out alpine sh -c \
    'apk add -q bpftool >/dev/null; sysctl -qw kernel.bpf_stats_enabled=1; sleep infinity' > /dev/null
  # The receiver sometimes fails its startup self-check on this kernel
  # ("system analysis helper failed ... bad address"); retry until it runs.
  for try in 1 2 3 4 5; do
    docker rm -f vs-ebpf > /dev/null 2>&1 || true
    docker run -d --name vs-ebpf --network vsebpf --privileged --pid=host -e 'PROBES=[]' -e 'EXTENSIONS=[]' \
      -e "SAMPLES_PER_SECOND=${HZ:-100}" \
      -v /sys/kernel/debug:/sys/kernel/debug -v /sys/fs/cgroup:/sys/fs/cgroup:ro -v "$cfg/ebpf.yaml":/etc/ebpf.yaml:ro \
      otel/opentelemetry-collector-ebpf-profiler:latest --config /etc/ebpf.yaml --feature-gates=+service.profilesSupport > /dev/null
    sleep 20
    [ "$(docker inspect -f '{{.State.Status}}' vs-ebpf)" = running ] && break
    echo "eBPF receiver failed to start (try $try)" >&2
  done
  docker exec vs-bpfstats bpftool -j prog show > "$out/bpf-start.json"
fi
date +%s > "$out/start"
docker run -d --name vs-demo-otel --network vsebpf -v "$here/bin/linux":/bin2:ro alpine \
  /bin2/demo-otel -profiler=otel -service=demo -endpoint=http://vs-hub:4318 -types=all -duration="${mins}m" -stats=60s > /dev/null
docker run -d --name vs-demo-none --network vsebpf -v "$here/bin/linux":/bin2:ro alpine \
  /bin2/demo-none -profiler=none -service=demo-none -endpoint=http://vs-hub:4318 -types=all -duration="${mins}m" -stats=60s > /dev/null
echo t,name,cpu_pct,mem > "$out/docker-stats.csv"
# shellcheck disable=SC2046 # the optional container name must split
while docker ps --format '{{.Names}}' | grep -q '^vs-demo-'; do
  docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' vs-demo-otel vs-demo-none vs-hub $([ "$ebpf" = 1 ] && echo vs-ebpf) 2>/dev/null |
    sed "s/^/$(date +%s),/" >> "$out/docker-stats.csv" || true
  sleep 15
done
date +%s > "$out/end"
[ "$ebpf" = 1 ] && docker exec vs-bpfstats bpftool -j prog show > "$out/bpf-end.json"
docker logs vs-demo-otel > "$out/demo-otel.log" 2>&1
docker logs vs-demo-none > "$out/demo-none.log" 2>&1
sleep 20 # let the last exports drain
[ "$ebpf" = 1 ] && docker logs vs-ebpf > "$out/ebpf.log" 2>&1
docker rm -f vs-ebpf vs-bpfstats vs-demo-otel vs-demo-none > /dev/null 2>&1 || true
echo "done: $out ($(cat "$out/start")..$(cat "$out/end"))"
