# Delta profiles without `go:linkname`

Heap allocations, mutex contention and blocking are cumulative in the Go
runtime: `runtime/pprof` reports totals since the process started. A
continuous profiler sends per-window deltas instead. By default otelprofile
computes them with [godeltaprof](https://github.com/grafana/pyroscope-go/tree/main/godeltaprof),
which keeps the previous counts and writes delta profiles itself. To build
the same profiles as `runtime/pprof` it reaches into `runtime/pprof`
internals through `go:linkname` (`runtime_FrameStartLine`,
`runtime_FrameSymbolName`, `runtime_expandFinalInlineFrame`,
`runtime_cyclesPerSecond`). Go restricts new `go:linkname` uses, and
projects such as opentelemetry-go are wary of the dependency.

`Config.RuntimeDeltas` (experimental, `OTEL_GO_X_PROFILES_RUNTIME_DELTAS`)
does it with public APIs only.

## How it works

1. At `Start` the profiler writes `pprof.Lookup("allocs"|"mutex"|"block")`
   once and sends it as a *baseline* record.
2. Each interval it writes the same cumulative profiles again (the heap
   after the same GC policy as the default path).
3. The encoder keys every sample by its frames (function names and line
   numbers, which are stable between writes, unlike pprof location IDs)
   and labels, and for `alloc_objects`, `alloc_space`, `contentions` and
   `delay` emits current − previous. `inuse_*` pass through. A counter that
   went down is taken as a fresh total.
4. The new totals become the baseline only once the request carrying them
   has been built for sending (`Encoder.CommitDeltas`), so a request split
   for size does not lose a window.

## Same data

`TestRuntimeDeltasMatchGodeltaprof` runs both paths in one process over the
same windows and exports both through OTLP. For the test's known functions:

| Delta | godeltaprof | runtime deltas |
|---|---|---|
| allocated bytes | 27,886,924 | 27,886,924 |
| allocated objects | 424 | 425 |
| mutex contentions / delay | 394 / 210,607,923 ns | 394 / 210,607,923 ns |
| block contentions / delay | 100 / 12,563,727 ns | 100 / 12,563,727 ns |

## Higher cost

The demo app at a 1 s interval with cpu, heap, mutex and block for 5
minutes (300 cycles), compared with the same app without a profiler:

| | CPU | Allocations |
|---|---|---|
| default (godeltaprof) | +0.9 s | +375 MB (1.25 MB per cycle) |
| `RuntimeDeltas` | +2.8 s | +1,237 MB (4.1 MB per cycle) |

The extra cost is `runtime/pprof` itself: every write serializes the whole
cumulative profile and allocates a new gzip writer (about 0.8 MB each, three
profiles per cycle), and the encoder then decompresses and indexes the
larger profiles. At the default 15 s interval that is roughly 3 MB more
allocation per cycle than godeltaprof.

## What would make it cheap

- A runtime API that hands out profile records without serializing them:
  the open proposal golang/go#74545 (`Recorder` types in runtime/pprof).
  The accepted golang/go#42502 (configurable CPU profiles) is related but
  covers the CPU profiler only.
- Or `runtime/pprof` reusing its gzip writer, which removes most of the
  allocation.

Until then the default stays godeltaprof, and this path exists for users
and projects that cannot take a `go:linkname` dependency.
