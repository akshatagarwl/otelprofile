# Changelog

All notable changes to this module. Versions follow semantic versioning;
before 1.0 minor versions may break the API, and the OTLP Profiles wire
format follows the upstream development revision.

## Unreleased

### Breaking

- Heap, mutex and block deltas have a single source now: `runtime.MemProfile`,
  `runtime.MutexProfile` and `runtime.BlockProfile`, through the public API
  only (internal/rtprof). The module no longer depends on
  `github.com/grafana/pyroscope-go/godeltaprof`, and nothing in it uses
  `go:linkname`.
- Removed `Config.RuntimeDeltas`, `profile.WithRuntimeDeltas`,
  `OTEL_GO_X_PROFILES_RUNTIME_DELTAS`, `profile.Record.Cumulative`,
  `profile.Record.Baseline`, `transform.Input.Cumulative`/`Baseline` and
  `Encoder.CommitDeltas`.
- Cost: heap profiles are about half the CPU of godeltaprof and allocate
  almost nothing. Mutex and block profiles cost 2.5–7× more CPU per cycle,
  because `runtime.MutexProfile`/`BlockProfile` symbolize every record on
  every call (`runtime.expandFrames`). Both types are opt-in. See
  docs/delta-without-linkname.md.
- Heap, mutex and block `Record.Pprof` are uncompressed pprof now. CPU and
  goroutine profiles are still gzipped by runtime/pprof.

### Known differences from godeltaprof

- Stacks hold at most 32 entries (the public `runtime.StackRecord` size;
  godeltaprof read up to 128). Deeper stacks are cut at the 32nd frame.
  Totals are unchanged.
- Generic functions are named `F[...]` (`runtime.Frame.Function`) instead of
  the linker's shape names such as `F[go.shape.int]`.
- `Function.start_line` is 0, because `runtime.Frame` does not export it.

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
