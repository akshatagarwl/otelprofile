# Changelog

All notable changes to this module. Versions follow semantic versioning;
before 1.0 minor versions may break the API, and the OTLP Profiles wire
format follows the upstream development revision.

## Unreleased

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
