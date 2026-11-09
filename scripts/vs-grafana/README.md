# Reproduce the Grafana comparison

Prerequisites are Go 1.27+, Python 3, Docker, and at least 10 GiB free disk. The workload statistics use Unix `getrusage`. macOS arm64 was exercised here. Linux is supported by the fixture but was not exercised in this comparison.

From the repository root, run the complete collection and five alternating cost rounds:

```sh
bash scripts/vs-grafana/run.sh /tmp/vsgrafana-$(date +%s)
```

Allow about 13 minutes, plus image startup. The runner creates or reuses its own `vsgrafana-pyro` container on port 14040 and a temporary local recording proxy on 14041. It does not touch other containers. Each run chooses a new service prefix to avoid mixing stored results. The output directory must be new. Use `PREFIX` only if you also ensure the prefix has no prior backend data.

The runner stops its proxy on exit and leaves its backend available for screenshots. `QUALITY_DURATION`, `COST_DURATION`, and `ROUNDS` change the durations and repetition count. The summary rejects fewer than five cost runs or unequal request counts. There are no automatic destructive cleanup commands.

## Inspect the results

The output directory contains:

- `quality-*.json` and `quality-*/*.pprof` for completed work, locally emitted spans, and original pprof.
- `uploads/*.otlp` and `uploads/uploads.jsonl` for received wire bodies and successful or failed uploads.
- `quality.json` for decoded stack, label, and link summaries. The analyzer reads actual pprof and OTLP, not UI text.
- `backend.json` for Pyroscope profile types, span and dynamic-label filters, CPU totals, and heap snapshot sums.
- `cost-*.json` and `summary.json` for every measured run, medians, and ranges.
- `verified.json` for raw/OTLP/backend equality and observed correlation checks.
- `versions.txt` for the host, toolchain, and full module graph.

CPU and allocation deltas begin after profiler initialization and end after `Stop()`. RSS is lifetime peak. The default logger is silent in our profiler, but Grafana's diagnostic logger is enabled in this fixture. Shutdown paths, logging, and span-name labels differ. These numbers are descriptive process costs, not a controlled steady-state overhead ranking.

Do not sum in-use heap or goroutine snapshots and call the result resident memory. The analysis preserves such sums to check transport/backend equality only. Do not add both `cpu` and `process_cpu` backend aliases, which name the same OTLP CPU data here.

The span check resolves `(trace ID, span ID)` to the local exporter. It does not check timestamp overlap. The local exporter records real SDK spans but does not export traces to Tempo or another trace server.

## Capture the real backend UI

Install the existing browser dependency:

```sh
npm ci --prefix e2e
```

Use the run's service prefix:

```sh
out=/tmp/your-run
prefix=$(< "$out/prefix")
node scripts/vs-grafana/shoot.mjs "$prefix-otel-default" process_cpu cpu /tmp/otel-cpu.png
node scripts/vs-grafana/shoot.mjs "$prefix-otel-default" space inuse_space /tmp/otel-heap.png
node scripts/vs-grafana/shoot.mjs "$prefix-grafana-default" process_cpu cpu /tmp/grafana-cpu.png
node scripts/vs-grafana/shoot.mjs "$prefix-grafana-default" memory inuse_space /tmp/grafana-heap.png
```

`PYRO` overrides the backend URL. `CHROME` overrides the browser path, which defaults to Google Chrome's macOS installation. The script checks the selected service, queries the backend through its actual UI, and requires a workload function before taking the image.

## Recheck saved artifacts

```sh
out=/tmp/your-run
(cd scripts/vs-grafana/workload && go build -o "$out/bin/workload" .)
"$out/bin/workload" analyze "$out" > "$out/quality.json"
python3 scripts/vs-grafana/backend.py "$out" > "$out/backend.json"
python3 scripts/vs-grafana/summarize.py "$out" > "$out/summary.json"
python3 scripts/vs-grafana/verify.py "$out"
```

`backend.py` requires the original backend data to remain available. A trace-ID filter can return an explicit unsupported response on Pyroscope's v1 query backend. Successful span filtering, not an invented trace UI link, is the E2E correlation result here.
