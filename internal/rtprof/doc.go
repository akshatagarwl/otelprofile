// Package rtprof writes delta heap, mutex and block profiles from the Go
// runtime's public profiling API (runtime.MemProfile, runtime.MutexProfile,
// runtime.BlockProfile) only, with no access to runtime internals.
//
// Each Profiler keeps the previous cumulative values per stack and writes
// only the change since its last call, as an uncompressed pprof protobuf.
// Symbolization (runtime.CallersFrames) is cached per PC and per stack for
// the life of the Profiler, and every buffer is reused, so a steady-state
// call allocates almost nothing.
//
// The location building (inline-frame grouping with a "deck", final inline
// frame expansion, /proc/self/maps mappings) follows Go's runtime/pprof
// (BSD-3-Clause, Copyright The Go Authors), rewritten on public APIs.
//
// Known differences from runtime/pprof and godeltaprof, all caused by the
// public API:
//   - Stacks are at most 32 entries deep (runtime.StackRecord.Stack0);
//     runtime/pprof and godeltaprof see up to debug.profstackdepth (128).
//     Records whose stacks agree in their first 32 entries are merged.
//   - Function names are runtime.Frame.Function, so generic functions read
//     F[...] rather than the linker's shape name F[go.shape.int].
//   - Function.start_line is 0 (runtime.Frame does not export it).
package rtprof
