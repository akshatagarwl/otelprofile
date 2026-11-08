package transform_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/grafana/pyroscope-go/godeltaprof"
)

var regenerate = flag.Bool("regenerate", false, "re-record the pprof input fixtures from the live runtime")

// fixtureNames are the pprof inputs under testdata/, recorded from a real Go
// runtime by TestRecordFixtures -regenerate.
var fixtureNames = []string{"cpu", "heap", "goroutine", "mutex", "block"}

//go:noinline
func burnA(n int) int {
	x := 0
	for i := 0; i < n; i++ {
		x += i * i % 7
	}
	return x
}

//go:noinline
func burnB(n int) int { return burnA(n / 2) }

var sink [][]byte
var sinkInt int

//go:noinline
func allocBig() { sink = append(sink, make([]byte, 1<<16)) }

//go:noinline
func allocSmall() { sink = append(sink, make([]byte, 64)) }

func TestRecordFixtures(t *testing.T) {
	if !*regenerate {
		t.Skip("pass -regenerate to re-record fixtures")
	}
	dir := "testdata"
	_ = os.MkdirAll(dir, 0o755)
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name+".pprof"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)
	defer runtime.SetMutexProfileFraction(0)
	defer runtime.SetBlockProfileRate(0)
	heap := godeltaprof.NewHeapProfiler()
	mutex := godeltaprof.NewMutexProfiler()
	block := godeltaprof.NewBlockProfiler()
	_ = heap.Profile(&bytes.Buffer{})
	_ = mutex.Profile(&bytes.Buffer{})
	_ = block.Profile(&bytes.Buffer{})

	var cpu bytes.Buffer
	if err := pprof.StartCPUProfile(&cpu); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	// Labelled CPU work: one region with a trace link, one with a plain label.
	wg.Add(2)
	go pprof.Do(context.Background(), pprof.Labels("otel.trace_id", "0af7651916cd43dd8448eb211c80319c", "otel.span_id", "b7ad6b7169203331", "endpoint", "/slow"), func(context.Context) {
		defer wg.Done()
		deadline := time.Now().Add(400 * time.Millisecond)
		for time.Now().Before(deadline) {
			sinkInt += burnA(1 << 16)
		}
	})
	go pprof.Do(context.Background(), pprof.Labels("endpoint", "/fast"), func(context.Context) {
		defer wg.Done()
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			sinkInt += burnB(1 << 16)
		}
	})
	// Contention.
	var mu sync.Mutex
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				mu.Lock()
				time.Sleep(10 * time.Microsecond)
				mu.Unlock()
			}
		}()
	}
	ch := make(chan int)
	go func() {
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Microsecond)
			ch <- i
		}
		close(ch)
	}()
	for range ch {
	}
	for i := 0; i < 2000; i++ {
		allocSmall()
		if i%10 == 0 {
			allocBig()
		}
	}
	// Parked goroutines with labels.
	for i := 0; i < 3; i++ {
		go pprof.Do(context.Background(), pprof.Labels("pool", "workers"), func(context.Context) { <-stop })
	}
	wg.Wait()
	pprof.StopCPUProfile()
	write("cpu", cpu.Bytes())
	runtime.GC()

	var b bytes.Buffer
	_ = heap.Profile(&b)
	write("heap", b.Bytes())
	b.Reset()
	_ = mutex.Profile(&b)
	write("mutex", b.Bytes())
	b.Reset()
	_ = block.Profile(&b)
	write("block", b.Bytes())
	b.Reset()
	_ = pprof.Lookup("goroutine").WriteTo(&b, 0)
	write("goroutine", b.Bytes())
	close(stop)
	sink = nil
}
