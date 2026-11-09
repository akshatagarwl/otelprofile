#!/usr/bin/env bash
# Every eBPF comparison probe, in sequence, on the local Docker host. Used by
# .github/workflows/vs-ebpf.yml on stock Ubuntu runners (x86-64 and arm64).
#
#   scripts/vs-ebpf/ci.sh <outdir> [seconds per rate/overhead probe]
set -uo pipefail
root=$1; s=${2:-120}
p=$(dirname "$0")/probe.sh
mkdir -p "$root"
{ uname -a; nproc; grep PRETTY /etc/os-release; ls -la /sys/kernel/btf/vmlinux; go version; } > "$root/env.txt" 2>&1
step() { local dir=$1; shift; echo "::group::$dir"; "$@" || echo "probe $dir failed" | tee -a "$root/failures.txt"; echo "::endgroup::"; }

# 1. Both profilers on the same workload, eBPF at 97 Hz.
step quality env HZ=97 "$p" 300 "$root/quality" qotel qnone
# 2. The off-CPU probe.
step offcpu env HZ=97 OFFCPU=1 "$p" "$s" "$root/offcpu" offnone
# 3. Sampling rate vs request timing: two identical processes per probe.
for arr in ticker poisson; do
  for hz in 20 100 97; do
    step "rate-$arr-$hz" env ARRIVALS=$arr HZ=$hz "$p" "$s" "$root/rate-$arr-$hz" "${arr:0:1}${hz}a" "${arr:0:1}${hz}b"
  done
done
# 4. Cost inside the profiled process: the same process alone, no eBPF vs
#    eBPF at 97 Hz vs eBPF at the default 20 Hz, three rounds.
for r in 1 2 3; do
  step "ovh-off-$r" env EBPF=0 "$p" "$s" "$root/ovh-off-$r" "ovoff$r"
  step "ovh-97-$r" env HZ=97 "$p" "$s" "$root/ovh-97-$r" "ov97n$r"
  step "ovh-20-$r" env HZ=20 "$p" "$s" "$root/ovh-20-$r" "ov20n$r"
done
# 5. OBI publishes trace context; the profiler links samples to it.
step obi env OBI=1 HZ=97 "$p" 180 "$root/obi" obinone
python3 "$(dirname "$0")/ci-summary.py" "$root" | tee "$root/summary.md"
