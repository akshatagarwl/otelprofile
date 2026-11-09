#!/usr/bin/env bash
# One measured run on the local Docker host: start the eBPF profiler, run the
# given demo processes, then compare what each profiler saw with each
# process's own rusage CPU time (scripts/vs-ebpf/probe-report.py).
#
#   scripts/vs-ebpf/probe.sh <seconds> <outdir> <name>[:<docker flags>] ...
#
# A name containing "otel" runs with otelprofile on (all types); any other
# name links no profiler. Executable names are the process names the eBPF
# profiler reports, so keep them short and unique.
#
# Environment:
#   HZ=97          eBPF samples per second (the receiver's default is 20)
#   EBPF=1         0 runs the demos with no eBPF profiler (overhead baseline)
#   ARRIVALS=ticker  demo request timing: ticker | poisson
#   OFFCPU=0       1 enables the receiver's off-CPU probe
#   OBI=0          1 also runs OpenTelemetry eBPF Instrumentation on the demos
#                  (executables matching OBI_TARGET, default */obi*) with
#                  trace-context sharing on, and the profiler reads it
#   PIDNS=auto     the receiver's pid_namespace_translation_mode
set -euo pipefail
secs=$1; out=$2; shift 2
here=$(cd "$(dirname "$0")/../.." && pwd); cfg=$here/scripts/vs-ebpf
ebpf=${EBPF:-1}; hz=${HZ:-97}; arrivals=${ARRIVALS:-ticker}; offcpu=${OFFCPU:-0}; obi=${OBI:-0}
obi_image=${OBI_IMAGE:-otel/ebpf-instrument:v1.0.0}
ebpf_image=${EBPF_IMAGE:-otel/opentelemetry-collector-ebpf-profiler:0.165.0}
mkdir -p "$out" "$here/bin/probe"; chmod 777 "$out"; rm -f "$out"/*.jsonl "$out/rusage.txt"

arch=$(docker version -f '{{.Server.Arch}}')
if [ ! -x "$here/bin/linux-$arch/demo-none" ] || [ "${REBUILD:-0}" = 1 ]; then
  (cd "$here/examples/demo" &&
    GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -tags nootel,nopyro -o "../../bin/linux-$arch/demo-none" . &&
    GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -tags nopyro -o "../../bin/linux-$arch/demo-otel" .)
fi

docker network create vsebpf > /dev/null 2>&1 || true
docker rm -f vs-hub vs-ebpf vs-bpfstats vs-obi > /dev/null 2>&1 || true
docker run -d --name vs-hub --network vsebpf -v "$cfg/hub.yaml":/etc/hub.yaml:ro -v "$out":/out \
  otel/opentelemetry-collector-contrib:0.165.0 --config /etc/hub.yaml --feature-gates=service.profilesSupport > /dev/null
sleep 3

if [ "$ebpf" = 1 ]; then
  # kernel.bpf_stats_enabled makes the kernel count run time per BPF program.
  docker run -d --name vs-bpfstats --privileged --pid=host alpine sh -c \
    'apk add -q bpftool >/dev/null; sysctl -qw kernel.bpf_stats_enabled=1; sleep infinity' > /dev/null
  probes='[]'; exts='[]'
  [ "$offcpu" = 1 ] && probes='[offcpu]' && exts='[offcpu]'
  # The receiver sometimes fails its startup self-check ("system analysis
  # helper failed ... bad address"); retry until it runs.
  for try in 1 2 3 4 5; do
    docker rm -f vs-ebpf > /dev/null 2>&1 || true
    docker run -d --name vs-ebpf --network vsebpf --privileged --pid=host \
      -e "PROBES=$probes" -e "EXTENSIONS=$exts" -e "SAMPLES_PER_SECOND=$hz" -e "PIDNS=${PIDNS:-auto}" \
      -e "OBI_PROCESS_CTX=$([ "$obi" = 1 ] && echo true || echo false)" \
      -v /sys/kernel/debug:/sys/kernel/debug -v /sys/fs/cgroup:/sys/fs/cgroup:ro -v /sys/fs/bpf:/sys/fs/bpf \
      -v "$cfg/ebpf.yaml":/etc/ebpf.yaml:ro \
      "$ebpf_image" --config /etc/ebpf.yaml --feature-gates=+service.profilesSupport > /dev/null
    sleep 20
    [ "$(docker inspect -f '{{.State.Status}}' vs-ebpf)" = running ] && break
    echo "eBPF receiver failed to start (try $try): $(docker logs vs-ebpf 2>&1 | grep -o 'Error: .*' | head -1)" | tee -a "$out/starts.txt" >&2
  done
  echo "started on try $try" >> "$out/starts.txt"
  sleep 5
  docker exec vs-bpfstats bpftool -j prog show > "$out/bpf-start.json" 2>/dev/null || true
fi

names=()
for spec in "$@"; do names+=("${spec%%:*}"); done
if [ "$obi" = 1 ]; then
  docker run -d --name vs-obi --network vsebpf --privileged --pid=host -v /sys/fs/bpf:/sys/fs/bpf \
    -v /sys/kernel/debug:/sys/kernel/debug \
    -e OTEL_EBPF_AUTO_TARGET_EXE="${OBI_TARGET:-*/obi*}" -e OTEL_EBPF_BPF_POPULATE_TRACE_CONTEXT=true \
    -e OTEL_EXPORTER_OTLP_ENDPOINT=http://vs-hub:4318 -e OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
    -e OTEL_EBPF_TRACE_PRINTER=disabled \
    "$obi_image" > /dev/null
  sleep 15
fi

for spec in "$@"; do
  name=${spec%%:*}; flags=""; [ "$spec" != "$name" ] && flags=${spec#*:}
  if [[ $name == *otel* ]]; then
    cp "$here/bin/linux-$arch/demo-otel" "$here/bin/probe/$name"; mode="-profiler=otel -types=all"
  else
    cp "$here/bin/linux-$arch/demo-none" "$here/bin/probe/$name"; mode="-profiler=none"
  fi
  # With OBI the demo's own tracing is off, so every span comes from OBI.
  tracing=true; [ "$obi" = 1 ] && tracing=false
  docker rm -f "vs-$name" > /dev/null 2>&1 || true
  # shellcheck disable=SC2086 # mode and flags are word lists
  docker run -d --name "vs-$name" --network vsebpf $flags -v "$here/bin/probe":/bin2:ro alpine \
    /bin2/$name $mode -service=$name -endpoint=http://vs-hub:4318 -duration=${secs}s \
    -arrivals=$arrivals -tracing=$tracing > /dev/null
done
sleep $((secs + 5))
[ "$ebpf" = 1 ] && { docker exec vs-bpfstats bpftool -j prog show > "$out/bpf-end.json" 2>/dev/null || true; }
sleep 20 # let the last exports drain
for n in "${names[@]}"; do docker logs "vs-$n" 2>&1 | grep '"exit"' | sed "s/^/$n /"; docker rm -f "vs-$n" > /dev/null; done > "$out/rusage.txt"
for c in vs-ebpf vs-obi; do docker logs $c > "$out/${c#vs-}.log" 2>&1 || true; done
docker rm -f vs-ebpf vs-bpfstats vs-obi vs-hub > /dev/null 2>&1 || true
python3 "$cfg/probe-report.py" "$out" "${names[@]}" | tee "$out/report.jsonl"
if [ -f "$out/bpf-end.json" ] && [ -f "$out/bpf-start.json" ]; then
  python3 - "$out" <<'EOF' | tee "$out/bpf-stats.json"
import json, sys
out = sys.argv[1]
a = {p["id"]: p for p in json.load(open(f"{out}/bpf-start.json"))}
for p in json.load(open(f"{out}/bpf-end.json")):
    if p.get("name") == "native_tracer_entry":
        s = a.get(p["id"], {})
        t = p.get("run_time_ns", 0) - s.get("run_time_ns", 0); c = p.get("run_cnt", 0) - s.get("run_cnt", 0)
        print(json.dumps({"program": p["name"], "run_time_ns": t, "run_cnt": c, "ns_per_run": round(t / max(c, 1))}))
EOF
fi
