package godeltaprof_test

import (
	"bytes"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/akshatagarwl/otelprofile-go/internal/rtprof"
	"github.com/google/pprof/profile"
	"github.com/grafana/pyroscope-go/godeltaprof"
)

// paths holds one profiler of each kind on one side of the comparison.
type paths struct {
	heap, mutex, block func() []byte
}

func godeltaprofPaths() paths {
	opts := godeltaprof.ProfileOptions{GenericsFrames: true, LazyMappings: true}
	h := godeltaprof.NewHeapProfilerWithOptions(opts)
	m := godeltaprof.NewMutexProfilerWithOptions(opts)
	b := godeltaprof.NewBlockProfilerWithOptions(opts)
	var buf bytes.Buffer
	run := func(f func() error) []byte {
		buf.Reset()
		if err := f(); err != nil {
			panic(err)
		}
		return bytes.Clone(buf.Bytes())
	}
	return paths{
		heap:  func() []byte { return run(func() error { return h.Profile(&buf) }) },
		mutex: func() []byte { return run(func() error { return m.Profile(&buf) }) },
		block: func() []byte { return run(func() error { return b.Profile(&buf) }) },
	}
}

func rtprofPaths(t testing.TB) (paths, *rtprof.HeapProfiler, *rtprof.MutexProfiler, *rtprof.MutexProfiler) {
	s := rtprof.NewSymbols()
	h := rtprof.NewHeapProfiler(s)
	m, err := rtprof.NewMutexProfiler(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := rtprof.NewBlockProfiler(s)
	if err != nil {
		t.Fatal(err)
	}
	return paths{
		heap:  func() []byte { return bytes.Clone(h.Profile()) },
		mutex: func() []byte { return bytes.Clone(m.Profile()) },
		block: func() []byte { return bytes.Clone(b.Profile()) },
	}, h, m, b
}

// totals sums every sample type over all samples ("all") and over samples
// whose stack contains fn, and counts samples whose stack is deeper than
// rtprof.MaxStackDepth frames.
type totals map[string]float64

func addTotals(t testing.TB, tot totals, kind string, data []byte, fns ...string) {
	p, err := profile.ParseData(data)
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	if err := p.CheckValid(); err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	for _, s := range p.Sample {
		var names []string
		frames := 0
		for _, l := range s.Location {
			for _, ln := range l.Line {
				name := ln.Function.Name
				if i := strings.IndexByte(name, '['); i >= 0 && strings.Contains(name, "go.shape") {
					name = name[:i] + "[...]" // godeltaprof writes the linker's shape name
				}
				names = append(names, name)
				frames++
			}
		}
		st := strings.Join(names, ";")
		if root := names[len(names)-1]; len(s.Value) > 0 && frames < rtprof.MaxStackDepth &&
			!strings.HasSuffix(root, ".walk") && !strings.HasSuffix(root, ".left") && !strings.HasSuffix(root, ".right") {
			// Per-stack totals for whole stacks (not cut inside the
			// workload's call tree by the 32-entry cap), with generic
			// shape names reduced to [...].
			tot["stack/"+kind+"/"+st] += float64(s.Value[0])
		}
		for i, v := range s.Value {
			typ := p.SampleType[i].Type
			tot[kind+"/"+typ+"/all"] += float64(v)
			if !profilerOwn(st) {
				tot[kind+"/"+typ+"/app"] += float64(v)
			}
			for _, fn := range fns {
				if strings.Contains(st, fn) {
					tot[kind+"/"+typ+"/"+fn] += float64(v)
				}
			}
			if i == 1 { // alloc_space or delay
				tot[kind+"/value"] += float64(v)
				if frames > rtprof.MaxStackDepth {
					tot[kind+"/value_deeper_than_32"] += float64(v)
				}
			}
		}
		tot[kind+"/samples"]++
		if frames > rtprof.MaxStackDepth {
			tot[kind+"/samples_deeper_than_32"]++
		}
	}
}

// TestEquivalence runs godeltaprof and rtprof in one process over the same
// windows and checks that their totals agree within 1%.
func TestEquivalence(t *testing.T) {
	defer runtime.SetMutexProfileFraction(runtime.SetMutexProfileFraction(1))
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)

	gd := godeltaprofPaths()
	rt, rh, rm, rb := rtprofPaths(t)
	// Prime both: the first profile covers the time before the test.
	runtime.GC()
	for _, p := range []paths{gd, rt} {
		p.heap()
		p.mutex()
		p.block()
	}
	tg, tr := totals{}, totals{}
	var trunc [3]rtprof.Stats
	type side struct {
		p   paths
		tot totals
	}
	for window := 0; window < 4; window++ {
		allocWork(4)
		contendWork(64)
		runtime.GC() // both read the heap profile of the same GC cycle
		// Alternate which side reads first, so contention between the two
		// reads lands on both sides equally.
		sides := []side{{gd, tg}, {rt, tr}}
		if window%2 == 1 {
			sides[0], sides[1] = sides[1], sides[0]
		}
		// Read everything back to back, parse afterwards: parsing
		// allocates, which can start a GC that publishes new heap records
		// between the two sides' reads.
		var got [2][3][]byte
		for i, side := range sides {
			got[i][0] = side.p.heap()
		}
		for i, side := range sides {
			got[i][1] = side.p.mutex()
			got[i][2] = side.p.block()
		}
		for i, side := range sides {
			addTotals(t, side.tot, "heap", got[i][0], "leafAlloc")
			addTotals(t, side.tot, "mutex", got[i][1], "leafLock")
			addTotals(t, side.tot, "block", got[i][2], "leafBlock")
		}
		for i, s := range []rtprof.Stats{rh.Stats(), rm.Stats(), rb.Stats()} {
			trunc[i].Records += s.Records
			trunc[i].Truncated += s.Truncated
			trunc[i].Value += s.Value
			trunc[i].TruncVal += s.TruncVal
		}
	}

	keys := []string{
		"heap/alloc_objects/all", "heap/alloc_space/all", "heap/inuse_objects/all", "heap/inuse_space/all",
		"heap/alloc_objects/app", "heap/alloc_space/app", "heap/alloc_objects/leafAlloc", "heap/alloc_space/leafAlloc",
		"mutex/contentions/all", "mutex/delay/all", "mutex/contentions/app", "mutex/delay/app", "mutex/contentions/leafLock", "mutex/delay/leafLock",
		"block/contentions/all", "block/delay/all", "block/contentions/leafBlock", "block/delay/leafBlock",
	}
	t.Logf("| total | godeltaprof | rtprof | diff |")
	for _, k := range keys {
		a, b := tg[k], tr[k]
		d := 0.0
		if a != 0 {
			d = (b - a) / a
		}
		t.Logf("| %s | %.0f | %.0f | %+.3f%% |", k, a, b, 100*d)
		// Totals over all stacks include both profilers' own garbage and
		// runtime-internal lock contention (see profilerOwn), which land
		// on whichever side reads after they happen: reported, not
		// checked. "app" leaves them out.
		limit := 0.01
		if strings.HasSuffix(k, "/all") {
			limit = math.Inf(1)
		}
		if a == 0 {
			t.Errorf("%s: godeltaprof reported nothing", k)
		} else if math.Abs(d) > limit {
			t.Errorf("%s: %.3f%% apart", k, 100*d)
		}
	}
	same, diff, ex := compareStacks(tg, tr)
	t.Logf("per-stack first values (whole stacks): %d equal, %d differ %v", same, diff, ex)
	for i, kind := range []string{"heap", "mutex", "block"} {
		s := trunc[i]
		t.Logf("%s depth: godeltaprof samples deeper than %d frames %.0f/%.0f (value %s); rtprof records exactly %d deep %d/%d (value %s)",
			kind, rtprof.MaxStackDepth, tg[kind+"/samples_deeper_than_32"], tg[kind+"/samples"],
			pct(tg[kind+"/value_deeper_than_32"], tg[kind+"/value"]),
			rtprof.MaxStackDepth, s.Truncated, s.Records, pct(float64(s.TruncVal), float64(s.Value)))
	}
}

// compareStacks counts per-stack values that differ between the sides.
func compareStacks(a, b totals) (same, diff int, examples []string) {
	keys := map[string]bool{}
	for k := range a {
		if strings.HasPrefix(k, "stack/") {
			keys[k] = true
		}
	}
	for k := range b {
		if strings.HasPrefix(k, "stack/") {
			keys[k] = true
		}
	}
	for k := range keys {
		if a[k] == b[k] {
			same++
			continue
		}
		diff++
		if len(examples) < 5 {
			examples = append(examples, fmt.Sprintf("%s: %.0f vs %.0f", k[:min(len(k), 160)], a[k], b[k]))
		}
	}
	return
}

// profilerOwn reports stacks that belong to either profiler, to the test's
// pprof parser, or to the runtime's own locks. Which side sees them depends on whether a GC
// published them before or after that side's read.
func profilerOwn(st string) bool {
	return strings.Contains(st, "pyroscope-go/godeltaprof") || strings.Contains(st, "internal/rtprof") ||
		strings.Contains(st, "google/pprof/profile") ||
		// Contention on runtime-internal locks, e.g. GC during a read. On
		// Go 1.26 godeltaprof writes these stacks with an extra unnamed
		// leaf frame, hence the TrimPrefix.
		strings.HasPrefix(strings.TrimPrefix(st, ";"), "runtime.unlock;")
}

func pct(a, b float64) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*a/b)
}
