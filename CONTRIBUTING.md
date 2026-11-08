# Contributing

Thanks for helping. Issues, measurements from real services, and pull
requests are all welcome.

## Before you start

- Open an issue first for anything bigger than a small fix, so we can agree
  on the approach. OTLP Profiles is still a development signal, and some
  changes are better discussed with the OpenTelemetry Profiling SIG first.
- Keep the profiled program safe: no panics, no blocking in application
  goroutines, errors logged rather than returned after `Start`, and no new
  process-wide runtime settings unless they are opt-in and restored on
  `Stop`.
- Keep the production import graph small. The encoder and exporter use
  only `protowire`; google/pprof and the generated OTLP protos are for
  tests (`internal/transform/oracle`).

## Development

```sh
go test -race ./...                                   # unit, golden, conformance, ground-truth tests
go test ./internal/transform -run '^$' -fuzz FuzzEncoder -fuzztime 1m
golangci-lint run ./... && (cd examples/demo && golangci-lint run ./...)
scripts/ci-collector.sh                               # needs Docker
scripts/ci-pyroscope.sh                               # needs Docker
```

The minimum Go version is the one in `go.mod` (1.26), the same as
opentelemetry-go. CI runs Go 1.26 and 1.27 on Linux (amd64, arm64) and
macOS.

If you change the encoder, run the golden tests with `-update` and review
the diff in `internal/transform/testdata/*.golden.txt`; the encoder must
keep matching the reference converter (`TestEncoderMatchesBuilder`,
`FuzzEncoder`) and passing `profcheck`.

## Measuring

Overhead or accuracy claims need numbers. `examples/demo` is a fixed
workload that runs with `-profiler=otel`, `pyroscope`, `both` (both
profilers in one process) or `alternate` (CPU profilers taking turns).
`scripts/ab.sh`, `scripts/ab-report.sh` and `scripts/soak.sh` reproduce the
comparisons in `SCORECARD.md`; `examples/demo/memcheck` measures retained
heap exactly. Some scripts expect a sigiro server as the receiver for
queries; adapt them to your backend's query API as needed.

## Pull requests

- One change per pull request, with tests.
- Add a line under "Unreleased" in `CHANGELOG.md`.
- Conventional commit titles (`feat:`, `fix:`, `perf:`, `docs:`, `test:`,
  `ci:`, `chore:`).
- If an AI tool helped write the change, say so with an `Assisted-by:`
  trailer naming the tool, and make sure you understand and can explain
  every line. Don't use `Co-authored-by:` for AI tools.

By contributing you agree that your contributions are licensed under the
Apache License 2.0.
