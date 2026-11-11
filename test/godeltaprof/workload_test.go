// Package godeltaprof_test compares internal/rtprof, which reads the public
// runtime profiling API, with github.com/grafana/pyroscope-go/godeltaprof,
// which otelprofile used before v0.4 and which reaches into runtime/pprof
// through go:linkname. It is a separate module so that the main module does
// not depend on godeltaprof.
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

//go:noinline
func leafAlloc(path int) {
	b := make([]byte, 512+(path%4)*256)
	sinkMu.Lock()
	allocSink = append(allocSink, b)
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
	treeDepth = 9  // 2^9 = 512 distinct shallow stacks
	deepDepth = 24 // walk frames are 2 per level: these stacks pass 32
	deepPaths = 32
)

// allocWork allocates at every shallow path and some deep ones, n times.
func allocWork(n int) {
	for i := 0; i < n; i++ {
		for p := 0; p < 1<<treeDepth; p++ {
			walk(treeDepth, p, leafAlloc)
		}
		for p := 0; p < deepPaths; p++ {
			walk(deepDepth, p*7919, leafAlloc)
		}
	}
	sinkMu.Lock()
	allocSink = nil
	sinkMu.Unlock()
}

// contendWork makes mutex contention and channel blocking on paths.
func contendWork(paths int) {
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := 0; p < paths; p++ {
				walk(treeDepth, p, leafLock)
			}
		}()
	}
	for p := 0; p < paths; p++ {
		walk(treeDepth, p, leafBlock)
	}
	for p := 0; p < deepPaths/4; p++ {
		walk(deepDepth, p*7919, leafBlock)
	}
	wg.Wait()
}
