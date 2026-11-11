// Package rtprof writes delta heap, mutex and block profiles through the
// public runtime/pprof API, with no go:linkname in this module.
//
// Each Profile call writes runtime/pprof's cumulative profile
// (pprof.Lookup(name).WriteTo(w, 0)), which carries everything the runtime
// records: full-depth stacks (debug.profstackdepth, 128 by default), linker
// symbol names including generic shapes, function start lines, inline
// frames and the process mappings. It then subtracts the previous call's
// values per stack and writes the change as an uncompressed pprof that
// keeps runtime/pprof's locations, functions, mappings and strings
// byte for byte.
//
// A stack is identified by the addresses of its locations (runtime/pprof
// gives each location the PC of its leaf frame, which is stable for the
// life of the process) plus, for the heap, the object size label. Nothing
// is symbolized or decoded into strings here; the gzip reader and every
// buffer are reused.
//
// Values follow godeltaprof, which otelprofile used before:
//   - heap: alloc_objects and alloc_space are the window's deltas,
//     inuse_* the current values; both scaled for sampling from the raw
//     counts, which are recovered exactly from runtime/pprof's scaled ones;
//   - mutex, block: contentions and delay since the previous call.
package rtprof
