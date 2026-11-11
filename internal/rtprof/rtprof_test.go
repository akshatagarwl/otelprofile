package rtprof

import (
	"bytes"
	"fmt"
	"math"
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

// genericDeep recurses through a generic function, so its frames carry
// shape names, then runs f about depth frames down.
//
//go:noinline
func genericDeep[T any](depth int, v T, f func()) {
	if depth == 0 {
		f()
		return
	}
	genericDeep(depth-1, v, f)
}

const testFile = "/internal/rtprof/rtprof_test.go"

// stacks maps each sample that passes through this file's workload
// functions (not the tests themselves, under which the runtime and the
// comparison do their own work) to a key holding every location field but
// the IDs, summing the given value.
func stacks(t *testing.T, b []byte, typ int) map[string]int64 {
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
		var k strings.Builder
		ours := false
		for _, l := range s.Location {
			m := l.Mapping
			fmt.Fprintf(&k, "%#x %#x-%#x %q|", l.Address, m.Start, m.Limit, m.File)
			for _, ln := range l.Line {
				ours = ours || strings.HasSuffix(ln.Function.Filename, testFile) && !strings.Contains(ln.Function.Name, ".Test")
				fmt.Fprintf(&k, "%s %s %s:%d@%d;", ln.Function.Name, ln.Function.SystemName, ln.Function.Filename, ln.Line, ln.Function.StartLine)
			}
			k.WriteByte('\n')
		}
		if ours {
			fmt.Fprintf(&k, "%v", s.NumLabel)
			out[k.String()] += s.Value[typ]
		}
	}
	return out
}

func cumulative(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := pprof.Lookup(name).WriteTo(&b, 0); err != nil {
		t.Fatal(err)
	}
	p, err := profile.ParseData(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := p.WriteUncompressed(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func sameStacks(t *testing.T, kind string, got, want map[string]int64) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: runtime/pprof has no samples for the test functions", kind)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d, runtime/pprof %d for\n%s", kind, got[k], v, k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: extra stack\n%s", kind, k)
		}
	}
}

// checkDepthAndNames checks that the deep generic stacks came through whole.
func checkDepthAndNames(t *testing.T, kind string, b []byte) {
	t.Helper()
	p, err := profile.ParseData(b)
	if err != nil {
		t.Fatal(err)
	}
	deepest, shapes, inlined := 0, 0, false
	for _, s := range p.Sample {
		n := 0
		for _, l := range s.Location {
			if len(l.Line) > 1 && l.Line[0].Function.Name == "github.com/akshatagarwl/otelprofile-go/internal/rtprof.allocInlined" {
				inlined = true
			}
			for _, ln := range l.Line {
				n++
				if strings.Contains(ln.Function.Name, "genericDeep[go.shape.") {
					shapes++
					if ln.Function.StartLine == 0 {
						t.Errorf("%s: %s has start_line 0", kind, ln.Function.Name)
					}
				}
			}
		}
		deepest = max(deepest, n)
	}
	if deepest < 80 || shapes == 0 {
		t.Errorf("%s: deepest stack %d frames, %d generic-shape frames; want ≥ 80 and > 0", kind, deepest, shapes)
	}
	if kind == "heap" && !inlined {
		t.Errorf("heap: no location with the inlined allocInlined frame")
	}
}

// TestHeapMatchesRuntimePprof checks the first heap profile, which covers
// everything since process start, against runtime/pprof's cumulative
// profile: the same stacks (≥ 80 frames, generic shapes, start lines,
// inline frames and mappings) with the same values, then that the next
// profile holds only what changed.
func TestHeapMatchesRuntimePprof(t *testing.T) {
	for i := 0; i < 2000; i++ {
		allocOuter(1 << 12)
		genericDeep(90, i, func() { allocOuter(1 << 10) })
		genericDeep(90, "s", func() { allocOuter(1 << 11) })
	}
	sink = nil
	runtime.GC()
	runtime.GC()
	h := NewHeap()
	first, err := h.Profile()
	if err != nil {
		t.Fatal(err)
	}
	first = bytes.Clone(first)
	want := cumulative(t, "heap")
	for typ := 0; typ < 4; typ++ {
		sameStacks(t, fmt.Sprintf("heap value %d", typ), stacks(t, first, typ), stacks(t, want, typ))
	}
	checkDepthAndNames(t, "heap", first)

	for i := 0; i < 500; i++ {
		allocOuter(1 << 12)
	}
	sink = nil
	runtime.GC()
	runtime.GC()
	next, err := h.Profile()
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for k, v := range stacks(t, next, 0) {
		if strings.Contains(k, "genericDeep") && v != 0 {
			t.Errorf("unchanged stack in the delta: %d\n%s", v, k)
		}
		if strings.Contains(k, "allocInlined") {
			n += v
		}
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

//go:noinline
func blockOnChan() {
	ch := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Microsecond)
		close(ch)
	}()
	<-ch
}

func TestMutexAndBlockMatchRuntimePprof(t *testing.T) {
	defer runtime.SetMutexProfileFraction(runtime.SetMutexProfileFraction(1))
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				genericDeep(90, g, func() { lockContended(&mu) })
			}
		}()
	}
	wg.Wait()
	for i := 0; i < 20; i++ {
		genericDeep(90, "s", blockOnChan)
	}
	for _, c := range []struct {
		name string
		p    *Profiler
	}{{"mutex", NewMutex()}, {"block", NewBlock()}} {
		got, err := c.p.Profile()
		if err != nil {
			t.Fatal(err)
		}
		got = bytes.Clone(got)
		want := cumulative(t, c.name)
		for typ := 0; typ < 2; typ++ {
			sameStacks(t, fmt.Sprintf("%s value %d", c.name, typ), stacks(t, got, typ), stacks(t, want, typ))
		}
		checkDepthAndNames(t, c.name, got)
		if s := c.p.Stats(); s.Samples == 0 || s.Written == 0 || s.Locations == 0 || s.Decoded != s.Samples {
			t.Errorf("%s stats %+v", c.name, s)
		}
		// Nothing changed since: the delta has no samples from this file.
		again, err := c.p.Profile()
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range stacks(t, again, 0) {
			if strings.Contains(k, "genericDeep") {
				t.Errorf("%s: unchanged stack in the delta: %d\n%s", c.name, v, k)
			}
		}
	}
}

// TestUnscale checks that the raw count is recovered exactly from
// runtime/pprof's scaled one, so deltas are scaled from raw counts as
// godeltaprof does.
func TestUnscale(t *testing.T) {
	for _, rate := range []int64{0, 1, 4096, 512 * 1024} {
		for _, size := range []int64{8, 24, 1000, 1 << 16, 1 << 20} {
			for n := int64(0); n < 3000; n++ {
				v, _ := scaleHeapSample(n, n*size, rate)
				if got := unscale(v, size, rate); got != n && n != 0 {
					t.Fatalf("rate %d size %d: unscale(%d) = %d, want %d", rate, size, v, got, n)
				}
			}
		}
	}
	if s := heapScale(1<<16, 512*1024); math.Abs(s-8.5104) > 1e-3 {
		t.Errorf("scale %f", s)
	}
}

func TestMalformed(t *testing.T) {
	// A truncated bytes field, a truncated varint, an unknown wire type.
	for _, b := range [][]byte{{0x12, 0x05, 0x0a}, {0x08, 0x80}, {0x0f}} {
		if _, err := nextField(b, 0); err == nil {
			t.Errorf("%x: no error", b)
		}
	}
	// A sample that refers to a location the profile does not have.
	p := NewMutex()
	if _, err := p.delta([]byte{0x12, 0x06, 0x08, 0x07, 0x10, 0x01, 0x10, 0x02}, nil); err == nil {
		t.Error("unknown location: no error")
	}
	// A location ID written twice.
	loc := []byte{0x22, 0x04, 0x08, 0x01, 0x18, 0x10}
	if _, err := p.delta(append(append([]byte{}, loc...), loc...), nil); err == nil {
		t.Error("duplicate location: no error")
	}
}

// TestSteadyStateAllocs reports the allocations of a profile with nothing
// new. Nearly all are runtime/pprof's own (its profile builder); the gzip
// reader and this package's buffers are reused.
func TestSteadyStateAllocs(t *testing.T) {
	runtime.GC()
	h := NewHeap()
	for i := 0; i < 3; i++ {
		if _, err := h.Profile(); err != nil {
			t.Fatal(err)
		}
	}
	a := testing.AllocsPerRun(20, func() { _, _ = h.Profile() })
	t.Logf("heap: %.0f allocs per profile", a)
}

// TestCacheIsTransparent checks that reusing the previous profile's
// decoded samples gives the same delta as decoding every sample.
func TestCacheIsTransparent(t *testing.T) {
	defer runtime.SetBlockProfileRate(0)
	runtime.SetBlockProfileRate(1)
	raw := func() []byte {
		for i := 0; i < 5; i++ {
			genericDeep(10+i, i, blockOnChan)
		}
		allocOuter(1 << 12)
		runtime.GC()
		g := NewBlock()
		if err := pprof.Lookup("block").WriteTo(&g.gz, 0); err != nil {
			t.Fatal(err)
		}
		if err := g.gunzip(); err != nil {
			t.Fatal(err)
		}
		return bytes.Clone(g.raws[g.cur].Bytes())
	}
	// One call site, so every round makes the same stacks; the first
	// round records them.
	var raws [][]byte
	for range 4 {
		raws = append(raws, raw())
	}
	cached, fresh := NewBlock(), NewBlock()
	reused := false
	var prev []byte
	for i, r := range raws[1:] {
		x, err := cached.delta(r, prev)
		if err != nil {
			t.Fatal(err)
		}
		x = bytes.Clone(x)
		clear(fresh.samples)
		y, err := fresh.delta(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(x, y) {
			t.Errorf("profile %d: cached and fresh deltas differ", i)
		}
		if i > 0 && cached.Stats().Decoded < cached.Stats().Samples {
			reused = true
		}
		prev = r
	}
	if !reused {
		t.Error("no sample was reused")
	}
}
