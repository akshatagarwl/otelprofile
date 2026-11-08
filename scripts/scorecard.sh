#!/usr/bin/env bash
# Runs the automatable scorecard checks for one round and writes evidence to
# evidence/<round>-*. The long runs (A/B, soak) are scripts/ab.sh and
# scripts/soak.sh; the quickstart is done by hand from README.md.
#
#   scripts/scorecard.sh r2
#
# Needs: a running sigiro (API $SIGIRO, OTLP/HTTP $OTLP) serving the dashboard,
# Docker for the Linux and collector checks, and ${WORK:-/tmp/otelprofile-work}/bin/profcheck.
set -uo pipefail
round=${1:?round name, e.g. r2}
SIGIRO=${SIGIRO:-http://localhost:19999}
OTLP=${OTLP:-http://localhost:14318}
here=$(cd "$(dirname "$0")/.." && pwd)
ev="$here/evidence"
mkdir -p "$ev"
cd "$here"
pass() { echo "PASS $1" | tee -a "$ev/$round-summary.txt"; }
fail() { echo "FAIL $1" | tee -a "$ev/$round-summary.txt"; }
: > "$ev/$round-summary.txt"
q() { curl -s -m 30 -X POST "$SIGIRO/v1/query" --data-binary "$1"; }

echo "== build"
go build -o bin/otlpdump ./cmd/otlpdump && go build -o bin/abcompare ./cmd/abcompare &&
  (cd examples/demo && go build -o ../../bin/demo . && go build -tags nootel,nopyro -o ../../bin/demo-none . &&
    go build -tags nopyro -o ../../bin/demo-otel . && go build -tags nootel -o ../../bin/demo-pyro .) || { fail build; exit 1; }

echo "== quality: vet, lint, race tests, fuzz"
{ go vet ./... && (cd examples/demo && go vet ./... && go vet -tags nootel,nopyro ./... && go vet -tags nopyro ./... && go vet -tags nootel ./...) && golangci-lint run ./... && (cd examples/demo && golangci-lint run ./...); } > "$ev/$round-lint.txt" 2>&1 && pass "vet+lint" || fail "vet+lint"
go test -race -count=1 ./... > "$ev/$round-tests-darwin.txt" 2>&1 && pass "tests darwin (race)" || fail "tests darwin"
{ go test -run x -fuzz FuzzEncoder -fuzztime 30s ./internal/transform && go test -run x -fuzz FuzzTransform -fuzztime 30s ./internal/transform; } > "$ev/$round-fuzz.txt" 2>&1 && pass "fuzz 2x30s" || fail "fuzz"
docker run --rm -v "$here":/src:ro -v "$(go env GOMODCACHE)":/go/pkg/mod:ro -e GOPROXY=off -e GOFLAGS=-buildvcs=false -e GOCACHE=/tmp/gocache -w /src golang:1.27-alpine \
  sh -c 'go version && go vet ./... && go test -count=1 ./...' > "$ev/$round-tests-linux-arm64.txt" 2>&1 && pass "tests linux/arm64" || fail "tests linux/arm64"

echo "== conformance: profcheck on every request"
rm -rf ${WORK:-/tmp/otelprofile-work}/otlp-dump && ./bin/otlpdump -dir ${WORK:-/tmp/otelprofile-work}/otlp-dump -duration 12s > /dev/null
ok=1; : > "$ev/$round-profcheck.txt"
for f in ${WORK:-/tmp/otelprofile-work}/otlp-dump/*.pb; do
  ${WORK:-/tmp/otelprofile-work}/bin/profcheck -check-dupes -check-orphans -check-sample-shapes "$f" >> "$ev/$round-profcheck.txt" 2>&1 || ok=0
done
[ $ok = 1 ] && pass "profcheck" || fail "profcheck"

echo "== collector otlp receiver + debug exporter"
docker rm -f otelprofile-collector > /dev/null 2>&1
docker run -d --name otelprofile-collector -p 127.0.0.1:24318:4318 \
  -v "$here/scripts/collector/config.yaml":/etc/otelcol-contrib/config.yaml \
  otel/opentelemetry-collector-contrib:latest --config /etc/otelcol-contrib/config.yaml --feature-gates=service.profilesSupport > /dev/null
sleep 4
./bin/demo -profiler=otel -service=$round-collector -endpoint=http://localhost:24318 -duration=25s -interval=10s -types=all 2>&1 | grep -v "traces export" > /dev/null
sleep 2
docker logs otelprofile-collector > "$ev/$round-collector.log" 2>&1
recs=$(grep -c '"sample records"' "$ev/$round-collector.log")
links=$(grep -A1 "Link table" "$ev/$round-collector.log" | grep -c "Trace ID: Str([0-9a-f]")
errs=$(grep -cE '\berror\b' "$ev/$round-collector.log")
echo "collector batches=$recs linked_tables=$links error_lines=$errs" | tee -a "$ev/$round-collector.log"
[ "$recs" -ge 2 ] && [ "$links" -ge 1 ] && [ "$errs" = 0 ] && pass "collector accepts ($recs batches, links present)" || fail "collector"
docker rm -f otelprofile-collector > /dev/null

echo "== sigiro: every type through OTLP and through the Pyroscope bridge"
./bin/demo -profiler=otel -service=$round-types-otel -endpoint=$OTLP -duration=35s -interval=10s -types=all > /dev/null 2>&1 &
./bin/demo -profiler=pyroscope -service=$round-types-pyro -endpoint=$OTLP -duration=35s -interval=10s -types=all > /dev/null 2>&1 &
wait
sleep 15
q "SELECT service_name, profile_type, count(*) AS rows, sum(value) AS total, count(trace_id) AS linked, count(labels) AS labelled FROM sigiro_profiles WHERE service_name IN ('$round-types-otel','$round-types-pyro') GROUP BY ALL ORDER BY 2,1" > "$ev/$round-types.json"
n=$(python3 -c "import json;r=json.load(open('$ev/$round-types.json'));print(len({x['profile_type'] for x in r if x['service_name'].endswith('otel')} & {x['profile_type'] for x in r if x['service_name'].endswith('pyro')}))")
[ "$n" = 11 ] && pass "all 11 sample types in sigiro from both paths" || fail "types ($n of 11 common)"

echo "== resource attributes and links: OTLP vs Pyroscope rows"
q "SELECT service_name, count(*) AS rows, count(trace_id) AS linked_rows, max(json_extract_string(resource_attributes, '\$.\"service.instance.id\"')) AS instance, max(json_extract_string(resource_attributes, '\$.\"host.name\"')) AS host, max(json_extract_string(resource_attributes, '\$.\"process.pid\"')) AS pid, max(json_extract_string(resource_attributes, '\$.\"telemetry.sdk.language\"')) AS sdk FROM sigiro_profiles WHERE service_name IN ('$round-types-otel','$round-types-pyro') AND profile_type = 'cpu' GROUP BY 1 ORDER BY 1" > "$ev/$round-resource-links.json"
cat "$ev/$round-resource-links.json"; echo
python3 - "$ev/$round-resource-links.json" <<'EOF' && pass "resource attrs + links (otel only)" || fail "resource attrs + links"
import json,sys
r={x['service_name']:x for x in json.load(open(sys.argv[1]))}
o=[v for k,v in r.items() if k.endswith('otel')][0]; p=[v for k,v in r.items() if k.endswith('pyro')][0]
assert o['linked_rows']>0 and o['instance'] and o['host'] and o['pid']
assert p['linked_rows']==0 and not p['instance']
EOF

echo "== dashboard screenshots"
cd e2e
since=$(( ($(date +%s)-120)*1000000 ))
T=$(q "SELECT trace_id AS t FROM sigiro_profiles WHERE service_name='$round-types-otel' AND profile_type='cpu' AND trace_id IS NOT NULL AND timestamp > make_timestamp($since) AND trace_id IN (SELECT trace_id FROM sigiro_spans WHERE span_name='query-db') GROUP BY 1 ORDER BY sum(value) DESC LIMIT 1" | python3 -c "import json,sys;r=json.load(sys.stdin);print(r[0]['t'] if r else '')")
shot() { DASH=$SIGIRO node shoot.mjs "$@" 2>&1; }
shot "/dashboard/profiles/?service=$round-types-otel&range=15m" "$ev/$round-flamegraph.png" --wait=".sigiro-flamegraph rect" > "$ev/$round-flamegraph.json"
shot "/dashboard/profiles/?service=$round-types-otel&range=15m" "$ev/$round-label-filter.png" --wait=".sigiro-flamegraph rect" '--click=[data-action="profile-label-filter"]|[data-label-option="endpoint=/slow"]' > "$ev/$round-label-filter.json"
# Diff: a settled 5-minute window (ending 6 minutes ago, so ingest has
# caught up) against the 5 minutes before it, on the longest-running
# OTLP service (a soak or A/B process), so both windows have data.
now_ms=$(( $(date +%s) * 1000 ))
DSVC=$(q "SELECT service_name AS s FROM sigiro_profiles WHERE service_name LIKE '%otel' AND profile_type='cpu' AND timestamp > make_timestamp($(( ($(date +%s)-600)*1000000 ))) GROUP BY 1 ORDER BY min(timestamp) LIMIT 1" | python3 -c "import json,sys;r=json.load(sys.stdin);print(r[0]['s'] if r else '')")
shot "/dashboard/profiles/?service=$DSVC&ptype=alloc_space&compare=previous&from=$((now_ms-660000))&to=$((now_ms-360000))" "$ev/$round-diff.png" --wait=".sigiro-flamegraph rect" > "$ev/$round-diff.json"
shot "/dashboard/profiles/?service=$round-types-pyro&range=15m&ptype=mutex_duration" "$ev/$round-pyro-mutex.png" --wait=".sigiro-flamegraph rect" > "$ev/$round-pyro-mutex.json"
if [ -n "$T" ]; then
  shot "/dashboard/traces/?trace=$T&range=15m" "$ev/$round-trace-to-profile.png" --wait="[data-trace-profiles]" '--click=[data-action="open-span-profile"]' --wait2=".sigiro-flamegraph rect" > "$ev/$round-trace-to-profile.json"
fi
cd ..
python3 - "$ev" "$round" <<'EOF' && pass "dashboard: flamegraph, label filter, diff, pyroscope data, trace->profile" || fail "dashboard"
import json,sys,os
ev,r=sys.argv[1],sys.argv[2]
def j(n):
    s=open(f"{ev}/{r}-{n}.json").read(); return json.loads(s[s.index('{'):])
f=j('flamegraph'); assert int(f['rects'])>5 and int(f['topFunctions'])>5 and not f['errors'], f
l=j('label-filter'); assert l['filters']==['endpoint=/slow'] and int(l['rects'])>0 and not l['errors'], l
d=j('diff'); assert int(d['rects'])>0 and d['totalNote'].startswith('baseline') and not d['totalNote'].startswith('baseline 0 '), d
p=j('pyro-mutex'); assert int(p['rects'])>0 and not p['errors'], p
t=j('trace-to-profile'); assert 'span=' in t['url'] and int(t['rects'])>0 and not t['errors'], t
EOF
echo; cat "$ev/$round-summary.txt"
