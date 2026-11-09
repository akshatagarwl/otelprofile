#!/usr/bin/env bash
# Short eBPF-only checks: start the eBPF profiler, run the given demo
# processes (each "<name>:<docker run flags>"), then compare each process's
# eBPF CPU total with its own rusage CPU time.
#
#   scripts/vs-ebpf/probe.sh <seconds> <outdir> <name>[:<docker flags>] ...
# e.g. scripts/vs-ebpf/probe.sh 180 /tmp/vsebpf/e1 e1-none
#      scripts/vs-ebpf/probe.sh 180 /tmp/vsebpf/e3 e3-a:--pid=host e3-b:--pid=host
# A name containing "otel" runs with otelprofile on (all types).
set -euo pipefail
secs=$1; out=$2; shift 2
here=$(cd "$(dirname "$0")/../.." && pwd); cfg=$here/scripts/vs-ebpf
mkdir -p "$out" "$here/bin/probe"; chmod 777 "$out"; rm -f "$out/profiles.jsonl"
docker rm -f vs-hub vs-ebpf > /dev/null 2>&1 || true
docker run -d --name vs-hub --network vsebpf -v "$cfg/hub.yaml":/etc/hub.yaml:ro -v "$out":/out \
  otel/opentelemetry-collector-contrib:0.165.0 --config /etc/hub.yaml --feature-gates=service.profilesSupport > /dev/null
sleep 3
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
names=()
for spec in "$@"; do
  name=${spec%%:*}; flags=""; [ "$spec" != "$name" ] && flags=${spec#*:}
  names+=("$name")
  if [[ $name == *otel* ]]; then
    cp "$here/bin/linux/demo-otel" "$here/bin/probe/$name"; mode="-profiler=otel -types=all"
  else
    cp "$here/bin/linux/demo-none" "$here/bin/probe/$name"; mode="-profiler=none"
  fi
  docker rm -f "vs-$name" > /dev/null 2>&1 || true
  # shellcheck disable=SC2086
  docker run -d --name "vs-$name" --network vsebpf $flags -v "$here/bin/probe":/bin2:ro alpine \
    /bin2/$name $mode -service=$name -endpoint=http://vs-hub:4318 -duration=${secs}s > /dev/null
done
sleep $((secs + 5))
sleep 20
for n in "${names[@]}"; do docker logs "vs-$n" 2>&1 | grep '"exit"' | sed "s/^/$n /"; docker rm -f "vs-$n" > /dev/null; done > "$out/rusage.txt"
docker logs vs-ebpf > "$out/ebpf.log" 2>&1
docker rm -f vs-ebpf > /dev/null
python3 - "$out" "${names[@]}" <<'EOF'
import collections, json, re, sys
out, names = sys.argv[1], sys.argv[2:]
cpu = {l.split()[0]: json.loads(l.split(" ", 1)[1])["cpu_s"] for l in open(f"{out}/rusage.txt")}
ebpf = collections.Counter(); otel = collections.Counter()
for line in open(f"{out}/profiles.jsonl"):
    d = json.loads(line); dic = d["dictionary"]; st = dic["stringTable"]
    for rp in d["resourceProfiles"]:
        res = {st[a["keyStrindex"]]: a["value"] for a in rp["resource"].get("attributes", [])}
        for sp in rp["scopeProfiles"]:
            is_ebpf = "ebpf" in sp["scope"].get("name", "")
            for p in sp["profiles"]:
                if st[p["sampleType"].get("typeStrindex", 0)] != "samples":
                    continue
                period = int(p.get("period", 0) or 0)
                for s in p.get("samples", []):
                    if is_ebpf:
                        sa = {st[dic["attributeTable"][i]["keyStrindex"]]: dic["attributeTable"][i]["value"] for i in s.get("attributeIndices", [])}
                        e = {**res, **sa}.get("process.executable.name", {})
                        e = st[e["stringValueStrindex"]] if "stringValueStrindex" in e else ""
                        ebpf[e] += len(s.get("timestampsUnixNano", [])) * period / 1e9
                    else:
                        svc = res.get("service.name", {})
                        svc = st[svc["stringValueStrindex"]] if "stringValueStrindex" in svc else svc.get("stringValue", "")
                        otel[svc] += int(s["values"][0]) / 100  # 100 Hz
for n in names:
    r = {"name": n, "rusage_cpu_s": cpu.get(n), "ebpf_cpu_s": round(ebpf[n], 2), "ebpf_vs_rusage": round(ebpf[n] / cpu[n], 3) if cpu.get(n) else None}
    if otel[n]:
        r["otelprofile_cpu_s"] = round(otel[n], 2); r["otelprofile_vs_rusage"] = round(otel[n] / cpu[n], 3)
    print(json.dumps(r))
EOF
