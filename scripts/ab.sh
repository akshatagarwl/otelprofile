#!/usr/bin/env bash
# A/B accuracy run against sigiro: pyroscope-go (through sigiro's Pyroscope
# bridge) versus otelprofile (OTLP) on the demo workload.
#  - ab-both: one process running BOTH profilers for heap, goroutine, mutex
#    and block, so both observe the same runtime state. It samples the heap
#    every 4 KiB instead of every 512 KiB: at the default rate the demo's
#    in-use object counts rest on one or two samples (each worth ~4,096
#    small objects), so a GC landing between the two profilers' reads moved
#    whole samples and the comparison measured runtime sampling, not the
#    profilers.
#  - ab-alt: one process that alternates pyroscope-go and otelprofile CPU
#    profiling in 2-minute turns (only one can own the runtime CPU profiler
#    at a time; one process removes process-to-process variance).
#  - ab-cpu-otel / ab-cpu-pyroscope: two processes, CPU only; used for the
#    rusage ground truth (each profile against its own process's CPU time).
# Afterwards: bin/abcompare over the steady-state range (first and last
# minute dropped).
#
#   scripts/ab.sh <minutes> <outdir> [endpoint]
set -euo pipefail
mins=${1:-12}; out=${2:-/tmp/otelprofile-ab}; ep=${3:-http://localhost:14318}
mkdir -p "$out"
here=$(cd "$(dirname "$0")/.." && pwd)
start=$(date +%s)
echo "$start" > "$out/start"
nohup "$here/bin/demo" -profiler=both -memprofilerate=4096 -service=${PREFIX:-ab}-both -endpoint="$ep" -duration=${mins}m -tracing=false -types=heap,goroutine,mutex,block > "$out/both.log" 2>&1 &
nohup "$here/bin/demo" -profiler=alternate -service=${PREFIX:-ab}-alt -endpoint="$ep" -duration=${mins}m -tracing=false -phase=2m > "$out/alt.log" 2>&1 &
nohup "$here/bin/demo" -profiler=otel -service=${PREFIX:-ab}-cpu-otel -endpoint="$ep" -duration=${mins}m -tracing=false -types=cpu > "$out/cpu-otel.log" 2>&1 &
nohup "$here/bin/demo" -profiler=pyroscope -service=${PREFIX:-ab}-cpu-pyroscope -endpoint="$ep" -duration=${mins}m -tracing=false -types=cpu > "$out/cpu-pyro.log" 2>&1 &
echo "started at $start; compare after $((start + mins*60 + 30)) with:"
echo "  bin/abcompare -a ${PREFIX:-ab}-both-pyro -b ${PREFIX:-ab}-both-otel -from $((start+60)) -to $((start + mins*60 - 60))"
echo "  bin/abcompare -a ${PREFIX:-ab}-cpu-pyroscope -b ${PREFIX:-ab}-cpu-otel -from $((start+60)) -to $((start + mins*60 - 60))"
