# Scorecard

Every row is compared with pyroscope-go v1.4.3 on the same demo app
(`examples/demo`, fixed workload: HTTP handlers burning CPU and allocating,
mutex contention, channel blocking, 50 parked goroutines). sigiro is the
local `feat/go-profiling` build. Evidence paths are relative to this
module; `rN-` is the round that produced them.

Status column: rounds 12 and 13 (2026-11-08, both clean, every row PASS, no change between them). See ROUNDS.md for history.

| # | Row | Status | Evidence | vs pyroscope-go |
|---|---|---|---|---|
| 1 | One-call setup, env-only config, README quickstart ≤ 5 min | PASS | `otelprofile.Start(otelprofile.Config{ServiceName, Endpoint})`. Env-only: `TestEnvOnlyConfiguration` (`OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_GO_X_PROFILES_*`). Quickstart followed word for word in `/tmp/qs.c7nX`: 69 s from `mkdir` to the flame graph (`${WORK:-/tmp/otelprofile-work}/quickstart-timing.txt`, `evidence/r1-quickstart.png`, `main.work` 36.1 s of 37.0 s). | Same shape as `pyroscope.Start(pyroscope.Config{ApplicationName, ServerAddress})`; pyroscope-go has no env-only config. |
| 2 | Stop/Flush clean; host never blocked, never panics; errors logged | PASS | `profile` tests: hung exporter (Shutdown bounded by ctx), exporter panic recovered and reported, idempotent Shutdown, Flush after Shutdown is a no-op, CPU profiler already taken → one error, other types keep flowing, rates restored. `TestUnreachableEndpointIsLoggedNotFatal`: error logged within the 10 s retry budget, `Stop` ≤ 5 s. All with `-race` on darwin and in a linux/arm64 container (`evidence/r2-tests-*.txt`). | pyroscope-go: `Stop` flushes; upload errors logged. Equal. |
| 3 | Every profile type; mutex/block/CPU opt-in where pyroscope makes them opt-in | PASS | All 11 sample types stored in sigiro from both paths in the same run (`evidence/r2-types.json`): cpu, samples, alloc_objects, alloc_space, inuse_objects, inuse_space, goroutines, mutex_count, mutex_duration, block_count, block_duration. Defaults cpu+heap like pyroscope-go; goroutine/mutex/block opt-in. | Same set (pyroscope's goroutine_leak needs a GOEXPERIMENT; not covered). |
| 4 | Static tags + dynamic TagWrapper labels, filterable in sigiro | PASS | Static `Tags` → resource attributes; `otelprofile.TagWrapper(ctx, otelprofile.Labels("endpoint", "/slow"), fn)` → OTLP sample attributes → sigiro `labels` column. Dashboard filter picked interactively (`evidence/r2-label-filter.png`: only `main.slowHandler` remains, 100 % linked). `TestTagWrapperLabelsReachSamples`. | Same API shape as `pyroscope.TagWrapper`/`pyroscope.Labels`. |
| 5 | Span↔profile via `Sample.link` | PASS | `profiletrace.NewTracerProvider(tp)` sets `otel.trace_id`/`otel.span_id` pprof labels; the encoder turns them into `link_table` entries and strips them from attributes. In sigiro 91–97 % of CPU rows carry trace/span IDs. Trace view → "Open profile" on a span → flame graph filtered to that span (`evidence/r2-trace-to-profile.png`, URL has `trace=…&span=…`). | grafana/otel-profiling-go only adds a `span_id` label (no trace ID, no OTLP link). |
| 6 | Viewing in sigiro: flame graph per service/type/range, top functions, label filter, diff, trace→profile | PASS | New Profiles page: d3-flame-graph 5.0.0 (Apache-2.0) icicle, shadcn Select/Popover/Command/Tabs/Table/Card/Badge/Empty, timeline (evilcharts). `evidence/r2-flamegraph.png`, `r2-label-filter.png`, `r2-diff.png` (vs previous window; red grew, blue shrank), `r2-pyro-mutex.png` (bridge data, mutex wait), `r2-trace-to-profile.png`. Snapshot types (inuse, goroutines) are averaged over collections. | Pyroscope UI has the same views (plus a "sandwich" view). |
| 7 | A/B accuracy within a documented tolerance | PASS (r12 / r13: linux CPU +0.6 % 0.39 pt / +0.2 % 0.63 pt; darwin CPU −0.2 % 0.73 pt / −0.1 % 0.46 pt without the write path, 0.98 / 1.69 pt with it; non-CPU ≤ 1.42 pt) | Rule (user, Nov 8). Non-CPU types: one process runs both profilers; per-type total within ±10 % and every top-20 path within ±3 points (goroutines leave out the profilers' own and in-flight upload goroutines on both sides; the raw comparison is recorded). **CPU passes only if (A)** the linux/arm64 container A/B passes the full rule (total within 10 %, every top-20 path within 3 points) **and (B)** the darwin A/B passes the same rule with the HTTP response write-syscall path (`net/http.(*conn).serve` … `syscall.write`) left out of the top-20 comparison (totals stay complete; other paths keep their share of the full profile). The darwin result with that path included is reported for information only. CPU A/B = one process alternating pyroscope-go and otelprofile in 2-minute turns (`scripts/ab.sh`, `scripts/ab-linux.sh`, `scripts/ab-report.sh` with `OS=darwin|linux`). Ground truth: profiled CPU ÷ process rusage CPU, linux 0.994 vs 0.994, darwin ~0.86 vs ~0.86; `TestGoroutineAndCPUMatchGroundTruth`. | Reference. |
| 8 | Standard OTLP: sigiro, collector `otlp`+`debug`, profcheck | PASS | sigiro ingests (row 3). otelcol-contrib 0.165.0 with `service.profilesSupport`: 3 batches, sample records counted, link table with real trace IDs, no errors (`evidence/r2-collector.log`). `profcheck -check-dupes -check-orphans -check-sample-shapes` passes on every request (`evidence/r2-profcheck.txt`); unit and fuzz tests run profcheck on every output. | pyroscope-go speaks only Pyroscope's push API. |
| 9 | Resource attributes and trace links pyroscope-go lacks | PASS | `evidence/r2-resource-links.json`: OTLP rows have `service.instance.id`, `host.name`, `process.pid`, `telemetry.sdk.*`, OS and runtime attributes, and 467 of 512 CPU rows linked; Pyroscope-bridge rows have none of these and 0 links. | — |
| 10 | Overhead ≤ pyroscope-go (CPU, allocs, RSS, ≥ 30 min soak); tests, fuzz, golden, vet/lint; darwin + linux | CPU PASS, allocs PASS, RSS ACCEPTED BY USER (Nov 8: keep the reused encoder state, ~1.1 MB more heap for fewer allocations), quality PASS | Soak = `scripts/soak.sh` (no profiler / otelprofile / pyroscope-go side by side, cpu+heap, 15 s, 32 min); amplified = same at a 1 s interval for 8 min. Quality: `go vet` and golangci-lint clean (main and demo modules), race tests, 2 fuzz targets, golden fixtures, linux/arm64 container tests and an end-to-end run from a linux/arm64 container (`evidence/r1-linux-arm64-e2e.json`). | — |

## Overhead, all soaks (32 min, cpu+heap, 15 s; values above the no-profiler process)

| Soak | CPU s otel / pyro | Allocations otel / pyro | Mean RSS otel / pyro (MB) | Peak RSS otel / pyro (MB) |
|---|---|---|---|---|
| 1 (google/pprof transform) | −2.7 / +6.3 | +357 / +146 MB | — | 42.4 / 40.0 |
| 2 (encoder) | −1.9 / +3.2 | +242 / +131 MB | — | 46.8 / 39.7 |
| 3 (intern bound, 30 s CPU window) | −3.9 / +7.2 | +151 / +147 MB | — | 42.7 / 41.0 |
| 4 (proto-free, split binaries) | +2.8 / +5.1 | +128 / +158 MB | 38.4 / 38.4 | 41.2 / 43.4 |
| 5 | −1.1 / +2.4 | +121 / +140 MB | 43.3 / 40.4 | 49.9 / 47.3 |
| 6 | −4.8 / +0.8 | +124 / +140 MB | 45.2 / 38.7 | 49.0 / 40.0 |
| 7 | −18.9 / −11.0 | +128 / +140 MB | 42.8 / 41.2 | 49.1 / 50.0 |
| 8 (darwin) | −5.9 / −1.0 | +128 / +139 MB | 39.9 / 39.3 | 41.4 / 41.6 |
| 8 (linux container, VmRSS) | +0.9 / +3.7 | +146 / +159 MB | 23.3 / 22.7 | 32.4 / 30.0 |
| 9 (darwin; round invalid, the Mac slept mid-round) | +0.0 / +0.9 | +125 / +142 MB | 42.9 / 40.8 | 49.5 / 47.2 |
| 10 (darwin) | −20.4 / −14.9 | +127 / +138 MB | 44.6 / 41.9 | 50.6 / 49.1 |
| 10 (linux) | +0.3 / +4.1 | +146 / +159 MB | 23.0 / 22.3 | 32.5 / 31.3 |
| 12 (darwin) | −23.0 / −14.9 | +128 / +143 MB | 47.0 / 42.4 | 50.9 / 46.1 |
| 12 (linux) | +5.4 / +9.5 | +170 / +177 MB | 23.6 / 22.8 | 31.9 / 30.9 |
| 13 (darwin) | −11.7 / −8.9 | +134 / +140 MB | 42.1 / 43.8 | 45.4 / 49.0 |
| 13 (linux) | +0.6 / +4.7 | +143 / +153 MB | 23.5 / 22.9 | 31.7 / 30.8 |

RSS: accepted by the user (Nov 8) for the reused encoder state.

Exact retained heap (`bin/memcheck`, MemProfileRate=1): otelprofile 3.72 MB,
pyroscope-go 2.59 MB. The ~1.1 MB difference is the encoder's reusable
state; dropping it after each export matches pyroscope-go's heap (2.70 MB)
but allocates ~1.75 MB more per cycle (`evidence/r5-memcheck.txt`).
