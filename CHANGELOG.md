# Changelog

All notable changes to this module. Versions follow semantic versioning;
before 1.0 minor versions may break the API, and the OTLP Profiles wire
format follows the upstream development revision.

## Unreleased

### Breaking

- Heap, mutex and block deltas have a single source now, internal/rtprof:
  it reads runtime/pprof's own profiles (`pprof.Lookup(name).WriteTo`)
  and subtracts the previous call's values per stack. The module no longer
  depends on `github.com/grafana/pyroscope-go/godeltaprof`, and nothing in
  it uses `go:linkname`. The profiles match godeltaprof's field for field
  (full-depth stacks, generic shape names, start lines, inline frames,
  mappings); `test/godeltaprof` checks that in CI. Heap stacks hide
  leading `internal/runtime/*` frames as runtime/pprof does, and macOS
  profiles carry the executable's mapping.
- Removed `Config.RuntimeDeltas`, `profile.WithRuntimeDeltas`,
  `OTEL_GO_X_PROFILES_RUNTIME_DELTAS`, `profile.Record.Cumulative`,
  `profile.Record.Baseline`, `transform.Input.Cumulative`/`Baseline` and
  `Encoder.CommitDeltas`.
- Cost: heap, mutex and block profiles cost 3–9× godeltaprof's CPU per
  cycle in the benchmark, 66–94 % of it in runtime/pprof's `WriteTo`,
  which symbolizes and gzips the whole cumulative profile on every call.
  Mutex and block are opt-in. See docs/delta-without-linkname.md.
- Heap, mutex and block `Record.Pprof` are uncompressed pprof now. CPU and
  goroutine profiles are still gzipped by runtime/pprof.

### Fixed

- The encoder replaces invalid UTF-8 in pprof strings (possible through
  user-supplied labels) instead of writing a request no receiver can
  decode.

## v0.3.0 — 2026-11-09

### Breaking

- The module path is now `github.com/akshatagarwl/otelprofile-go`, matching
  the renamed repository. Package names are unchanged (`otelprofile`,
  `profile`, `otlpprofilehttp`, `profiletrace`), so only import lines change:

  ```sh
  go get github.com/akshatagarwl/otelprofile-go@v0.3.0
  ```

  v0.2.0 and earlier stay available under the old path.

### Documentation

- README: screenshots of real profiles (sigiro, Grafana Pyroscope, trace to
  span profile) and animated diagrams of the export pipeline, trace links,
  the OTLP dictionary and overhead. Sources in `docs/assets/src`.
- `docs/vs-ebpf.md`: measured comparison with the OpenTelemetry eBPF
  profiler on the same Go app, with Pyroscope screenshots. Scripts in
  `scripts/vs-ebpf`, evidence in `evidence/vs-ebpf`.

## v0.2.0 — 2026-11-08

### Changed

- The module now declares `go 1.26.0` (was 1.27.3), the minimum
  opentelemetry-go supports.

### Added

- Experimental `Config.RuntimeDeltas` / `profile.WithRuntimeDeltas`
  (`OTEL_GO_X_PROFILES_RUNTIME_DELTAS`): heap, mutex and block deltas
  computed by the encoder from runtime/pprof's cumulative profiles, with no
  `go:linkname`. A test checks they match godeltaprof's in the same process.

- CI: tests on Go 1.26 and 1.27 (Linux amd64 and arm64, macOS), lint, fuzz,
  `profcheck`, and end-to-end checks against the OpenTelemetry Collector
  and Grafana Pyroscope.

## v0.1.0 — 2026-11-08

First release.

- `otelprofile.Start` / `Stop` / `Flush`, configurable by `Config` or the
  `OTEL_*` environment.
- CPU, heap (alloc and in-use), goroutine, mutex and block profiles;
  defaults cpu + heap; mutex and block opt-in with rates restored on stop.
- `TagWrapper` / `Labels` for dynamic labels as OTLP sample attributes.
- `profiletrace.NewTracerProvider` links CPU samples to spans through OTLP
  `Sample.link`.
- `otlpprofilehttp` exporter: OTLP/HTTP protobuf to
  `/v1development/profiles`, optional gzip, retries with `Retry-After`,
  partial success, request size cap.
- Streaming pprof → OTLP encoder (one deduplicated dictionary per request,
  passes `profcheck` with every check enabled).
