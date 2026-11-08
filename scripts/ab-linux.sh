#!/usr/bin/env bash
# scripts/ab.sh inside a linux/arm64 container, sending to sigiro on the
# host: one process with both profilers (heap/goroutine/mutex/block), one
# process alternating CPU profilers in 2-minute turns, and two CPU-only
# processes for the rusage ground truth. Compare with scripts/ab-report.sh.
#
#   scripts/ab-linux.sh <minutes> <outdir> <prefix>
set -euo pipefail
mins=${1:-12}; out=${2:-/tmp/otelprofile-ab-linux}; prefix=${3:-lab}
here=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out" "$here/bin/linux"
(cd "$here/examples/demo" && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ../../bin/linux/demo .)
date +%s > "$out/start"
docker rm -f otelprofile-ab-linux > /dev/null 2>&1 || true
docker run -d --name otelprofile-ab-linux -v "$here/bin/linux":/bin2:ro -v "$out":/out golang:1.27-alpine sh -c "
  ep=http://host.docker.internal:14318
  /bin2/demo -profiler=both -memprofilerate=4096 -service=$prefix-both -endpoint=\$ep -duration=${mins}m -tracing=false -types=heap,goroutine,mutex,block > /out/both.log 2>&1 &
  /bin2/demo -profiler=alternate -service=$prefix-alt -endpoint=\$ep -duration=${mins}m -tracing=false -phase=2m > /out/alt.log 2>&1 &
  /bin2/demo -profiler=otel -service=$prefix-cpu-otel -endpoint=\$ep -duration=${mins}m -tracing=false -types=cpu > /out/cpu-otel.log 2>&1 &
  /bin2/demo -profiler=pyroscope -service=$prefix-cpu-pyroscope -endpoint=\$ep -duration=${mins}m -tracing=false -types=cpu > /out/cpu-pyro.log 2>&1 &
  wait" > /dev/null
echo "linux A/B started at $(cat "$out/start"): $out"
