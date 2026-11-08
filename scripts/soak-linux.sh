#!/usr/bin/env bash
# The overhead soak inside a linux/arm64 container: no profiler, otelprofile
# and pyroscope-go side by side (each binary links only its own profiler),
# sending to sigiro on the host. Samples VmRSS from /proc every 10 s.
#
#   scripts/soak-linux.sh <minutes> <outdir> <prefix>
set -euo pipefail
mins=${1:-32}; out=${2:-/tmp/otelprofile-soak-linux}; prefix=${3:-lsoak}
here=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out" "$here/bin/linux"
(cd "$here/examples/demo" &&
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags nootel,nopyro -o ../../bin/linux/demo-none . &&
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags nopyro -o ../../bin/linux/demo-otel . &&
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags nootel -o ../../bin/linux/demo-pyro .)
docker rm -f otelprofile-soak-linux > /dev/null 2>&1 || true
docker run -d --name otelprofile-soak-linux -v "$here/bin/linux":/bin2:ro -v "$out":/out golang:1.27-alpine sh -c "
  ep=http://host.docker.internal:14318
  /bin2/demo-none -profiler=none -service=$prefix-none -endpoint=\$ep -duration=${mins}m -tracing=false -rps=10 -types=cpu,heap > /out/none.log 2>&1 & n=\$!
  /bin2/demo-otel -profiler=otel -service=$prefix-otel -endpoint=\$ep -duration=${mins}m -tracing=false -rps=10 -types=cpu,heap > /out/otel.log 2>&1 & o=\$!
  /bin2/demo-pyro -profiler=pyroscope -service=$prefix-pyroscope -endpoint=\$ep -duration=${mins}m -tracing=false -rps=10 -types=cpu,heap > /out/pyroscope.log 2>&1 & p=\$!
  echo t,profiler,rss_kb > /out/rss.csv
  while kill -0 \$n 2>/dev/null || kill -0 \$o 2>/dev/null || kill -0 \$p 2>/dev/null; do
    t=\$(date +%s)
    for e in none:\$n otel:\$o pyroscope:\$p; do
      r=\$(awk '/VmRSS/{print \$2}' /proc/\${e#*:}/status 2>/dev/null) && [ -n \"\$r\" ] && echo \$t,\${e%%:*},\$r >> /out/rss.csv
    done
    sleep 10
  done" > /dev/null
echo "linux soak started: $out"
