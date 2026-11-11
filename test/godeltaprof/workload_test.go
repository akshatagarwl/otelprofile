// Package godeltaprof_test compares internal/rtprof, which reads
// runtime/pprof's public output, with
// github.com/grafana/pyroscope-go/godeltaprof, which otelprofile used before
// and which reaches into the runtime through go:linkname. It is a separate
// module so that the main module does not depend on godeltaprof.
package godeltaprof_test

import (
	"runtime"
	"sync"
	"time"
)

func init() {
	// Sample more heap allocations than the 512 KiB default so the
	// workload creates many heap records quickly.
	runtime.MemProfileRate = 4096
}

var (
	allocSink [][]byte
	sinkMu    sync.Mutex
)

// The call tree: walk(depth, path) descends through left or right by the
// bits of path, so every path is a distinct stack. Leaves allocate, lock or
// block.

type leaf func(path int)

//go:noinline
func left(depth, path int, f leaf) {
	if depth == 0 {
		f(path)
		return
	}
	walk(depth-1, path>>1, f)
}

//go:noinline
func right(depth, path int, f leaf) {
	if depth == 0 {
		f(path)
		return
	}
	walk(depth-1, path>>1, f)
}

//go:noinline
func walk(depth, path int, f leaf) {
	if path&1 == 0 {
		left(depth, path, f)
	} else {
		right(depth, path, f)
	}
}

// genericDeep recurses n times through a generic function, so its frames
// carry shape names (genericDeep[go.shape.int], ...), then calls f.
//
//go:noinline
func genericDeep[T any](n int, v T, f leaf) {
	if n == 0 {
		f(n)
		return
	}
	genericDeep(n-1, v, f)
}

type pair struct{ A, B int }

//go:noinline
func leafAlloc(path int) {
	b := make([]byte, 512+(path%4)*256)
	sinkMu.Lock()
	allocSink = append(allocSink, b)
	sinkMu.Unlock()
}

var mapSink []map[int]int

// leafMap allocates inside the runtime (map growth), so its heap stacks
// start with internal/runtime/maps frames that runtime/pprof hides.
//
//go:noinline
func leafMap(path int) {
	m := map[int]int{}
	for i := 0; i < 64+path%64; i++ {
		m[i] = i
	}
	sinkMu.Lock()
	mapSink = append(mapSink, m)
	sinkMu.Unlock()
}

//go:noinline
func leafAllocGeneric(int) { allocGeneric[pair](8) }

//go:noinline
func allocGeneric[T any](n int) {
	s := make([]T, n*64)
	sinkMu.Lock()
	allocSink = append(allocSink, make([]byte, len(s)))
	sinkMu.Unlock()
}

var contended sync.Mutex

//go:noinline
func leafLock(int) {
	contended.Lock()
	time.Sleep(20 * time.Microsecond)
	contended.Unlock()
}

//go:noinline
func leafBlock(int) {
	ch := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Microsecond)
		close(ch)
	}()
	<-ch
}

// Shape of the workload.
const (
	treeDepth = 9 // 2^9 = 512 distinct shallow stacks
	// deepDepth levels of walk are 2 frames each: about 70 frames.
	deepDepth = 34
	deepPaths = 32
	// genericDepth frames of genericDeep: about 65 to 100 frames.
	genericDepth = 64
)

// deepWork runs f on the deep and generic stacks.
func deepWork(f leaf, paths int) {
	for p := 0; p < paths; p++ {
		walk(deepDepth, p*7919, f)
	}
	genericDeep(genericDepth, 1, f)
	genericDeep(genericDepth, "s", f)
	genericDeep(genericDepth+30, pair{}, f)
}

// allocWork allocates at every shallow path and at the deep and generic
// ones, n times.
func allocWork(n int) {
	for i := 0; i < n; i++ {
		for p := 0; p < 1<<treeDepth; p++ {
			walk(treeDepth, p, leafAlloc)
		}
		deepWork(leafAlloc, deepPaths)
		deepWork(leafAllocGeneric, 2)
		for p := 0; p < 64; p++ {
			walk(4, p, leafMap)
		}
	}
	sinkMu.Lock()
	allocSink, mapSink = nil, nil
	sinkMu.Unlock()
}

// allocPaths allocates once at the first n shallow paths.
func allocPaths(n int) {
	for p := 0; p < n; p++ {
		walk(treeDepth, p, leafAlloc)
	}
	sinkMu.Lock()
	allocSink = nil
	sinkMu.Unlock()
}

// contendWork makes mutex contention and channel blocking on paths, deep
// and generic stacks included.
func contendWork(paths int) {
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := 0; p < paths; p++ {
				walk(treeDepth, p, leafLock)
			}
			deepWork(leafLock, 4)
		}()
	}
	for p := 0; p < paths; p++ {
		walk(treeDepth, p, leafBlock)
	}
	deepWork(leafBlock, deepPaths/4)
	wg.Wait()
}
