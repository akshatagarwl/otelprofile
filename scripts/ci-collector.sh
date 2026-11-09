#!/usr/bin/env bash
# Send every profile type, with span links, from the demo app to an
# OpenTelemetry Collector (otlp receiver, debug exporter) and check that it
# accepted them: profile batches logged, the demo's service name, a link
# table with a real trace ID, and no errors.
#
#   scripts/ci-collector.sh [collector image]
set -euo pipefail
image=${1:-otel/opentelemetry-collector-contrib:0.165.0}
here=$(cd "$(dirname "$0")/.." && pwd)
docker rm -f otelprofile-ci-collector > /dev/null 2>&1 || true
docker run -d --name otelprofile-ci-collector -p 127.0.0.1:4318:4318 \
  -v "$here/scripts/collector/config.yaml":/etc/otelcol-contrib/config.yaml \
  "$image" --config /etc/otelcol-contrib/config.yaml --feature-gates=service.profilesSupport > /dev/null
trap 'docker logs otelprofile-ci-collector > "$here/collector.log" 2>&1; docker rm -f otelprofile-ci-collector > /dev/null' EXIT
for _ in $(seq 30); do docker logs otelprofile-ci-collector 2>&1 | grep -q "Everything is ready" && break; sleep 1; done
(cd "$here" && OTELPROFILE_COMPRESSION_TEST_ENDPOINT=http://localhost:4318 \
  go test ./otlpprofilehttp -run TestCollectorCompression -v -count=1)
(cd "$here/examples/demo" && go run . -profiler=otel -service=ci-collector -endpoint=http://localhost:4318 \
  -duration=25s -interval=10s -types=all 2>&1 | grep -v "traces export" || true)
sleep 2
logs=$(docker logs otelprofile-ci-collector 2>&1)
batches=$(grep -c '"sample records"' <<< "$logs" || true)
service=$(grep -c 'service.name: Str(ci-collector)' <<< "$logs" || true)
links=$(grep -A1 "Link table" <<< "$logs" | grep -c "Trace ID: Str([0-9a-f]" || true)
errors=$(grep -cE '\berror\b' <<< "$logs" || true)
echo "batches=$batches service=$service linked_tables=$links error_lines=$errors"
[ "$batches" -ge 2 ] && [ "$service" -ge 1 ] && [ "$links" -ge 1 ] && [ "$errors" = 0 ]
