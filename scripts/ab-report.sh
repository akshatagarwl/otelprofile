#!/usr/bin/env bash
# Compare an A/B run started by scripts/ab.sh: per-type totals and top-20
# stack shares (bin/abcompare), plus CPU accuracy against each process's own
# rusage CPU, which is the ground truth two separate processes can share.
#
#   scripts/ab-report.sh <prefix> <outdir> <round>
set -uo pipefail
prefix=$1; out=$2; round=$3
here=$(cd "$(dirname "$0")/.." && pwd); cd "$here"
SIGIRO=${SIGIRO:-http://localhost:19999}
s=$(cat "$out/start")
ok=0
./bin/abcompare -a $prefix-both-pyro -b $prefix-both-otel -from $((s+60)) -to $((s+660)) > evidence/$round-ab-sameprocess.txt || ok=1
# Goroutines without excluding the profilers' own goroutines: informational.
./bin/abcompare -exclude-profiler-goroutines=false -a $prefix-both-pyro -b $prefix-both-otel -from $((s+60)) -to $((s+660)) -types goroutines > evidence/$round-ab-goroutines-raw.txt
# CPU accuracy: the alternating process, over the whole run so both
# profilers get their three 2-minute turns. Rule (user, Nov 8): linux must
# pass in full; darwin must pass with the HTTP response write-syscall path
# left out, and the full darwin comparison is kept for information.
# OS=linux|darwin selects the rule (default darwin).
if [ "${OS:-darwin}" = linux ]; then
  ./bin/abcompare -a $prefix-alt-pyro -b $prefix-alt-otel -from $s -to $((s+780)) -types cpu,samples > evidence/$round-ab-cpu.txt || ok=1
else
  ./bin/abcompare -exclude-http-write-syscall -a $prefix-alt-pyro -b $prefix-alt-otel -from $s -to $((s+780)) -types cpu,samples > evidence/$round-ab-cpu.txt || ok=1
  ./bin/abcompare -a $prefix-alt-pyro -b $prefix-alt-otel -from $s -to $((s+780)) -types cpu,samples > evidence/$round-ab-cpu-full-info.txt
fi
# Two separate processes: reported, not judged (their CPU use differs).
./bin/abcompare -a $prefix-cpu-pyroscope -b $prefix-cpu-otel -from $((s+60)) -to $((s+660)) > evidence/$round-ab-cpu-separate.txt
: > evidence/$round-ab-cpu-vs-rusage.txt
for pair in cpu-otel:$prefix-cpu-otel cpu-pyro:$prefix-cpu-pyroscope; do
  f=${pair%%:*}; svc=${pair#*:}
  r=$(grep -h '"exit"' "$out/$f.log" | python3 -c "import json,sys; print(json.load(sys.stdin)['cpu_s'])")
  p=$(curl -s -X POST "$SIGIRO/v1/query" --data-binary "select sum(value)/1e9 as s from sigiro_profiles where service_name='$svc' and profile_type='cpu'" | python3 -c "import json,sys; print(json.load(sys.stdin)[0]['s'])")
  echo "$svc rusage_cpu_s=$r profiled_cpu_s=$p ratio=$(python3 -c "print(round($p/$r,3))")" >> evidence/$round-ab-cpu-vs-rusage.txt
done
python3 - evidence/$round-ab-cpu-vs-rusage.txt <<'PY' || ok=1
import sys,re
r=[float(re.search(r'ratio=([\d.]+)',l).group(1)) for l in open(sys.argv[1])]
print("cpu/rusage ratios", r, "difference", round(abs(r[0]-r[1]),3))
assert abs(r[0]-r[1]) <= 0.03, "CPU accuracy differs by more than 3 points"
PY
cat evidence/$round-ab-sameprocess.txt evidence/$round-ab-cpu.txt evidence/$round-ab-cpu-vs-rusage.txt
exit $ok
