# Changelog

All notable changes to this module. Versions follow semantic versioning;
before 1.0 minor versions may break the API, and the OTLP Profiles wire
format follows the upstream development revision.

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
