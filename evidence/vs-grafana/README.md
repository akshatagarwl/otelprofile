# Grafana comparison evidence

These files come from the synthetic fixture in `scripts/vs-grafana`. They contain no customer workloads.

- `quality.json` summarizes original pprof and received OTLP sample values, stacks, labels, and resolvable span pairs. Function values are inclusive and cannot be added across functions.
- `backend.json` records actual Pyroscope queries and the explicit HTTP 501 trace-filter limitation. `cpu` and `process_cpu` are aliases here, not additional captured CPU.
- `cost-runs.json` retains all 15 individual runs. `summary.json` contains the five-run medians and ranges. The CPU ranges overlap.
- `verified.json` records artifact equality and E2E scope. It is not a temporal attribution or overhead significance verdict.
- `environment.json` describes versions, the shared host, interpretation limits, and collection/tooling incidents. `runner-smoke.json` records a separate successful fresh-prefix run of the final runner, not used for performance results.
- `modules.txt` records the exact resolved dependency graph. Both adapters share OTel 1.47.0 and godeltaprof 0.1.12.
- `raw.tar.gz` contains 134 original pprof, OTLP, emitted-span/statistics, upload-status, and summary files. `raw-manifest.json` contains their SHA-256 hashes.

CPU/allocated-byte deltas exclude initialization and include profiler shutdown. Peak RSS covers the process lifetime. The Grafana diagnostic logger is enabled. Fixed arrival pacing means this is not a saturation or latency benchmark. In-use/goroutine totals sum snapshots and are retained only for transport/backend equality, not live-memory claims. Span IDs resolve, but sample/span timestamp overlap was not checked.

The first wall-deadline probes were discarded after discovering unequal completed work. The measured series uses a fixed iteration count and equal 300-request cost runs. An edit to the running shell file broke automatic postprocessing after the 15th completed output. The workload binary was not changed. The final analyzer, backend queries, summary, and verifier were then run explicitly and passed. The current runner also rejects reused output directories and gives each run a unique service prefix. It subsequently passed a complete smoke with shorter probe durations, all five rounds, and all raw/backend checks.

## Verify the retained raw inputs offline

From the repository root:

```sh
out=$(mktemp -d /tmp/vsgrafana-audit.XXXXXX)
tar -xzf evidence/vs-grafana/raw.tar.gz -C "$out"
(cd scripts/vs-grafana/workload && go build -o "$out/analyze" .)
"$out/analyze" analyze "$out" > "$out/recomputed.json"
cmp "$out/recomputed.json" evidence/vs-grafana/quality.json
python3 scripts/vs-grafana/summarize.py "$out" > "$out/recomputed-cost.json"
cmp "$out/recomputed-cost.json" evidence/vs-grafana/summary.json
python3 scripts/vs-grafana/verify.py "$out"
```

This rechecks recorded backend results, not a live backend. To requery or recollect, use `scripts/vs-grafana/README.md`.

A fresh same-family, context-isolated reviewer inspected measurement validity. It was not cross-family independent. Its CPU-window, RSS, snapshot-sum, and temporal-attribution limitations are incorporated in the report.
