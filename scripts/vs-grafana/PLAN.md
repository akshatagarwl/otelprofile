# Comparison plan

Goal: compare otelprofile-go v0.3.0 with grafana/otel-profiling-go v0.7.0 plus pyroscope-go v1.4.0 on the same workload and Pyroscope 2.4.3 backend. Deliver a draft PR with reproducible measurements, real screenshots, source links, and explicit limitations. No production changes, releases, or merges.

## Workflow

1. Read poteto-mode principles and prior eBPF comparison. Done.
2. Pin released versions and inspect architecture, collection, correlation, and transport. Done.
3. Build one rerunnable workload with adapters. Use typed run records for workload counts, process cost, spans, and profiles. Run quality probes before timing.
4. Capture CPU/heap profiles and backend evidence. Test Grafana root defaults and all-span configuration separately. Inspect all supported profile types without mistaking correlation support for collection support.
5. Alternate at least five equivalent CPU+heap runs per mode, including an unprofiled control. Keep equal intervals, rates, request counts, and backend. Report median/range and call small gaps inconclusive on this shared host.
6. Capture actual backend screenshots for both CPU and heap. Write a concise comparison with a visual architecture diagram and reproducible commands.
7. Inspect the diff, run checks, publish a draft PR, and write the handoff. Do not merge.

Throughput checkpoint: one workload and one runner replace independent hand-run probes. Architecture exploration is skipped because this is an isolated measurement fixture, not a library redesign. The set_tasks tool is unavailable in this session; this file records the phases instead.

Principles used: Build the Lever changes the deliverable from a one-off table to a rerunnable workload/runner. Explain the Number requires equal work, per-process CPU/rusage, successful uploads, run ranges, and no winner claim below host noise. Separate Before Serializing Shared State keeps all writes in this worktree and newly named containers.

## Measurement decision

Expected claim: both stacks retain the planted Go caller and collect CPU/heap from the same runtime. The useful adoption difference is native OTLP links versus Pyroscope labels, not presumed sampling superiority. An overhead gap smaller than per-mode run variation is inconclusive. This workload is bounded by one CPU-burning worker plus allocation/GC, not network throughput. Verify the actual CPU hotspot before interpreting cost.

## Decision trail

| Decision | Evidence |
|---|---|
| Use released tags, not unreleased compression changes | origin/main af1834b; v0.3.0; Grafana releases v0.7.0/v1.4.0 |
| Compare Grafana integration with its profiler | Grafana README explicitly says it does not start pprof collection |
| Keep native transports | otelprofile OTLP/HTTP; pyroscope-go multipart pprof /ingest |
| Do not stop unrelated workloads | Shared Mac has active Rust agent workload and browser CPU use; alternate timing and disclose noise |
| Replace a wall deadline with fixed work before measured series | Preliminary probes completed different request counts. Measured probes completed 900 quality / 300 cost requests each. |
| Avoid a sampling-superiority claim | All three quality configurations retained every planted caller. |
| Distinguish defaults from capability | Real backend child-span filters return CPU for both every-span configurations and zero for Grafana root defaults. |
| Limit the overhead conclusion | Five CPU ranges overlap. Initialization is excluded, Stop is included, native shutdown/logging differ, RSS is lifetime peak. |
| Do not call snapshot sums live memory or resolved IDs temporal proof | Context-isolated same-family reviewer identified these interpretation limits. They are stated next to the results. |
| Retain raw evidence and re-run final analysis | A live edit to the shell runner caused postprocessing syntax failure after all 15 cost files completed. Workload binary was unchanged. Explicit analysis, backend queries, and verification passed. |
| Prevent data mixing in future runs | New runner refuses existing output and chooses a unique service prefix. |
| Preserve backend filtering failure | Trace-ID selector returns 501 for all configurations on this v1 query backend; no invented trace UI claim. |
