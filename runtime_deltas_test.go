package otelprofile_test

import (
	"context"
	"math"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile/otlpprofilehttp"
	"github.com/akshatagarwl/otelprofile/profile"
)

var deltaSink [][]byte

//go:noinline
func deltaTestAlloc() { deltaSink = append(deltaSink, make([]byte, 64<<10)) }

//go:noinline
func deltaTestLock(mu *sync.Mutex) {
	mu.Lock()
	time.Sleep(200 * time.Microsecond)
	mu.Unlock()
}

//go:noinline
func deltaTestBlock(ch chan struct{}) { <-ch }

// TestRuntimeDeltasMatchGodeltaprof runs a godeltaprof profiler and a
// WithRuntimeDeltas profiler in the same process over the same windows and
// checks that the deltas they export for known functions agree.
func TestRuntimeDeltasMatchGodeltaprof(t *testing.T) {
	defer runtime.SetBlockProfileRate(0)
	var sa, sb sink
	srvA, srvB := sa.server(t), sb.server(t)
	defer srvA.Close()
	defer srvB.Close()
	newProfiler := func(url string, runtimeDeltas bool) *profile.Profiler {
		exp, err := otlpprofilehttp.New(context.Background(), otlpprofilehttp.WithEndpointURL(url))
		if err != nil {
			t.Fatal(err)
		}
		return profile.NewProfiler(profile.WithExporter(exp), profile.WithInterval(time.Hour),
			profile.WithTypes(profile.TypeHeap, profile.TypeMutex, profile.TypeBlock),
			profile.WithMutexProfileFraction(1), profile.WithBlockProfileRate(1),
			profile.WithRuntimeDeltas(runtimeDeltas))
	}
	a := newProfiler(srvA.URL, false)
	b := newProfiler(srvB.URL, true)
	defer func() { _ = a.Shutdown(context.Background()) }()
	defer func() { _ = b.Shutdown(context.Background()) }()

	for window := 0; window < 2; window++ {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					deltaTestLock(&mu)
				}
			}()
		}
		ch := make(chan struct{})
		go func() {
			for i := 0; i < 100; i++ {
				time.Sleep(100 * time.Microsecond)
				ch <- struct{}{}
			}
			close(ch)
		}()
		for range ch {
			deltaTestBlock(ch)
		}
		for i := 0; i < 200; i++ {
			deltaTestAlloc()
		}
		wg.Wait()
		deltaSink = nil
		runtime.GC() // both read the heap profile of the same GC cycle
		if err := a.ForceFlush(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := b.ForceFlush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	totals := func(s *sink) map[string]float64 {
		out := map[string]float64{}
		for _, r := range s.rows(t) {
			st := strings.Join(r.Stack, ";")
			for _, fn := range []string{"deltaTestAlloc", "deltaTestLock", "deltaTestBlock"} {
				if strings.Contains(st, fn) {
					out[r.Name+"/"+r.Type+"/"+fn] += float64(r.Value)
				}
			}
		}
		return out
	}
	ta, tb := totals(&sa), totals(&sb)
	for _, k := range []string{"heap/alloc_space/deltaTestAlloc", "heap/alloc_objects/deltaTestAlloc",
		"mutex/contentions/deltaTestLock", "mutex/delay/deltaTestLock",
		"block/contentions/deltaTestBlock", "block/delay/deltaTestBlock"} {
		if ta[k] == 0 {
			t.Fatalf("godeltaprof reported nothing for %s (all %v)", k, ta)
		}
		if d := math.Abs(tb[k]-ta[k]) / ta[k]; d > 0.02 {
			t.Errorf("%s: godeltaprof %.0f, runtime deltas %.0f (%.1f%% apart)", k, ta[k], tb[k], 100*d)
		} else {
			t.Logf("%s: godeltaprof %.0f, runtime deltas %.0f", k, ta[k], tb[k])
		}
	}
}
