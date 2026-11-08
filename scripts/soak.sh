#!/usr/bin/env bash
# Overhead soak: the demo workload three times side by side — no profiler,
# otelprofile, pyroscope-go — same rate, same interval, tracing off, default
# profile types (cpu + heap). Each process prints its own CPU time, peak RSS
# and allocation totals every 5 minutes and at exit.
#
#   scripts/soak.sh <minutes> <outdir> [endpoint] [types]
set -euo pipefail
mins=${1:-32}; out=${2:-/tmp/otelprofile-soak}; ep=${3:-http://localhost:14318}; types=${4:-cpu,heap}
mkdir -p "$out"
here=$(cd "$(dirname "$0")/.." && pwd)
pids=()
for p in none otel pyroscope; do
  # Each variant links only its own profiler (see examples/demo build tags),
  # as a real program would; DEMO overrides with one binary for all three.
  case $p in none) bin=demo-none ;; otel) bin=demo-otel ;; pyroscope) bin=demo-pyro ;; esac
  nohup "${DEMO:-$here/bin/$bin}" -profiler=$p -service=${PREFIX:-soak}-$p -endpoint="$ep" -duration=${mins}m \
    -tracing=false -rps=10 -types="$types" -stats=5m > "$out/$p.log" 2>&1 &
  pids+=("$p:$!")
  echo "$p pid $!"
done
# Current RSS of each process every 10 s (peak RSS alone is one spike).
(
  echo "t,profiler,rss_kb" > "$out/rss.csv"
  while :; do
    alive=0
    for e in "${pids[@]}"; do
      r=$(ps -o rss= -p "${e#*:}" 2>/dev/null | tr -d ' ')
      [ -n "$r" ] && { echo "$(date +%s),${e%%:*},$r" >> "$out/rss.csv"; alive=1; }
    done
    [ $alive = 0 ] && break
    sleep 10
  done
) > /dev/null 2>&1 &
