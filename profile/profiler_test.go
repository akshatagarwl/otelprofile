package profile

import (
	"context"
	"errors"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"testing"
	"time"

	gpprof "github.com/google/pprof/profile"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
)

type memExporter struct {
	mu       sync.Mutex
	recs     []Record
	calls    int
	err      error
	block    chan struct{}
	panicNow bool
	shut     bool
}

func (m *memExporter) Export(ctx context.Context, _ *resource.Resource, _ instrumentation.Scope, recs []Record) error {
	if m.panicNow {
		panic("boom")
	}
	if m.block != nil {
		select {
		case <-m.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.recs = append(m.recs, recs...)
	return m.err
}
func (m *memExporter) ForceFlush(context.Context) error { return nil }
func (m *memExporter) Shutdown(context.Context) error {
	m.mu.Lock()
	m.shut = true
	m.mu.Unlock()
	return nil
}
func (m *memExporter) types() map[Type]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[Type]int{}
	for _, r := range m.recs {
		out[r.Type]++
	}
	return out
}

//go:noinline
func spin(d time.Duration) {
	x := 0
	for end := time.Now().Add(d); time.Now().Before(end); {
		x++
	}
	_ = x
}

func TestAllTypesCollectedAndParse(t *testing.T) {
	exp := &memExporter{}
	p := NewProfiler(WithExporter(exp), WithTypes(AllTypes...), WithInterval(200*time.Millisecond))
	var mu sync.Mutex
	for i := 0; i < 3; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				mu.Lock()
				time.Sleep(time.Millisecond)
				mu.Unlock()
			}
		}()
	}
	spin(500 * time.Millisecond)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := exp.types()
	for _, ty := range AllTypes {
		if got[ty] == 0 {
			t.Errorf("no %s records: %v", ty, got)
		}
	}
	for _, r := range exp.recs {
		if _, err := gpprof.ParseData(r.Pprof); err != nil {
			t.Fatalf("%s record does not parse: %v", r.Type, err)
		}
		if r.Duration <= 0 || r.Start.IsZero() {
			t.Fatalf("%s record has no window: %+v", r.Type, r.Start)
		}
	}
	if !exp.shut {
		t.Fatal("exporter not shut down")
	}
}

func TestShutdownRestoresRates(t *testing.T) {
	runtime.SetMutexProfileFraction(0)
	p := NewProfiler(WithExporter(&memExporter{}), WithTypes(TypeMutex, TypeBlock), WithInterval(time.Hour))
	if got := runtime.SetMutexProfileFraction(-1); got != DefaultMutexProfileFraction {
		t.Fatalf("mutex fraction while running = %d", got)
	}
	_ = p.Shutdown(context.Background())
	if got := runtime.SetMutexProfileFraction(-1); got != 0 {
		t.Fatalf("mutex fraction not restored: %d", got)
	}

	runtime.SetMutexProfileFraction(3)
	defer runtime.SetMutexProfileFraction(0)
	p = NewProfiler(WithExporter(&memExporter{}), WithTypes(TypeMutex), WithInterval(time.Hour))
	if got := runtime.SetMutexProfileFraction(-1); got != 3 {
		t.Fatalf("a fraction the app set must be kept, got %d", got)
	}
	_ = p.Shutdown(context.Background())
	if got := runtime.SetMutexProfileFraction(-1); got != 3 {
		t.Fatalf("app's fraction changed: %d", got)
	}
}

func TestCPUProfilerBusyIsReportedNotFatal(t *testing.T) {
	var buf safeBuf
	if err := pprof.StartCPUProfile(&buf); err != nil {
		t.Skip("cpu profiler already in use")
	}
	defer pprof.StopCPUProfile()
	var errs []error
	var mu sync.Mutex
	exp := &memExporter{}
	p := NewProfiler(WithExporter(exp), WithTypes(TypeCPU, TypeGoroutine), WithInterval(100*time.Millisecond),
		WithErrorHandler(func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() }))
	time.Sleep(350 * time.Millisecond)
	_ = p.Shutdown(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 1 {
		t.Fatalf("want exactly one cpu error reported, got %v", errs)
	}
	if exp.types()[TypeGoroutine] == 0 {
		t.Fatal("other types must keep flowing")
	}
}

type safeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func TestExporterPanicIsRecovered(t *testing.T) {
	var got error
	var mu sync.Mutex
	p := NewProfiler(WithExporter(&memExporter{panicNow: true}), WithTypes(TypeGoroutine),
		WithInterval(50*time.Millisecond), WithErrorHandler(func(err error) { mu.Lock(); got = err; mu.Unlock() }))
	time.Sleep(200 * time.Millisecond)
	err := p.ForceFlush(context.Background())
	_ = p.Shutdown(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if got == nil && err == nil {
		t.Fatal("panic was not reported")
	}
}

func TestHungExporterNeverBlocksShutdownPastDeadline(t *testing.T) {
	exp := &memExporter{block: make(chan struct{})}
	defer close(exp.block)
	p := NewProfiler(WithExporter(exp), WithTypes(TypeGoroutine), WithInterval(20*time.Millisecond),
		WithExportTimeout(time.Hour), WithErrorHandler(func(error) {}))
	time.Sleep(150 * time.Millisecond) // queue fills; collection keeps going and drops
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.Shutdown(ctx)
	if time.Since(start) > 2*time.Second {
		t.Fatalf("shutdown took %v", time.Since(start))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
}

func TestShutdownIsIdempotentAndFlushAfterShutdownFails(t *testing.T) {
	p := NewProfiler(WithExporter(&memExporter{}), WithTypes(TypeGoroutine), WithInterval(time.Hour))
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatalf("flush after shutdown should be a quiet no-op, got %v", err)
	}
}

func TestForceFlushExportsCurrentWindow(t *testing.T) {
	exp := &memExporter{}
	p := NewProfiler(WithExporter(exp), WithTypes(TypeHeap, TypeGoroutine), WithInterval(time.Hour))
	defer func() { _ = p.Shutdown(context.Background()) }()
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exp.types()[TypeGoroutine] != 1 || exp.types()[TypeHeap] != 1 {
		t.Fatalf("flush exported %v", exp.types())
	}
}

func TestParseTypes(t *testing.T) {
	ts, err := ParseTypes("cpu, alloc_space,inuse_objects,goroutines,mutex_count,block")
	if err != nil {
		t.Fatal(err)
	}
	want := []Type{TypeCPU, TypeHeap, TypeGoroutine, TypeMutex, TypeBlock}
	if len(ts) != len(want) {
		t.Fatalf("%v", ts)
	}
	for i := range want {
		if ts[i] != want[i] {
			t.Fatalf("%v", ts)
		}
	}
	if _, err := ParseTypes("nope"); err == nil {
		t.Fatal("want error")
	}
	all, _ := ParseTypes("all")
	if len(all) != len(AllTypes) {
		t.Fatal("all")
	}
}

//go:noinline
func parkedForTest(ch chan struct{}) { <-ch }

// TestGoroutineAndCPUMatchGroundTruth checks collected values against what
// the runtime itself reports.
func TestGoroutineAndCPUMatchGroundTruth(t *testing.T) {
	ch := make(chan struct{})
	defer close(ch)
	for i := 0; i < 100; i++ {
		go parkedForTest(ch)
	}
	time.Sleep(50 * time.Millisecond)
	exp := &memExporter{}
	p := NewProfiler(WithExporter(exp), WithTypes(TypeGoroutine, TypeCPU), WithInterval(time.Hour))
	cpu0 := processCPU()
	spin(time.Second)
	used := processCPU() - cpu0
	want := runtime.NumGoroutine()
	if err := p.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = p.Shutdown(context.Background())
	seen := map[Type]bool{}
	for _, r := range exp.recs {
		if seen[r.Type] {
			continue // the flushed window, not the short one Shutdown adds
		}
		seen[r.Type] = true
		pp, err := gpprof.ParseData(r.Pprof)
		if err != nil {
			t.Fatal(err)
		}
		var total, parked, spun int64
		for _, s := range pp.Sample {
			v := s.Value[len(s.Value)-1]
			total += v
			for _, l := range s.Location {
				for _, ln := range l.Line {
					switch ln.Function.Name {
					case "github.com/akshatagarwl/otelprofile/profile.parkedForTest":
						parked += v
					case "github.com/akshatagarwl/otelprofile/profile.spin":
						spun += v
					}
				}
			}
		}
		switch r.Type {
		case TypeGoroutine:
			if parked != 100 {
				t.Errorf("parked goroutines = %d, want 100", parked)
			}
			if d := total - int64(want); d < -3 || d > 3 {
				t.Errorf("goroutines = %d, runtime says %d", total, want)
			}
		case TypeCPU:
			// The profile should account for the CPU the process used (100 Hz
			// sampling, so within 25%), and almost all of it in spin.
			if r := float64(total) / float64(used); r < 0.75 || r > 1.25 {
				t.Errorf("profiled cpu %v vs process cpu %v", time.Duration(total), used)
			}
			if float64(spun) < 0.7*float64(total) {
				t.Errorf("cpu in spin = %v of %v", time.Duration(spun), time.Duration(total))
			}
		}
	}
}

func processCPU() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func TestDefaultCPUWindowIsTwoIntervals(t *testing.T) {
	exp := &memExporter{}
	interval := 100 * time.Millisecond
	p := NewProfiler(WithExporter(exp), WithTypes(TypeCPU, TypeGoroutine), WithInterval(interval))
	spin(1050 * time.Millisecond)
	_ = p.Shutdown(context.Background())
	// CPU windows start about two intervals apart. (A window's Duration is
	// shorter than its spacing: runtime/pprof's StopCPUProfile waits up to
	// ~100ms for its writer before the next profile can start.) Ticks can
	// arrive late on a loaded machine, so allow some slack.
	var starts []time.Time
	gor := 0
	exp.mu.Lock()
	for _, r := range exp.recs {
		switch r.Type {
		case TypeCPU:
			starts = append(starts, r.Start)
		case TypeGoroutine:
			gor++
		}
	}
	exp.mu.Unlock()
	if len(starts) < 3 || len(starts) >= gor {
		t.Fatalf("cpu windows %d, goroutine windows %d", len(starts), gor)
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < interval*14/10 {
			t.Fatalf("cpu windows %d and %d start %v apart, want about 2 × %v", i-1, i, gap, interval)
		}
	}
}
