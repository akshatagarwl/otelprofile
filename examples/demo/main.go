// Command demo is a small service with a fixed, repeatable workload. It runs
// with either otelprofile (OTLP) or pyroscope-go so the two can be compared on
// the same code:
//
//	demo -profiler=otel      -service=demo-otel
//	demo -profiler=pyroscope -service=demo-pyro
//
// The workload: an HTTP server whose /fast and /slow handlers burn CPU and
// allocate (each request traced and labelled with its endpoint), a client
// that calls them at a fixed rate, a mutex-contention loop, a channel-blocking
// loop, and parked goroutines.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/akshatagarwl/otelprofile/profile"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var (
	mode     = flag.String("profiler", "otel", "otel | pyroscope | none")
	service  = flag.String("service", "", "service name (default demo-<profiler>)")
	endpoint = flag.String("endpoint", "http://localhost:4318", "OTLP/HTTP base URL (otel) or Pyroscope server address (pyroscope)")
	rps      = flag.Int("rps", 20, "requests per second from the built-in client")
	duration = flag.Duration("duration", 0, "exit after this long (0 = run until interrupted)")
	interval = flag.Duration("interval", 15*time.Second, "profile upload interval")
	types    = flag.String("types", "all", "profile types (otel names or 'all')")
	tracing  = flag.Bool("tracing", true, "export traces (needed for trace-to-profile links)")
	mutexF   = flag.Int("mutex-fraction", 5, "runtime.SetMutexProfileFraction")
	blockR   = flag.Int("block-rate", 10000, "runtime.SetBlockProfileRate (ns)")
	phase    = flag.Duration("phase", 2*time.Minute, "alternate mode: length of each profiler's turn")
	cpuWin   = flag.Duration("cpu-window", 0, "otelprofile CPU window (0 = interval)")
	dumpDir  = flag.String("dump", "", "also write every raw profile record to this directory (otel mode)")
	rtDeltas = flag.Bool("runtime-deltas", false, "otelprofile: heap/mutex/block deltas from runtime/pprof instead of godeltaprof")
	memRate  = flag.Int("memprofilerate", 0, "set runtime.MemProfileRate before the workload starts (0 = Go default, 512 KiB)")
	statsF   = flag.Duration("stats", 0, "print resource usage as JSON every interval and at exit (0 = only at exit)")
	arrivals = flag.String("arrivals", "ticker", "request timing: ticker (every 1/rps) | poisson (random gaps, mean 1/rps)")
)

var started = time.Now()

// printStats reports the process's own CPU time, peak RSS and allocation
// totals, for overhead comparisons between profilers.
func printStats(tag string) {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	cpu := time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	maxrss := ru.Maxrss // bytes on darwin, KiB on linux
	if runtime.GOOS == "linux" {
		maxrss *= 1024
	}
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}, {Name: "/gc/heap/allocs:objects"}, {Name: "/gc/cycles/total:gc-cycles"}, {Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/free:bytes"}, {Name: "/memory/classes/heap/released:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"},
		{Name: "/memory/classes/profiling/buckets:bytes"}, {Name: "/memory/classes/metadata/other:bytes"}, {Name: "/memory/classes/other:bytes"}, {Name: "/gc/heap/goal:bytes"}, {Name: "/sched/goroutines:goroutines"}}
	metrics.Read(s)
	mb := func(i int) float64 { return float64(s[i].Value.Uint64()) / (1 << 20) }
	defer fmt.Printf(`{"mem_detail":%q,"objects":%.1f,"free":%.1f,"released":%.1f,"stacks":%.1f,"prof_buckets":%.1f,"meta_other":%.1f,"other":%.1f,"heap_goal":%.1f,"goroutines":%d}`+"\n",
		*mode, mb(4), mb(5), mb(6), mb(7), mb(8), mb(9), mb(10), mb(11), s[12].Value.Uint64())
	fmt.Printf(`{"stats":%q,"profiler":%q,"uptime_s":%.0f,"cpu_s":%.3f,"maxrss_mb":%.1f,"alloc_mb":%.1f,"alloc_objects":%d,"gc_cycles":%d,"runtime_mem_mb":%.1f}`+"\n",
		tag, *mode, time.Since(started).Seconds(), cpu.Seconds(), float64(maxrss)/(1<<20),
		float64(s[0].Value.Uint64())/(1<<20), s[1].Value.Uint64(), s[2].Value.Uint64(), float64(s[3].Value.Uint64())/(1<<20))
}

func main() {
	flag.Parse()
	if *memRate > 0 {
		runtime.MemProfileRate = *memRate
	}
	if *service == "" {
		*service = "demo-" + *mode
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	tp := trace.TracerProvider(otel.GetTracerProvider())
	if *tracing {
		sdktp, err := newTracerProvider(ctx)
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = sdktp.Shutdown(c)
		}()
		tp = sdktp
	}

	ts, err := profile.ParseTypes(*types)
	if err != nil {
		log.Fatal(err)
	}
	switch *mode {
	case "otel":
		stop := startOtel(*service, ts)
		defer stop()
		tp = wrapTracerProvider(tp)
	case "pyroscope":
		runtime.SetMutexProfileFraction(*mutexF)
		runtime.SetBlockProfileRate(*blockR)
		stop := startPyroscope(*service, ts)
		defer stop()
	case "both":
		// Both profilers in one process see the same runtime state, which is
		// the fair comparison for heap, goroutine, mutex and block. Only one
		// can own the CPU profiler, so CPU is left out here.
		var rest []profile.Type
		for _, t := range ts {
			if t != profile.TypeCPU {
				rest = append(rest, t)
			}
		}
		runtime.SetMutexProfileFraction(*mutexF)
		runtime.SetBlockProfileRate(*blockR)
		stopP := startPyroscope(*service+"-pyro", rest)
		defer stopP()
		stopO := startOtel(*service+"-otel", rest)
		defer stopO()
	case "alternate":
		// CPU accuracy without process-to-process variance: the same process
		// alternates between pyroscope-go and otelprofile CPU profiling in
		// equal phases, so both see the same workload and the same kernel.
		altDone = make(chan struct{})
		go func() { defer close(altDone); alternate(ctx, *service, *phase) }()
	case "none":
	default:
		log.Fatalf("unknown -profiler %q", *mode)
	}

	if *statsF > 0 {
		go func() {
			t := time.NewTicker(*statsF)
			defer t.Stop()
			for range t.C {
				printStats("periodic")
			}
		}()
	}
	run(ctx, tp.Tracer("demo"))
	if altDone != nil {
		<-altDone // the last turn's profiler must export before exit
	}
	printStats("exit")
}

var altDone chan struct{}

func alternate(ctx context.Context, svc string, phase time.Duration) {
	cpu := []profile.Type{profile.TypeCPU}
	for i := 0; ctx.Err() == nil; i++ {
		var stop func()
		if i%2 == 0 {
			stop = startPyroscope(svc+"-pyro", cpu)
		} else {
			stop = startOtel(svc+"-otel", cpu)
		}
		select {
		case <-ctx.Done():
		case <-time.After(phase):
		}
		stop()
	}
}

// The profilers are linked in by otel.go and pyroscope.go; build tags
// nootel / nopyro leave one out, so each variant links only what it uses.
var (
	startOtel          = func(string, []profile.Type) func() { log.Fatal("built with nootel"); return nil }
	startPyroscope     = func(string, []profile.Type) func() { log.Fatal("built with nopyro"); return nil }
	wrapTracerProvider = func(tp trace.TracerProvider) trace.TracerProvider { return tp }
)

func newTracerProvider(ctx context.Context) (*sdktrace.TracerProvider, error) {
	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(*endpoint+"/v1/traces"))
	if err != nil {
		return nil, err
	}
	res, _ := resource.Merge(resource.Default(), resource.NewSchemaless(attribute.String("service.name", *service)))
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp, nil
}

func run(ctx context.Context, tracer trace.Tracer) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	handle := func(path string, work func(context.Context)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), "GET "+path, trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()
			// The same call as otelprofile.TagWrapper / pyroscope.TagWrapper, so
			// the workload links neither profiler.
			pprof.Do(ctx, pprof.Labels("endpoint", path), work)
			_, _ = fmt.Fprintln(w, "ok")
		})
	}
	handle("/fast", func(ctx context.Context) { fastHandler(ctx, tracer) })
	handle("/slow", func(ctx context.Context) { slowHandler(ctx, tracer) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	url := "http://" + ln.Addr().String()
	slog.Info("demo running", "service", *service, "addr", url)

	var wg sync.WaitGroup
	start := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	start(func() { client(ctx, url) })
	start(func() { allocator(ctx) })
	start(func() { contention(ctx) })
	start(func() { blocking(ctx) })
	for i := 0; i < 50; i++ {
		go parked(ctx)
	}
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	wg.Wait()
}

func client(ctx context.Context, url string) {
	gap := time.Second / time.Duration(max(*rps, 1))
	t := time.NewTicker(gap)
	defer t.Stop()
	c := &http.Client{Timeout: 5 * time.Second}
	for i := 0; ; i++ {
		var next <-chan time.Time = t.C
		if *arrivals == "poisson" {
			// Exponential gaps: requests don't line up with any timer, like
			// traffic from many independent clients.
			next = time.After(time.Duration(rand.ExpFloat64() * float64(gap)))
		}
		select {
		case <-ctx.Done():
			return
		case <-next:
		}
		path := "/fast"
		if i%4 == 3 {
			path = "/slow"
		}
		resp, err := c.Get(url + path)
		if err == nil {
			_ = resp.Body.Close()
		}
	}
}

var burnSink atomic.Uint64

//go:noinline
func burn(n int) {
	burnSink.Add(spinLCG(n))
}

//go:noinline
func spinLCG(n int) uint64 {
	var x uint64 = 1
	for i := 0; i < n; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	return x
}

var sinkMu sync.Mutex
var sink [][]byte

//go:noinline
func fastHandler(ctx context.Context, tracer trace.Tracer) {
	burn(2_000_000)
	keep(make([]byte, 4<<10))
}

//go:noinline
func slowHandler(ctx context.Context, tracer trace.Tracer) {
	_, span := tracer.Start(ctx, "query-db")
	queryDB()
	span.End()
	renderPage()
}

//go:noinline
func queryDB() { burn(6_000_000) }

//go:noinline
func renderPage() {
	burn(2_000_000)
	for i := 0; i < 16; i++ {
		keep(make([]byte, 16<<10))
	}
}

func keep(b []byte) {
	sinkMu.Lock()
	sink = append(sink, b)
	if len(sink) > 2000 {
		sink = sink[1000:]
	}
	sinkMu.Unlock()
}

//go:noinline
func allocator(ctx context.Context) {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			allocSmall()
			allocLarge()
		}
	}
}

//go:noinline
func allocSmall() {
	for i := 0; i < 100; i++ {
		keep(make([]byte, 128))
	}
}

//go:noinline
func allocLarge() { keep(make([]byte, 256<<10)) }

var contended sync.Mutex

//go:noinline
func contention(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				lockAndHold()
			}
		}()
	}
	wg.Wait()
}

//go:noinline
func lockAndHold() {
	contended.Lock()
	time.Sleep(time.Millisecond)
	contended.Unlock()
}

//go:noinline
func blocking(ctx context.Context) {
	ch := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				close(ch)
				return
			case <-t.C:
				ch <- struct{}{}
			}
		}
	}()
	for range ch {
		waitOnChannel(ch)
	}
}

//go:noinline
func waitOnChannel(ch chan struct{}) { <-ch }

//go:noinline
func parked(ctx context.Context) { <-ctx.Done() }
