package godeltaprof_test

import (
	"bytes"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/akshatagarwl/otelprofile-go/internal/rtprof"
	"github.com/google/pprof/profile"
	"github.com/grafana/pyroscope-go/godeltaprof"
)

// side holds one delta profiler per kind.
type side struct {
	name string
	read map[string]func() []byte
}

var kinds = []string{"heap", "mutex", "block"}

// godeltaprofSide is configured as otelprofile configured it before.
func godeltaprofSide() side {
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
	return side{"godeltaprof", map[string]func() []byte{
		"heap":  func() []byte { return run(func() error { return h.Profile(&buf) }) },
		"mutex": func() []byte { return run(func() error { return m.Profile(&buf) }) },
		"block": func() []byte { return run(func() error { return b.Profile(&buf) }) },
	}}
}

func rtprofSide(t testing.TB) side {
	read := func(p *rtprof.Profiler) func() []byte {
		return func() []byte {
			b, err := p.Profile()
			if err != nil {
				t.Fatal(err)
			}
			return bytes.Clone(b)
		}
	}
	return side{"rtprof", map[string]func() []byte{
		"heap":  read(rtprof.NewHeap()),
		"mutex": read(rtprof.NewMutex()),
		"block": read(rtprof.NewBlock()),
	}}
}

// workload reports whether a sample passes through workload_test.go, so
// it belongs to the workload rather than to the profilers, the comparison
// code or the runtime, whose activity lands on whichever side reads later.
func workload(s *profile.Sample) bool {
	for _, l := range s.Location {
		for _, ln := range l.Line {
			if strings.HasSuffix(ln.Function.Filename, "/test/godeltaprof/workload_test.go") {
				return true
			}
		}
	}
	return false
}

// compareMappings is whether both sides should write the same mappings.
// godeltaprof reads mappings only on Linux (/proc/self/maps) and writes one
// empty mapping elsewhere; runtime/pprof also reads them on macOS, so there
// rtprof's mappings carry more (the executable's path and address range)
// and are checked separately.
var compareMappings = runtime.GOOS == "linux"

// stackKey writes every field of a sample's labels and locations except
// the IDs, which are local to one profile: addresses, mappings, inline
// lines, function names, system names, file names and start lines.
func stackKey(s *profile.Sample) string {
	var b strings.Builder
	keys := make([]string, 0, len(s.NumLabel))
	for k := range s.NumLabel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "label %s=%v%v\n", k, s.NumLabel[k], s.NumUnit[k])
	}
	for _, l := range s.Location {
		fmt.Fprintf(&b, "loc %#x folded=%v", l.Address, l.IsFolded)
		if m := l.Mapping; m != nil && compareMappings {
			fmt.Fprintf(&b, " map{%#x-%#x off=%#x file=%q build=%q fn=%v file=%v line=%v inline=%v}",
				m.Start, m.Limit, m.Offset, m.File, m.BuildID, m.HasFunctions, m.HasFilenames, m.HasLineNumbers, m.HasInlineFrames)
		}
		b.WriteByte('\n')
		for _, ln := range l.Line {
			f := ln.Function
			fmt.Fprintf(&b, "  %s sys=%s %s:%d col=%d start=%d\n", f.Name, f.SystemName, f.Filename, ln.Line, ln.Column, f.StartLine)
		}
	}
	return b.String()
}

type parsed struct {
	stacks       map[string][]int64 // workload samples by stackKey
	depth        map[string]int     // frames (lines) per stack
	dups         map[string]int     // samples per stack
	all          []int64            // totals over every sample
	types        string
	period       string
	maxDepth     int
	shapes       int // workload frames with a [go.shape.…] name
	noStart      int // workload frames with start_line 0
	mapped       int // workload locations in a mapping with a file name
	locs         int // workload locations
	trimmed      int // workload stacks changed by trimRuntime
	placeholders int // workload stacks changed by dropPlaceholder
}

// hiddenRuntime is runtime/pprof's rule for the leading frames it hides in
// a heap stack: allocations the runtime makes on the caller's behalf (map
// growth, ...) are charged to the first frame outside the runtime.
// godeltaprof v0.1.12 carries an older copy of the rule that hides only
// "runtime." frames, so it keeps leading internal/runtime/... frames
// (internal/runtime/maps.newarray and the like). rtprof writes what
// runtime/pprof writes; the test applies runtime/pprof's rule to
// godeltaprof's heap stacks and counts the stacks it changes.
func hiddenRuntime(name string) bool {
	return strings.HasPrefix(name, "runtime.") || strings.HasPrefix(name, "internal/runtime/")
}

// trimRuntime applies runtime/pprof's rule to s and reports whether it
// changed it.
func trimRuntime(s *profile.Sample) bool {
	for i, l := range s.Location {
		if len(l.Line) == 0 || !hiddenRuntime(l.Line[0].Function.Name) {
			if i > 0 {
				s.Location = s.Location[i:]
				return true
			}
			return false
		}
	}
	return false // all runtime: runtime/pprof shows all frames
}

// dropPlaceholder removes the empty leading location (address
// 0xfffffffffffffffe, one line with no function) that godeltaprof writes
// on Go 1.26 for contention on runtime-internal locks: its copy of the
// profile reader does not strip the runtime's marker word from those
// stacks, which runtime/pprof does. It reports whether it removed one.
func dropPlaceholder(s *profile.Sample) bool {
	if len(s.Location) == 0 {
		return false
	}
	for _, ln := range s.Location[0].Line {
		if ln.Function != nil && ln.Function.Name != "" {
			return false
		}
	}
	s.Location = s.Location[1:]
	return true
}

func parse(t *testing.T, data []byte, trim bool) parsed {
	t.Helper()
	p, err := profile.ParseData(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckValid(); err != nil {
		t.Fatal(err)
	}
	r := parsed{stacks: map[string][]int64{}, depth: map[string]int{}, dups: map[string]int{}, all: make([]int64, len(p.SampleType))}
	for _, st := range p.SampleType {
		r.types += st.Type + "/" + st.Unit + " "
	}
	r.period = fmt.Sprintf("%s/%s %d", p.PeriodType.Type, p.PeriodType.Unit, p.Period)
	for _, s := range p.Sample {
		for i, v := range s.Value {
			r.all[i] += v
		}
		if !workload(s) {
			continue
		}
		if trim && trimRuntime(s) {
			r.trimmed++
		}
		if dropPlaceholder(s) {
			r.placeholders++
		}
		k := stackKey(s)
		if r.stacks[k] == nil {
			r.stacks[k] = make([]int64, len(s.Value))
		}
		for i, v := range s.Value {
			r.stacks[k][i] += v
		}
		r.dups[k]++
		if r.dups[k] > 1 {
			continue // frames are counted once per stack
		}
		n := 0
		for _, l := range s.Location {
			r.locs++
			if l.Mapping != nil && l.Mapping.File != "" && l.Mapping.Limit > l.Mapping.Start {
				r.mapped++
			}
			for _, ln := range l.Line {
				n++
				if strings.Contains(ln.Function.Name, "[go.shape.") {
					r.shapes++
				}
				if ln.Function.StartLine == 0 {
					r.noStart++
				}
			}
		}
		r.depth[k] = n
		r.maxDepth = max(r.maxDepth, n)
	}
	return r
}

// TestEquivalence runs godeltaprof and rtprof side by side over the same
// windows and checks that, for the workload's stacks, both write the same
// profiles field for field: the same set of stacks, each identical in
// depth, addresses, mappings, inline frames, function names (generic
// shapes included) and start lines, with values whose totals are within
// 1 %. The workload has stacks of about 70 and 100 frames and generic
// recursion.
func TestEquivalence(t *testing.T) {
	defer runtime.SetMutexProfileFraction(runtime.SetMutexProfileFraction(1))
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)

	g, r := godeltaprofSide(), rtprofSide(t)
	for _, k := range kinds { // drop everything since process start
		g.read[k]()
		r.read[k]()
	}
	const cycles = 6
	var maxDepth, shapes [2]int
	trimmed, placeholders := 0, 0
	for c := 0; c < cycles; c++ {
		allocWork(1)
		contendWork(64)
		runtime.GC()
		for _, kind := range kinds {
			// Alternate which side reads first.
			a, b := g.read[kind], r.read[kind]
			var gb, rb []byte
			if c%2 == 0 {
				gb, rb = a(), b()
			} else {
				rb, gb = b(), a()
			}
			gp, rp := parse(t, gb, kind == "heap"), parse(t, rb, false)
			name := fmt.Sprintf("cycle %d %s", c, kind)
			trimmed += gp.trimmed
			placeholders += gp.placeholders
			if rp.placeholders > 0 {
				t.Errorf("%s: rtprof wrote %d placeholder locations", name, rp.placeholders)
			}
			if gp.trimmed > 0 {
				t.Logf("%s: %d godeltaprof stacks had leading internal/runtime frames that runtime/pprof hides", name, gp.trimmed)
			}
			if gp.types != rp.types || gp.period != rp.period {
				t.Errorf("%s: sample types %q / %q, period %q / %q", name, gp.types, rp.types, gp.period, rp.period)
			}
			if len(gp.stacks) == 0 {
				t.Fatalf("%s: no workload samples", name)
			}
			compare(t, name, gp, rp)
			maxDepth[0], maxDepth[1] = max(maxDepth[0], gp.maxDepth), max(maxDepth[1], rp.maxDepth)
			shapes[0] += gp.shapes
			shapes[1] += rp.shapes
			if rp.mapped != rp.locs && (compareMappings || runtime.GOOS == "darwin") {
				t.Errorf("%s: %d of %d rtprof workload locations lack a file-backed mapping", name, rp.locs-rp.mapped, rp.locs)
			}
			if !compareMappings {
				t.Logf("%s: mappings not compared on %s; locations with a file-backed mapping: godeltaprof %d/%d, rtprof %d/%d",
					name, runtime.GOOS, gp.mapped, gp.locs, rp.mapped, rp.locs)
			}
			if rp.noStart != 0 {
				t.Errorf("%s: rtprof has %d workload frames with start_line 0 (godeltaprof %d)", name, rp.noStart, gp.noStart)
			}
			for i := range gp.all {
				t.Logf("%s all-stacks total %d: godeltaprof %d, rtprof %d (%s; includes the profilers' own work)",
					name, i, gp.all[i], rp.all[i], pct(rp.all[i], gp.all[i]))
			}
		}
	}
	t.Logf("deepest workload stack: godeltaprof %d frames, rtprof %d; generic-shape frames: %d, %d; godeltaprof heap stacks trimmed by runtime/pprof's rule: %d; godeltaprof placeholder frames dropped: %d",
		maxDepth[0], maxDepth[1], shapes[0], shapes[1], trimmed, placeholders)
	if maxDepth[1] < 60 || maxDepth[0] != maxDepth[1] {
		t.Errorf("deepest stack: godeltaprof %d, rtprof %d frames; want equal and ≥ 60", maxDepth[0], maxDepth[1])
	}
	if shapes[1] == 0 || shapes[0] != shapes[1] {
		t.Errorf("generic shape frames: godeltaprof %d, rtprof %d", shapes[0], shapes[1])
	}
}

// compare checks one profile pair.
func compare(t *testing.T, name string, g, r parsed) {
	t.Helper()
	var only []string
	for k := range g.stacks {
		if _, ok := r.stacks[k]; !ok {
			only = append(only, "godeltaprof only:\n"+k)
		}
	}
	for k := range r.stacks {
		if _, ok := g.stacks[k]; !ok {
			only = append(only, "rtprof only:\n"+k)
		}
	}
	if len(only) > 0 {
		sort.Strings(only)
		t.Errorf("%s: %d stacks differ (of %d / %d); first:\n%s", name, len(only), len(g.stacks), len(r.stacks), only[0])
	}
	var same, diff int
	tg, tr := make([]int64, len(g.all)), make([]int64, len(r.all))
	for k, gv := range g.stacks {
		rv := r.stacks[k]
		if g.depth[k] != r.depth[k] && rv != nil {
			t.Errorf("%s: depth %d / %d", name, g.depth[k], r.depth[k])
		}
		eq := rv != nil
		for i := range gv {
			tg[i] += gv[i]
			if rv != nil {
				eq = eq && gv[i] == rv[i]
			}
		}
		if eq {
			same++
		} else {
			diff++
			if rv != nil && (g.dups[k] < 2 || !rounding(gv, rv)) {
				t.Errorf("%s: values %v / %v for a stack godeltaprof writes %d times:\n%s", name, gv, rv, g.dups[k], k)
			}
		}
	}
	for _, rv := range r.stacks {
		for i := range rv {
			tr[i] += rv[i]
		}
	}
	for i := range tg {
		if d := rel(tr[i], tg[i]); d > 0.01 {
			t.Errorf("%s: workload total %d: godeltaprof %d, rtprof %d (%s)", name, i, tg[i], tr[i], pct(tr[i], tg[i]))
		}
	}
	t.Logf("%s: %d workload stacks, %d with equal values, %d differ; totals godeltaprof %v rtprof %v", name, len(g.stacks), same, diff, tg, tr)
}

// rounding reports whether two value vectors differ by at most one per
// value: godeltaprof scales each runtime record and truncates, and when
// runtime/pprof writes two records with the same stack (their frames differ
// only in hidden runtime frames), rtprof sees one stack and truncates once.
func rounding(a, b []int64) bool {
	for i := range a {
		if d := a[i] - b[i]; d < -1 || d > 1 {
			return false
		}
	}
	return true
}

func rel(a, b int64) float64 {
	if a == b {
		return 0
	}
	return math.Abs(float64(a-b)) / math.Max(math.Abs(float64(b)), 1)
}

func pct(a, b int64) string {
	if b == 0 {
		return fmt.Sprintf("%+d", a-b)
	}
	return fmt.Sprintf("%+.3f%%", 100*float64(a-b)/float64(b))
}
