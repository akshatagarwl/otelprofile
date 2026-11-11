package rtprof

import (
	"bytes"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/pprof/profile"
)

var sink [][]byte

//go:noinline
func allocOuter(n int) { allocInlined(n) }

// allocInlined is small enough to be inlined into allocOuter, so its
// location carries two lines.
func allocInlined(n int) { sink = append(sink, make([]byte, n)) }

//go:noinline
func allocDeep(depth int) {
	if depth == 0 {
		allocOuter(1 << 12)
		return
	}
	allocDeep(depth - 1)
}

// stacks maps "func;func;..." (leaf first) to the first sample value.
func stacks(t *testing.T, b []byte, filter string) map[string]int64 {
	t.Helper()
	p, err := profile.ParseData(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckValid(); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, s := range p.Sample {
		var names []string
		for _, l := range s.Location {
			for _, ln := range l.Line {
				names = append(names, ln.Function.Name)
			}
		}
		st := strings.Join(names, ";")
		if strings.Contains(st, filter) {
			out[st] += s.Value[0]
		}
	}
	return out
}

// TestHeapMatchesRuntimePprof checks the first heap profile, which covers
// everything since process start, against runtime/pprof's cumulative allocs
// profile: same stacks (inline frames included) and same scaled values.
func TestHeapMatchesRuntimePprof(t *testing.T) {
	for i := 0; i < 2000; i++ {
		allocOuter(1 << 12)
		allocDeep(3)
	}
	sink = nil
	runtime.GC()
	runtime.GC()
	h := NewHeapProfiler(NewSymbols())
	got := stacks(t, bytes.Clone(h.Profile()), "rtprof.alloc")
	var b bytes.Buffer
	if err := pprof.Lookup("allocs").WriteTo(&b, 0); err != nil {
		t.Fatal(err)
	}
	want := stacks(t, b.Bytes(), "rtprof.alloc")
	if len(want) == 0 {
		t.Fatal("runtime/pprof has no samples for the test functions")
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d, runtime/pprof %d", k, got[k], v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("extra stack %s", k)
		}
	}
	inlined := false
	for k := range got {
		inlined = inlined || strings.HasPrefix(k, "github.com/akshatagarwl/otelprofile-go/internal/rtprof.allocInlined;github.com/akshatagarwl/otelprofile-go/internal/rtprof.allocOuter")
	}
	if !inlined {
		t.Errorf("no stack with the inlined frame: %v", got)
	}

	// The next profile holds only what was allocated since.
	for i := 0; i < 500; i++ {
		allocOuter(1 << 12)
	}
	sink = nil
	runtime.GC()
	runtime.GC()
	delta := stacks(t, bytes.Clone(h.Profile()), "rtprof.allocOuter")
	var n int64
	for _, v := range delta {
		n += v
	}
	if n <= 0 || n > 1500 {
		t.Errorf("delta alloc_objects %d, want about 500", n)
	}
}

//go:noinline
func lockContended(mu *sync.Mutex) {
	mu.Lock()
	time.Sleep(50 * time.Microsecond)
	mu.Unlock()
}

func TestMutexMatchesRuntimePprof(t *testing.T) {
	defer runtime.SetMutexProfileFraction(runtime.SetMutexProfileFraction(1))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				lockContended(&mu)
			}
		}()
	}
	wg.Wait()
	m, err := NewMutexProfiler(NewSymbols())
	if err != nil {
		t.Fatal(err)
	}
	got := m.Profile()
	var b bytes.Buffer
	if err := pprof.Lookup("mutex").WriteTo(&b, 0); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []int{0, 1} {
		g, w := sum(t, got, "lockContended", typ), sum(t, b.Bytes(), "lockContended", typ)
		if w == 0 || g != w {
			t.Errorf("sample type %d: got %d, runtime/pprof %d", typ, g, w)
		}
	}
	if s := m.Stats(); s.Records == 0 || s.Samples == 0 {
		t.Errorf("stats %+v", s)
	}
}

func sum(t *testing.T, b []byte, fn string, typ int) int64 {
	p, err := profile.ParseData(b)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, s := range p.Sample {
		for _, l := range s.Location {
			for _, ln := range l.Line {
				if strings.Contains(ln.Function.Name, fn) {
					n += s.Value[typ]
					goto next
				}
			}
		}
	next:
	}
	return n
}

func TestCyclesPerSecond(t *testing.T) {
	v, err := CyclesPerSecond()
	if err != nil || v <= 0 {
		t.Fatalf("CyclesPerSecond() = %d, %v", v, err)
	}
	if _, err := parseCyclesPerSecond("--- mutex:\ncycles/second=abc\n"); err == nil {
		t.Error("parsed a bad header")
	}
	if v, err := parseCyclesPerSecond("--- mutex:\ncycles/second=2400000000\nsampling period=1\n"); err != nil || v != 2400000000 {
		t.Errorf("got %d, %v", v, err)
	}
}

// TestSteadyStateAllocs checks that a profile with nothing new allocates
// almost nothing: buffers, records and symbols are all reused.
func TestSteadyStateAllocs(t *testing.T) {
	runtime.GC()
	h := NewHeapProfiler(NewSymbols())
	h.Profile()
	h.Profile()
	if a := testing.AllocsPerRun(20, func() { h.Profile() }); a > 2 {
		t.Errorf("heap: %.0f allocs per profile", a)
	}
}
