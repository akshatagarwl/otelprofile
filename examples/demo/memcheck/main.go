// Command memcheck measures, exactly, the heap a profiler keeps alive: it
// runs one profiler (otel or pyroscope) at a 1 s interval against a local
// sink for -duration with runtime.MemProfileRate = 1, then prints the
// in-use bytes of every allocation site outside the workload.
//
//	memcheck -profiler=otel
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"github.com/akshatagarwl/otelprofile"
	"github.com/google/pprof/profile"
	"github.com/grafana/pyroscope-go"
)

var sink [][]byte

//go:noinline
func workload(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		x := 0
		for i := 0; i < 1e6; i++ {
			x += i % 7
		}
		sink = append(sink, make([]byte, 4096))
		if len(sink) > 500 {
			sink = sink[250:]
		}
		time.Sleep(time.Millisecond)
	}
}

func main() {
	mode := flag.String("profiler", "otel", "otel | pyroscope | none")
	dur := flag.Duration("duration", 60*time.Second, "how long to run")
	flag.Parse()
	runtime.MemProfileRate = 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) }))
	defer srv.Close()
	stop := make(chan struct{})
	go workload(stop)
	var done func()
	switch *mode {
	case "otel":
		p, err := otelprofile.Start(otelprofile.Config{ServiceName: "memcheck", Endpoint: srv.URL, Interval: time.Second})
		if err != nil {
			panic(err)
		}
		done = func() { _ = p.Stop() }
	case "pyroscope":
		p, err := pyroscope.Start(pyroscope.Config{ApplicationName: "memcheck", ServerAddress: srv.URL, UploadRate: time.Second})
		if err != nil {
			panic(err)
		}
		done = func() { _ = p.Stop() }
	default:
		done = func() {}
	}
	time.Sleep(*dur)
	runtime.GC()
	runtime.GC()
	var buf bytes.Buffer
	_ = pprof.Lookup("heap").WriteTo(&buf, 0)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	done()
	close(stop)
	p, err := profile.Parse(&buf)
	if err != nil {
		panic(err)
	}
	idx := 0
	for i, st := range p.SampleType {
		if st.Type == "inuse_space" {
			idx = i
		}
	}
	bySite := map[string]int64{}
	var total int64
	for _, s := range p.Sample {
		v := s.Value[idx]
		var frames []string
		for _, l := range s.Location {
			for _, ln := range l.Line {
				frames = append(frames, ln.Function.Name)
			}
		}
		st := strings.Join(frames, ";")
		if strings.Contains(st, "main.workload") || strings.Contains(st, "main.main") && !strings.Contains(st, "otelprofile") && !strings.Contains(st, "pyroscope") {
			continue
		}
		total += v
		// Group by the first frame that belongs to a profiler or stdlib entry point.
		key := frames[len(frames)-1]
		for i := len(frames) - 1; i >= 0; i-- {
			if strings.Contains(frames[i], "otelprofile") || strings.Contains(frames[i], "pyroscope-go") || strings.Contains(frames[i], "net/http") || strings.Contains(frames[i], "runtime/pprof") {
				key = frames[i]
				break
			}
		}
		bySite[key] += v
	}
	type kv struct {
		k string
		v int64
	}
	var kvs []kv
	for k, v := range bySite {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	fmt.Printf("%s: non-workload in-use heap %d KB; HeapAlloc %d KB, HeapSys %d KB, NumGC %d, TotalAlloc %d MB\n", *mode, total/1024, ms.HeapAlloc/1024, ms.HeapSys/1024, ms.NumGC, ms.TotalAlloc>>20)
	for _, e := range kvs[:min(12, len(kvs))] {
		fmt.Printf("  %7d KB  %s\n", e.v/1024, e.k)
	}
	if focus := os.Getenv("FOCUS"); focus != "" {
		leaf := map[string]int64{}
		for _, s := range p.Sample {
			var frames []string
			for _, l := range s.Location {
				for _, ln := range l.Line {
					frames = append(frames, ln.Function.Name)
				}
			}
			st := strings.Join(frames, ";")
			if !strings.Contains(st, focus) {
				continue
			}
			n := min(4, len(frames))
			leaf[strings.Join(frames[:n], " < ")] += s.Value[idx]
		}
		var ls []kv
		for k, v := range leaf {
			ls = append(ls, kv{k, v})
		}
		sort.Slice(ls, func(i, j int) bool { return ls[i].v > ls[j].v })
		fmt.Println("focus", focus)
		for _, e := range ls[:min(10, len(ls))] {
			fmt.Printf("  %7d KB  %s\n", e.v/1024, e.k)
		}
	}
	_ = context.Background
}
