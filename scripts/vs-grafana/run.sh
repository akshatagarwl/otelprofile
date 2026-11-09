#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
check_disk() { python3 -c 'import shutil; assert shutil.disk_usage("/").free >= 10 * 1024**3, "free disk below 10 GiB"'; }
check_disk
out=${1:-/tmp/vsgrafana}
if [[ -e "$out/versions.txt" ]]; then echo "output directory already contains a run" >&2; exit 1; fi
mkdir -p "$out/bin" "$out/uploads"; chmod 700 "$out"
prefix=${PREFIX:-vsgrafana-$(date +%s)}
printf '%s\n' "$prefix" > "$out/prefix"
(cd "$root/scripts/vs-grafana/workload" && go build -o "$out/bin/workload" .)
{
  date -u
  go version
  uname -a
  uptime
  git -C "$root" rev-parse HEAD
  (cd "$root/scripts/vs-grafana/workload" && go list -m all)
} > "$out/versions.txt"
if ! docker inspect vsgrafana-pyro >/dev/null 2>&1; then
  docker run -d --name vsgrafana-pyro -p 127.0.0.1:14040:4040 grafana/pyroscope:2.4.3 > "$out/container.id"
fi
for _attempt in $(seq 1 90); do
  if curl --max-time 2 -fsS http://127.0.0.1:14040/ready >/dev/null 2>&1; then break; fi
  sleep 2
done
curl --max-time 2 -fsS http://127.0.0.1:14040/ready >/dev/null
python3 "$root/scripts/vs-grafana/receive.py" --out "$out/uploads" > "$out/receive.log" 2>&1 &
receiver=$!
trap 'kill "$receiver" 2>/dev/null || true' EXIT
sleep 1
kill -0 "$receiver"
for spec in otel:default grafana:default grafana:all; do
  check_disk
  mode=${spec%:*}; scope=${spec#*:}; name="quality-$mode-$scope"
  "$out/bin/workload" -mode="$mode" -scope="$scope" -service="$prefix-$mode-$scope" \
    -duration="${QUALITY_DURATION:-90s}" -types=all -endpoint=http://127.0.0.1:14041 \
    -capture="$out/$name" -out="$out/$name.json" > "$out/$name.log" 2>&1
done
for round in $(seq 1 "${ROUNDS:-5}"); do
  if (( round % 2 )); then order='none otel grafana'; else order='grafana otel none'; fi
  for mode in $order; do
    check_disk
    "$out/bin/workload" -mode="$mode" -scope=all -matched \
      -service="$prefix-cost-$mode" -duration="${COST_DURATION:-30s}" \
      -endpoint=http://127.0.0.1:14041 -out="$out/cost-$round-$mode.json" \
      > "$out/cost-$round-$mode.log" 2>&1
  done
done
curl -fsS http://127.0.0.1:14040/ready > "$out/ready.txt"
python3 "$root/scripts/vs-grafana/summarize.py" "$out" > "$out/summary.json"
"$out/bin/workload" analyze "$out" > "$out/quality.json"
python3 "$root/scripts/vs-grafana/backend.py" "$out" > "$out/backend.json"
python3 "$root/scripts/vs-grafana/verify.py" "$out" > "$out/verified.json"
