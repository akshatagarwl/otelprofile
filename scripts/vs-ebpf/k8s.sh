#!/usr/bin/env bash
# The eBPF profiler as a Kubernetes DaemonSet (kind), profiling a demo pod.
# Runs the DaemonSet twice: with the receiver's default PID namespace mode
# and with pid_namespace_translation_mode: auto (kind nodes are containers,
# so a hostPID pod is not in the kernel's root PID namespace).
#
#   scripts/vs-ebpf/k8s.sh <outdir> [seconds]
# Needs kind, kubectl and Docker.
set -euo pipefail
out=$1; secs=${2:-150}
here=$(cd "$(dirname "$0")/../.." && pwd); cfg=$here/scripts/vs-ebpf
ebpf_image=${EBPF_IMAGE:-otel/opentelemetry-collector-ebpf-profiler:0.165.0}
mkdir -p "$out"
arch=$(docker version -f '{{.Server.Arch}}')
(cd "$here/examples/demo" && GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -tags nootel,nopyro -o "$out/k8sdemo" .)
printf 'FROM alpine\nCOPY k8sdemo /k8sdemo\nENTRYPOINT ["/k8sdemo"]\n' > "$out/Dockerfile"
docker build -q -t vs-k8sdemo:1 "$out" > /dev/null
kind get clusters 2>/dev/null | grep -qx vsebpf || kind create cluster --name vsebpf --wait 120s
kind load docker-image vs-k8sdemo:1 --name vsebpf
for img in "$ebpf_image" otel/opentelemetry-collector-contrib:0.165.0; do
  docker pull -q "$img" > /dev/null; kind load docker-image "$img" --name vsebpf
done
node=vsebpf-control-plane
kubectl create configmap hub --from-file=hub.yaml="$cfg/hub.yaml" --dry-run=client -o yaml | kubectl apply -f -

for mode in default auto; do
  d=$out/$mode; mkdir -p "$d"
  docker exec $node sh -c 'rm -rf /tmp/hub && mkdir -p /tmp/hub && chmod 777 /tmp/hub'
  if [ $mode = auto ]; then pidns="pid_namespace_translation_mode: auto"; else pidns=""; fi
  kubectl apply -f - <<YAML
apiVersion: v1
kind: Pod
metadata: {name: hub, labels: {app: hub}}
spec:
  containers:
  - name: hub
    image: otel/opentelemetry-collector-contrib:0.165.0
    args: [--config, /etc/hub/hub.yaml, --feature-gates=service.profilesSupport]
    volumeMounts: [{name: cfg, mountPath: /etc/hub}, {name: out, mountPath: /out}]
  volumes:
  - {name: cfg, configMap: {name: hub}}
  - {name: out, hostPath: {path: /tmp/hub}}
---
apiVersion: v1
kind: Service
metadata: {name: hub}
spec:
  selector: {app: hub}
  ports: [{name: grpc, port: 4317}, {name: http, port: 4318}]
---
apiVersion: v1
kind: ConfigMap
metadata: {name: ebpf}
data:
  config.yaml: |
    receivers:
      profiling:
        samples_per_second: 97
        $pidns
    exporters:
      otlp_grpc:
        endpoint: hub:4317
        tls: {insecure: true}
    service:
      pipelines:
        profiles: {receivers: [profiling], exporters: [otlp_grpc]}
---
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: ebpf-profiler}
spec:
  selector: {matchLabels: {app: ebpf-profiler}}
  template:
    metadata: {labels: {app: ebpf-profiler}}
    spec:
      hostPID: true
      containers:
      - name: profiler
        image: $ebpf_image
        args: [--config, /etc/ebpf/config.yaml, --feature-gates=+service.profilesSupport]
        securityContext: {privileged: true}
        volumeMounts:
        - {name: cfg, mountPath: /etc/ebpf}
        - {name: debugfs, mountPath: /sys/kernel/debug}
        - {name: cgroup, mountPath: /sys/fs/cgroup, readOnly: true}
      volumes:
      - {name: cfg, configMap: {name: ebpf}}
      - {name: debugfs, hostPath: {path: /sys/kernel/debug}}
      - {name: cgroup, hostPath: {path: /sys/fs/cgroup}}
YAML
  kubectl wait --for=condition=Ready pod/hub --timeout=120s
  sleep 30
  kubectl get pods -l app=ebpf-profiler -o wide > "$d/profiler-pods.txt"
  kubectl logs -l app=ebpf-profiler --tail=-1 > "$d/ebpf-start.log" 2>&1 || true
  kubectl run k8sdemo --image=vs-k8sdemo:1 --restart=Never --image-pull-policy=Never -- \
    -profiler=none -service=k8sdemo -endpoint=http://hub:4318 -tracing=false -duration="${secs}s"
  kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/k8sdemo --timeout=$((secs + 120))s || true
  sleep 20
  kubectl logs k8sdemo | grep '"exit"' | sed 's/^/k8sdemo /' > "$d/rusage.txt"
  kubectl get pods -l app=ebpf-profiler -o wide >> "$d/profiler-pods.txt"
  kubectl logs -l app=ebpf-profiler --tail=-1 --previous > "$d/ebpf-previous.log" 2>&1 || true
  kubectl logs -l app=ebpf-profiler --tail=-1 > "$d/ebpf.log" 2>&1 || true
  docker cp $node:/tmp/hub/. "$d/" 2>/dev/null || true
  touch "$d/profiles.jsonl"
  kubectl delete pod k8sdemo hub --wait=true > /dev/null
  kubectl delete daemonset ebpf-profiler --wait=true > /dev/null
  kubectl delete service hub configmap ebpf > /dev/null
  echo "== $mode"; cat "$d/profiler-pods.txt"; grep -o 'Error: .*' "$d"/ebpf*.log | head -2 || true
  python3 "$cfg/probe-report.py" "$d" k8sdemo | tee "$d/report.jsonl"
done
kind delete cluster --name vsebpf
