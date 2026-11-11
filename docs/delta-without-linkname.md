# Delta profiles without `go:linkname`

The Go runtime keeps heap allocations, mutex contention and blocking as
running totals since the process started. A continuous profiler sends what
changed in each window instead. Up to v0.3.0, otelprofile computed those
deltas with [godeltaprof](https://github.com/grafana/pyroscope-go/tree/main/godeltaprof),
which reads the runtime's internal profile records and `runtime/pprof`
helpers through `go:linkname`. It also had an opt-in `RuntimeDeltas` mode
that used only public APIs but cost about three times as much.

Both are gone. There is now one path, `internal/rtprof`. Nothing in the
module uses `go:linkname`, and it does not depend on godeltaprof. The
profiles carry the same data as godeltaprof's, field for field: full-depth
stacks, generic shape names, start lines, inline frames and mappings. They
cost more CPU per cycle, almost all of it inside runtime/pprof (see
[Cost](#cost)).

## How it works

Each interval, for heap, mutex and block:

1. `pprof.Lookup(name).WriteTo(w, 0)` writes runtime/pprof's cumulative
   profile. runtime/pprof reads the runtime's records at
   `debug.profstackdepth` (128 by default) and symbolizes them with the
   runtime's own helpers, so the profile has full-depth stacks, linker
   symbol names (`F[go.shape.int]`), function start lines, inline frames
   and the process mappings. It is the profile `go tool pprof` and
   `net/http/pprof` show.
2. Gunzip it with a reused reader into one of two reused buffers.
3. Index the locations by ID. Key every sample by its locations'
   addresses and, for the heap, its object-size label. runtime/pprof gives
   a location the address of its first PC, which is stable for the life of
   the process; location IDs are not, so they are not part of the key.
4. Add each sample's values to its key and subtract the key's totals from
   the previous call:
   - Heap: runtime/pprof writes each record scaled for sampling,
     `int(n · s)` with `s = 1 / (1 − e^(−size/rate))`. rtprof recovers the
     raw count `n` exactly (at most one integer maps to a given scaled
     value, since `s > 1`), takes the window's raw delta and scales that,
     as godeltaprof does. `inuse_*` are the current values.
   - Mutex and block: contentions and delay since the last call.
   A sample whose bytes are the same as in the previous call, with the
   same location numbering, adds the same values again, so it is matched
   by hash and byte comparison against the previous buffer and not decoded
   a second time.
5. Write an uncompressed pprof: every field of runtime/pprof's profile
   except the samples and locations is copied as is (strings, functions,
   mappings, sample types, period, time); then one sample per changed key,
   reusing its location IDs and labels; then the locations those samples
   use, copied byte for byte. The exporter's streaming encoder turns it
   into OTLP and drops functions and strings no sample uses.

## Why the first linkname-free version had gaps

The first version read `runtime.MemProfile`, `MutexProfile` and
`BlockProfile` and symbolized the PCs itself. Each public piece it used
fell short of what runtime/pprof gets through its internal linknames:

- **32-entry stacks.** `runtime.MemProfileRecord` and `BlockProfileRecord`
  embed `runtime.StackRecord`, whose `Stack0` is a fixed `[32]uintptr`.
  That size is part of the public API. runtime/pprof and godeltaprof call
  the internal `pprof_memProfileInternal` / `pprof_blockProfileInternal`
  instead, whose records hold `debug.profstackdepth` entries.
- **`F[...]` generic names.** `runtime.Frame.Function` is the
  user-facing name, in which the runtime replaces type arguments with
  `...`. runtime/pprof names functions with the linker symbol through the
  internal `runtime_FrameSymbolName`, which keeps the shape
  (`F[go.shape.int]`).
- **`start_line = 0`.** `runtime.Frame` keeps the function's start line
  in an unexported field (`startLine`). runtime/pprof reads it through the
  internal `runtime_FrameStartLine`; there is no exported accessor.

Reading runtime/pprof's output instead hands all three to the runtime's
own code, which may use those internals. The cost of that route is the
symbolization and compression runtime/pprof does on every call.

## Same data

The comparison lives in a separate module, `test/godeltaprof`, so the main
module does not depend on godeltaprof. CI runs it on Linux (amd64, arm64)
and macOS with Go 1.26 and 1.27:

```sh
cd test/godeltaprof
go test -race -run TestEquivalence -v .
go test -run '^$' -bench BenchmarkCycle -benchtime=100x .
```

`TestEquivalence` runs godeltaprof (configured as otelprofile configured
it, with `GenericsFrames` and `LazyMappings`) and rtprof side by side in
one process, over the same six windows. The workload has 512 shallow
stacks, 32 stacks of about 70 frames, generic recursion
(`genericDeep[go.shape.int]`, `[go.shape.string]`, a struct shape) up to
102 frames, and allocations made by the runtime for the caller (map
growth). For every stack that passes through the workload, it compares
each location's address, mapping, inline lines, function name, system
name, file name, line and start line, and the sample's labels. It
requires:

- the same set of stacks on both sides, each with the same depth, the
  same names (with generic shapes) and the same start lines;
- totals within 1 % (they are equal in practice);
- the deepest stack equal on both sides and at least 60 frames;
- no function with `start_line` 0.

Results over 18 runs (Go 1.27.3 and 1.26.9, `-race`, Apple M-series):
every stack identical; deepest stack 102 frames on both sides; 4,600–5,450
generic-shape frames per run on both sides; mutex and block values equal
for every stack; heap totals equal. Three additional final Go 1.27.3 race
runs passed with 102-frame stacks and 4,973 / 5,033 / 4,778 generic-shape
frames on each side.

Two differences are by design, because rtprof writes exactly what
runtime/pprof writes:

- **Leading runtime frames in heap stacks.** runtime/pprof charges an
  allocation the runtime makes for its caller (map growth, for example)
  to the first frame outside the runtime: it hides leading frames named
  `runtime.*` and `internal/runtime/*`. godeltaprof v0.1.12 carries an
  older copy of that rule, which hides only `runtime.*`, so it keeps
  frames such as `internal/runtime/maps.newarray` at the leaf. The test
  applies runtime/pprof's rule to godeltaprof's heap stacks before
  comparing, and logs how many it changed (298–327 in the three final
  Go 1.27.3 race-check runs, all
  under map growth). Where two of godeltaprof's records become one stack
  after that, it scaled each and truncated twice while rtprof truncates
  once, so their `alloc_objects` and `inuse_objects` can differ by one;
  the test allows exactly that and nothing more.
- **Mappings off Linux.** godeltaprof reads mappings only from
  `/proc/self/maps` and writes one empty mapping elsewhere. runtime/pprof
  also reads them on macOS, so rtprof's macOS profiles name the executable
  and its address range. On Linux, the test compares mappings exactly.

On Go 1.26, godeltaprof also writes an empty leading location for
contention on runtime-internal locks (it does not strip the runtime's
marker from those stacks); the test drops it and requires rtprof never to
write one.

### The old 424 vs 425 objects

The old `RuntimeDeltas` test reported 424 allocated objects for godeltaprof
and 425 for runtime deltas. That gap was rounding. Heap values are scaled
for sampling and then truncated to integers:

- godeltaprof truncates each window's scaled delta, `int(Δn · s)`.
- `RuntimeDeltas` truncated the scaled cumulative totals that
  runtime/pprof writes, and subtracted those: `int(N₂ · s) − int(N₁ · s)`.

For the test's 64 KiB objects at the default 512 KiB rate,
`s = 1 / (1 − e^(−1/8)) = 8.5104`. Two windows of 25 sampled objects each
give `int(25s) + int(25s) = 212 + 212 = 424`, against
`int(50s) − int(0) = 425`. rtprof recovers the raw counts and scales the
delta the way godeltaprof does, so the two agree.

## Cost

### Per cycle (benchmark)

`BenchmarkCycle` in `test/godeltaprof` measures one cycle per kind.
"all+encode" writes all three profiles and encodes them into one OTLP
request. Two workloads run after the full set of stacks has been recorded
once. In *churn* every stack changes every cycle. In *steady*, 16 stacks
change per cycle, which is closer to a service in steady state. The
workload runs and a GC publishes heap records between cycles, with the
timer stopped. Each benchmark runs in its own process, so no side inherits
another's records. Apple M-series, Go 1.27.3,
`runtime.MemProfileRate = 4096`, mutex fraction 1, block rate 1, median of
5 processes × 100 cycles. "rtprof v1" is the first linkname-free version
(public record readers, with the gaps above); the last column is
runtime/pprof's `WriteTo` alone, the floor for any route through public
runtime/pprof output, and its share of rtprof's time.

| workload | kind | godeltaprof | rtprof v1 (gaps) | **rtprof now** | of which runtime/pprof `WriteTo` |
|---|---|---|---|---|---|
| churn | heap | 1.87 ms, 389 KiB, 1,587 allocs | 0.63 ms (0.34×), 55 KiB, 115 allocs | 7.87 ms (4.20×), 1,866 KiB, 7,035 allocs | 5.53 ms (70 %), 1,710 KiB |
| churn | mutex | 1.38 ms, 204 KiB, 1,262 allocs | 2.00 ms (1.45×), 155 KiB, 637 allocs | 4.02 ms (2.91×), 1,237 KiB, 2,488 allocs | 3.43 ms (85 %), 1,195 KiB |
| churn | block | 2.83 ms, 290 KiB, 1,430 allocs | 8.37 ms (2.95×), 556 KiB, 2,484 allocs | 13.55 ms (4.78×), 2,009 KiB, 7,260 allocs | 12.40 ms (92 %), 1,970 KiB |
| churn | all+encode | 9.26 ms, 1,098 KiB, 7,201 allocs | 12.97 ms (1.40×), 825 KiB, 5,398 allocs | 30.68 ms (3.31×), 5,437 KiB, 21,100 allocs | — |
| steady | heap | 0.74 ms, 271 KiB, 1,128 allocs | 0.14 ms (0.19×), 16 KiB, 14 allocs | 3.85 ms (5.17×), 1,467 KiB, 4,257 allocs | 2.52 ms (66 %), 1,340 KiB |
| steady | mutex | 0.42 ms, 64 KiB, 229 allocs | 1.86 ms (4.47×), 136 KiB, 629 allocs | 3.40 ms (8.17×), 1,173 KiB, 2,241 allocs | 3.19 ms (94 %), 1,182 KiB |
| steady | block | 0.50 ms, 82 KiB, 278 allocs | 2.55 ms (5.06×), 187 KiB, 1,008 allocs | 4.51 ms (8.93×), 1,267 KiB, 3,057 allocs | 4.13 ms (92 %), 1,257 KiB |
| steady | all+encode | 2.78 ms, 565 KiB, 2,944 allocs | 5.60 ms (2.02×), 357 KiB, 2,241 allocs | 14.58 ms (5.25×), 4,216 KiB, 12,457 allocs | — |

Where the time goes: 66–94 % of rtprof's time is runtime/pprof writing the
cumulative profile. It symbolizes every frame of every record
(`runtime.(*Frames).Next`, `expandInlinedFrames`, `FuncForPC` for the heap's
runtime-frame rule), builds the location table, reads the mappings and
gzips the result, on every call, whether or not anything changed. The rest
is gunzip and rtprof's own pass, which the sample cache keeps small when
little changes. Allocation follows the same split: about 1.2–2 MB per call
is runtime/pprof's (most of it the gzip writer's state).

### Demo soak

The demo (`examples/demo`, 10 req/s, tracing off) ran for two rounds of
5 minutes at a 1 s interval, with cpu, heap, mutex and block enabled
(mutex fraction 5, block rate 10 µs). Four processes ran side by side:
no profiler, v0.3.0 (godeltaprof), the first linkname-free version, and
this full-fidelity version. Each completed both rounds without logged
errors. These are process CPU and total allocated bytes at exit, not
runtime CPU-class estimates:

| version | process CPU, rounds 1 / 2 | allocations, rounds 1 / 2 | max RSS, rounds 1 / 2 |
|---|---|---|---|
| no profiler | 33.427 / 36.291 s | 8,307.3 / 8,320.3 MB | 33.5 / 33.2 MB |
| v0.3.0 (godeltaprof) | 34.619 / 36.631 s | 8,681.1 / 8,698.8 MB | 42.4 / 42.8 MB |
| rtprof v1 (gaps) | 35.330 / 37.386 s | 8,630.0 / 8,642.3 MB | 39.1 / 40.3 MB |
| **rtprof now** | **37.090 / 38.823 s** | **9,524.7 / 9,543.0 MB** | **43.0 / 46.5 MB** |

Against godeltaprof, full fidelity adds 2.471 / 2.192 s of process CPU
per 300 s (about 8.2 / 7.3 ms per nominal cycle), and 843.6 / 844.2 MB
of allocation (about 2.81 MB per cycle). Against no profiler, it adds
3.663 / 2.532 s of CPU and 1,217.4 / 1,222.7 MB of allocation.
GC cycles were 1,458 / 1,453 against godeltaprof's 1,028 / 1,022;
GC CPU-class estimates were 3.260 / 3.579 s against 2.380 / 2.895 s.
The soak includes CPU-profiler restarts and GC pacing, unlike the
per-kind benchmark. It does not make the full-fidelity route cheap.

These are noisy laptop measurements on macOS arm64, not a Linux soak or
a production overhead guarantee. Raw logs: `/tmp/soak1/s4/r{1,2}-*.log`;
completion marker: `/tmp/soak1/s4/DONE`. No finished soak or benchmark was
rerun for the documentation update.

With the default types (cpu, heap) mutex and block cost nothing.

## What would make it cheaper

- A public API that hands out full-depth records and the symbol name and
  start line of a frame. The open proposal golang/go#74545 (`Recorder`
  types in runtime/pprof) would cover the records; rtprof could then
  symbolize each stack once instead of runtime/pprof symbolizing every
  stack on every call.
- An uncompressed `WriteTo`. Today runtime/pprof always gzips debug=0
  output, and rtprof immediately gunzips it.
