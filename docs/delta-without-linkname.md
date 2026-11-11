# Delta profiles without `go:linkname`

The Go runtime keeps heap allocations, mutex contention and blocking as
running totals since the process started. A continuous profiler sends what
changed in each window instead. Up to v0.3.0, otelprofile computed those
deltas with [godeltaprof](https://github.com/grafana/pyroscope-go/tree/main/godeltaprof),
which reads the runtime's internal profile records and `runtime/pprof`
helpers through `go:linkname`. It also had an opt-in `RuntimeDeltas` mode
that used only public APIs but cost about three times as much.

Both are gone. There is now one path, `internal/rtprof`. It uses only the
public runtime API, and the module no longer depends on godeltaprof or on
any other `go:linkname` code. The heap profile now costs about half as
much as with godeltaprof. Mutex and block profiles cost 2.5 to 7 times as
much: the public API symbolizes every record on every read (see
[Cost](#cost)). Both of those types are opt-in.

## How it works

Each interval, for heap, mutex and block:

1. Read the cumulative records with `runtime.MemProfile(buf, true)`,
   `runtime.MutexProfile(buf)` or `runtime.BlockProfile(buf)`. These are the
   same records that runtime/pprof reads internally, with PC stacks. The
   record slice is kept and reused.
2. Key every record by its stack (and by object size for the heap, as
   runtime/pprof does). Sum records that share a key, and subtract the
   previous totals for that key. Known stacks are found through a hash map,
   with no allocation.
3. For each key that changed, scale and convert the values:
   - Heap: `alloc_objects` and `alloc_space` are the window's deltas,
     `inuse_*` the current values. Both are scaled for sampling with
     runtime/pprof's `scaleHeapSample` (`1 / (1 - exp(-avgSize/MemProfileRate))`).
   - Mutex and block: `delay = cycles / (cyclesPerSecond / 1e9)`, as
     runtime/pprof computes it, then delta'd per key.
4. Turn a stack into pprof locations once, the first time it is seen.
   Symbolization uses `runtime.CallersFrames` and runtime/pprof's
   inline-frame grouping, and the result is cached per PC and per stack
   for the life of the Profiler. The heap profile hides runtime frames
   above the allocating function, as runtime/pprof does.
5. Write an uncompressed pprof protobuf with only the samples that changed
   and the locations, functions and strings they use, into a reused
   buffer. The exporter's streaming encoder turns it into OTLP. It already
   skips decompression when the input is not gzipped.

Nothing is gzipped, and no cumulative profile is ever serialized or parsed
back.

### Replacing the four private helpers

| godeltaprof / runtime/pprof used | rtprof uses |
|---|---|
| `runtime_cyclesPerSecond` | The `cycles/second=` line that `pprof.Lookup("mutex").WriteTo(w, 1)` prints in its header. This is the runtime's own value, so it is exact. It is read once per process, before the Profiler raises the sampling rates. On this machine that read took about 14 ms with 512 mutex records, and less on the usual near-empty profile. |
| `runtime_expandFinalInlineFrame` | `runtime.CallersFrames([]uintptr{lastPC, 1})`. When the next PC is not the virtual PC of an outer inlined frame, `CallersFrames` inserts those virtual PCs itself; the sentinel `1` has no function, so the walk ends there. |
| `runtime_FrameSymbolName` | `runtime.Frame.Function`. Generic functions read `F[...]`, not `F[go.shape.int]`. |
| `runtime_FrameStartLine` | Nothing: `Function.start_line` is 0. |
| internal record readers (`pprof_memProfileInternal`, …) | `runtime.MemProfile`, `MutexProfile`, `BlockProfile`. |

## Known gaps

All three come from the public API.

- **Stacks have at most 32 entries.** `runtime.MemProfileRecord` and
  `BlockProfileRecord` hold 32 PCs. runtime/pprof and godeltaprof read
  `debug.profstackdepth` (128 by default). Deeper stacks lose their outer
  frames and are cut at the 32nd. Records that agree in their first 32
  entries merge into one sample. Totals do not change. On the equivalence
  workload below, whose 32 deep paths per kind are about 50 frames deep:

  | | heap | mutex | block |
  |---|---|---|---|
  | godeltaprof samples deeper than 32 frames (share of value) | 51 of 1,854 (0.9 %) | 1 of 268 (0.0 %) | 32 of 332 (1.1 %) |
  | rtprof records exactly 32 entries deep (share of value) | 83 of 2,947 (0.9 %) | 4 of 275 (0.0 %) | 32 of 368 (1.1 %) |

  rtprof counts raw records, before records that share a key are merged,
  so its record counts are higher than godeltaprof's sample counts. In the
  demo service (90 s, all types), the deepest heap, mutex and block stacks
  are 21, 16 and 18 frames, so no record reaches the cap.
- **Generic function names** are `F[...]` instead of the linker's shape
  name.
- **`Function.start_line` is 0.**

## Same data

The comparison lives in a separate module, `test/godeltaprof`, at commit
`8bbff08`. It was removed afterwards so the repository holds no
godeltaprof code. To run it:

```sh
git checkout 8bbff08 -- test/godeltaprof
cd test/godeltaprof && go test -run TestEquivalence -v . && go test -run '^$' -bench BenchmarkCycle -benchtime=100x .
```

Its `TestEquivalence` runs godeltaprof and rtprof
side by side in one process, over the same windows, on a workload with 512
shallow and 32 deep distinct stacks per kind. It checks every total within
1 %. Largest difference over 40 runs, on Go 1.27.3 and on Go 1.26.9:

| Total | max difference |
|---|---|
| heap `alloc_objects` / `alloc_space` of the workload's function | 0 / 0 |
| heap `alloc_objects` / `alloc_space` over all stacks except the profilers' own | 0.001 % / 0.27 % |
| heap `inuse_objects` / `inuse_space` over all stacks | 0 / 0 |
| mutex contentions / delay of the workload's function | 0 / 0 |
| mutex contentions / delay over all stacks except runtime-internal locks | 0.21 % / 0.06 % |
| block contentions / delay, workload and all stacks | 0 / 0 |

Each stack under 32 frames has the same values on both sides, except for
the profilers' own allocations and contention on runtime-internal locks.
Those show up on whichever side reads after they happen. On Go 1.26,
godeltaprof writes runtime-lock stacks with an extra unnamed leaf frame;
rtprof does not.

### The old 424 vs 425 objects

The old `RuntimeDeltas` test reported 424 allocated objects for godeltaprof
and 425 for runtime deltas. That gap is rounding. Heap values are scaled
for sampling and then truncated to integers:

- godeltaprof truncates each window's scaled delta, `int(Δn · s)`.
- `RuntimeDeltas` truncated the scaled cumulative totals that
  runtime/pprof writes, and subtracted those: `int(N₂ · s) − int(N₁ · s)`.

For the test's 64 KiB objects at the default 512 KiB rate,
`s = 1 / (1 − e^(−1/8)) = 8.5104`. Two windows of 25 sampled objects each
give `int(25s) + int(25s) = 212 + 212 = 424`, against
`int(50s) − int(0) = 425`. The difference is at most one per stack per
window. rtprof scales the delta the way godeltaprof does, so the two agree
exactly.

## Cost

### Per cycle (benchmark)

`BenchmarkCycle` in the comparison module (above) measures one cycle per kind. "all+encode" writes
all three profiles and encodes them into one OTLP request. Two workloads
run after the full set of stacks has been recorded once. In *churn* every
stack changes every cycle. In *steady*, 16 stacks change per cycle, which
is closer to a service in steady state. The workload runs and a GC
publishes heap records between cycles, with the timer stopped. Apple
M-series, Go 1.27.3, `runtime.MemProfileRate = 4096`, mutex fraction 1,
block rate 1, median of 5 runs × 100 cycles:

| workload | kind | godeltaprof | rtprof | godeltaprof alloc | rtprof alloc |
|---|---|---|---|---|---|
| churn | heap | 1.92 ms | 1.13 ms (0.59×) | 396 KiB, 1,383 allocs | 19 KiB, 3 allocs |
| churn | mutex | 0.93 ms | 2.36 ms (2.54×) | 186 KiB, 1,150 allocs | 159 KiB, 600 allocs |
| churn | block | 2.46 ms | 7.76 ms (3.16×) | 275 KiB, 1,344 allocs | 515 KiB, 2,080 allocs |
| churn | all+encode | 8.70 ms | 13.52 ms (1.55×) | 1,164 KiB, 6,634 allocs | 750 KiB, 4,680 allocs |
| steady | heap | 2.65 ms | 1.24 ms (0.47×) | 583 KiB, 1,262 allocs | 16 KiB, 1 alloc |
| steady | mutex | 0.35 ms | 2.44 ms (6.92×) | 45 KiB, 120 allocs | 150 KiB, 633 allocs |
| steady | block | 1.39 ms | 7.76 ms (5.58×) | 127 KiB, 199 allocs | 503 KiB, 2,130 allocs |
| steady | all+encode | 5.71 ms | 12.05 ms (2.11×) | 930 KiB, 2,892 allocs | 686 KiB, 3,435 allocs |

An earlier run of the steady mutex+block cycle measured 5.1 ms for
rtprof against 2.4 ms for godeltaprof. A CPU profile of that run puts 93 %
of rtprof's mutex/block time inside `runtime.expandFrames`, which
`runtime.MutexProfile` and `runtime.BlockProfile` call on every read. It
runs `runtime.CallersFrames` over every frame of every record, computing
file, line and function name, and allocates once per record. rtprof's own
code (hashing, map lookups, writing) is about 4 %. godeltaprof avoids that
work because it reads the internal records through `go:linkname`. Every
public way to read these profiles pays it, `pprof.Lookup(...).WriteTo`
included. The cost grows with the number of mutex/block records, not with
how much changed.

The heap path has no such step. `runtime.MemProfile` copies records
without symbolizing them, and rtprof symbolizes each stack only once. A
steady-state heap profile allocates one object (the output buffer is
copied for the exporter).

### Demo soak

The demo service (`examples/demo`, 10 req/s) ran with a 1 s interval and
cpu, heap, mutex and block (mutex fraction 5, block rate 10 µs) for 5
minutes, which is 300 cycles. Three processes ran side by side: no
profiler, v0.3.0 (godeltaprof), and this change. There were 6 rounds; in
the last 4 the runtime also reported CPU by class
(`/cpu/classes/{user,gc/total}`):

| | CPU vs the previous version | allocations vs no profiler |
|---|---|---|
| v0.3.0 (godeltaprof) | | +375–381 MB (1.25 MB per cycle) |
| this change | +1.0 to +1.8 s per 300 cycles (≈ 3–6 ms per cycle) | +320–325 MB (1.07 MB per cycle) |

Of the extra CPU, user time accounts for +0.5 to +0.8 s, mostly the
mutex/block `expandFrames` work above. GC accounts for +0.6 to +0.9 s.
That GC share is not profiler work. rtprof keeps about 8 MB less live
heap than godeltaprof (runtime memory 31–39 MB against 39–43 MB), so the
GC pacer, which scales with the live heap, runs the demo's GC more often:
about 1,350 cycles against about 1,015 in 300 s. A control run of this
change with an 8 MiB ballast ran 612–614 GC cycles and 1.2–1.4 s of GC
CPU. The extra CPU is therefore partly memory saved. Most of the
allocation is CPU-profiler restarts (about 2 MB each, every second
interval), and those are the same on both paths.

With the default types (cpu, heap) there is no mutex/block cost, and
heap is cheaper than before.

## What would make it cheaper still

- A public way to read mutex/block records without `expandFrames`. The open
  proposal golang/go#74545 (`Recorder` types in runtime/pprof) would hand
  out records with full-depth stacks. Today `runtime.MutexProfile` and
  `BlockProfile` symbolize every record on every call, and that is the
  remaining mutex/block cost.
- Encoding straight to OTLP, without the intermediate uncompressed pprof,
  would save parsing the small delta profile in the encoder. That parse is
  a small share of a cycle next to the cost above, so it is not done.
