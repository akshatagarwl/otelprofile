package rtprof

import (
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
)

// MaxStackDepth is the deepest stack the public profiling API reports
// (len(runtime.StackRecord.Stack0)).
const MaxStackDepth = len(runtime.StackRecord{}.Stack0)

// entry is the state of one (stack, block size) key.
type entry struct {
	stk  []uintptr
	tag  int64
	hash uint64
	next *entry

	acc0, acc1   int64 // this profile: alloc/inuse objects, or count/cycles
	prev0, prev1 int64 // last profile: alloc objects, or count/delay ns
	touched      uint32

	locs     []uint64
	haveLocs bool
}

// table maps stacks to entries without allocating for known stacks.
type table struct {
	m       map[uint64]*entry
	cycle   uint32
	touched []*entry
}

func hashStack(stk []uintptr, tag int64) uint64 {
	const prime = 0x100000001b3
	h := uint64(0xcbf29ce484222325) ^ uint64(tag)
	for _, pc := range stk {
		h ^= uint64(pc)
		h *= prime
		h ^= h >> 29
	}
	return h
}

func equalStack(a, b []uintptr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (t *table) begin() {
	if t.m == nil {
		t.m = map[uint64]*entry{}
	}
	t.cycle++
	t.touched = t.touched[:0]
}

// lookup returns the entry for stk and tag, creating it on first sight, and
// records it as touched in this profile.
func (t *table) lookup(stk []uintptr, tag int64) *entry {
	h := hashStack(stk, tag)
	head := t.m[h]
	e := head
	for e != nil && (e.tag != tag || !equalStack(e.stk, stk)) {
		e = e.next
	}
	if e == nil {
		e = &entry{stk: append([]uintptr(nil), stk...), tag: tag, hash: h, next: head}
		t.m[h] = e
	}
	if e.touched != t.cycle {
		e.touched = t.cycle
		t.touched = append(t.touched, e)
	}
	return e
}

// Stats describes the last profile a Profiler read, for tests and
// benchmarks.
type Stats struct {
	Records   int   // runtime records read
	Truncated int   // of them, with a stack exactly MaxStackDepth deep
	Value     int64 // first sample value of the window (alloc_space bytes, or delay ns), over all keys
	TruncVal  int64 // of it, on keys whose stack is MaxStackDepth deep
	Samples   int   // samples written
}

// HeapProfiler writes delta heap profiles: alloc_objects and alloc_space
// since the previous call, inuse_objects and inuse_space as of the last GC.
type HeapProfiler struct {
	sym   *symbols
	w     *writer
	t     table
	recs  []runtime.MemProfileRecord
	stats Stats
}

// MutexProfiler writes delta mutex or block profiles (contentions and
// delay since the previous call).
type MutexProfiler struct {
	sym   *symbols
	w     *writer
	t     table
	recs  []runtime.BlockProfileRecord
	read  func([]runtime.BlockProfileRecord) (int, bool)
	ghz   float64
	stats Stats
}

// Symbols is a symbolization cache that the profilers of one process share.
type Symbols struct{ s *symbols }

// NewSymbols returns an empty cache.
func NewSymbols() Symbols { return Symbols{newSymbols()} }

// NewHeapProfiler returns a heap profiler. Its first Profile call covers
// the time since process start.
func NewHeapProfiler(s Symbols) *HeapProfiler {
	return &HeapProfiler{sym: s.s, w: newWriter(s.s)}
}

// NewMutexProfiler returns a mutex profiler.
func NewMutexProfiler(s Symbols) (*MutexProfiler, error) {
	return newCountCycle(s, runtime.MutexProfile)
}

// NewBlockProfiler returns a block profiler.
func NewBlockProfiler(s Symbols) (*MutexProfiler, error) {
	return newCountCycle(s, runtime.BlockProfile)
}

func newCountCycle(s Symbols, read func([]runtime.BlockProfileRecord) (int, bool)) (*MutexProfiler, error) {
	cps, err := CyclesPerSecond()
	if err != nil {
		return nil, err
	}
	return &MutexProfiler{sym: s.s, w: newWriter(s.s), read: read, ghz: float64(cps) / 1e9}, nil
}

// Stats returns the statistics of the last Profile call.
func (p *HeapProfiler) Stats() Stats { return p.stats }

// Stats returns the statistics of the last Profile call.
func (p *MutexProfiler) Stats() Stats { return p.stats }

var heapTypes = []valueType{{"alloc_objects", "count"}, {"alloc_space", "bytes"}, {"inuse_objects", "count"}, {"inuse_space", "bytes"}}

// Profile returns the delta heap profile, uncompressed. The bytes are valid
// until the next call.
func (p *HeapProfiler) Profile() []byte {
	n, ok := runtime.MemProfile(p.recs, true)
	for !ok {
		p.recs = make([]runtime.MemProfileRecord, n+n/4+50)
		n, ok = runtime.MemProfile(p.recs, true)
	}
	recs := p.recs[:n]
	p.stats = Stats{Records: n}
	p.t.begin()
	for i := range recs {
		r := &recs[i]
		if r.AllocObjects == 0 && r.FreeObjects == 0 {
			continue // a fresh bucket, published after the next GC cycles
		}
		var size int64
		if r.AllocObjects > 0 {
			size = r.AllocBytes / r.AllocObjects
		} else {
			size = r.FreeBytes / r.FreeObjects
		}
		stk := r.Stack()
		if len(stk) == MaxStackDepth {
			p.stats.Truncated++
		}
		e := p.t.lookup(stk, size)
		e.acc0 += r.AllocObjects
		e.acc1 += r.InUseObjects()
	}
	rate := int64(runtime.MemProfileRate)
	p.w.start(valueType{"space", "bytes"}, rate, heapTypes)
	var values [4]int64
	for _, e := range p.t.touched {
		accAlloc, accInuse := e.acc0, e.acc1
		e.acc0, e.acc1 = 0, 0
		if accAlloc == 0 && accInuse == 0 {
			continue
		}
		allocObjects := accAlloc - e.prev0
		e.prev0 = accAlloc
		if allocObjects < 0 {
			continue // cannot happen; treat as a counter reset
		}
		values[0], values[1] = scaleHeapSample(allocObjects, allocObjects*e.tag, rate)
		values[2], values[3] = scaleHeapSample(accInuse, accInuse*e.tag, rate)
		if values == [4]int64{} {
			continue
		}
		p.stats.Value += values[1]
		if len(e.stk) == MaxStackDepth {
			p.stats.TruncVal += values[1]
		}
		p.w.sample(values[:], p.heapLocs(e), e.tag)
		p.stats.Samples++
	}
	return p.w.finish()
}

// heapLocs returns the entry's locations, hiding runtime frames above the
// allocating function as runtime/pprof does.
func (p *HeapProfiler) heapLocs(e *entry) []uint64 {
	if e.haveLocs {
		return e.locs
	}
	stk := e.stk
	for i, addr := range stk {
		if f := runtime.FuncForPC(addr); f != nil && strings.HasPrefix(f.Name(), "runtime.") {
			continue
		}
		// Found non-runtime. Show any runtime uses above it.
		stk = stk[i:]
		break
	}
	locs := p.sym.locsForStack(stk)
	if len(locs) == 0 {
		locs = p.sym.locsForStack(e.stk) // try again, and show all frames
	}
	e.locs = append([]uint64(nil), locs...)
	e.haveLocs = true
	return e.locs
}

// scaleHeapSample is runtime/pprof's: it adjusts a heap sample for its
// probability of being sampled, 1-exp(-avgSize/rate).
func scaleHeapSample(count, size, rate int64) (int64, int64) {
	if count == 0 || size == 0 {
		return 0, 0
	}
	if rate <= 1 {
		// if rate==1 all samples were collected so no adjustment is needed.
		// if rate<1 treat as unknown and skip scaling.
		return count, size
	}
	avgSize := float64(size) / float64(count)
	scale := 1 / (1 - math.Exp(-avgSize/float64(rate)))
	return int64(float64(count) * scale), int64(float64(size) * scale)
}

var countCycleTypes = []valueType{{"contentions", "count"}, {"delay", "nanoseconds"}}

// Profile returns the delta mutex or block profile, uncompressed. The bytes
// are valid until the next call.
func (p *MutexProfiler) Profile() []byte {
	n, ok := p.read(p.recs)
	for !ok {
		p.recs = make([]runtime.BlockProfileRecord, n+n/4+50)
		n, ok = p.read(p.recs)
	}
	recs := p.recs[:n]
	p.stats = Stats{Records: n}
	p.t.begin()
	for i := range recs {
		r := &recs[i]
		stk := r.Stack()
		if len(stk) == MaxStackDepth {
			p.stats.Truncated++
		}
		e := p.t.lookup(stk, 0)
		e.acc0 += r.Count
		e.acc1 += r.Cycles
	}
	p.w.start(valueType{"contentions", "count"}, 1, countCycleTypes)
	var values [2]int64
	for _, e := range p.t.touched {
		count, cycles := e.acc0, e.acc1
		e.acc0, e.acc1 = 0, 0
		if count == 0 && cycles == 0 {
			continue
		}
		ns := int64(float64(cycles) / p.ghz)
		values[0] = count - e.prev0
		values[1] = ns - e.prev1
		e.prev0, e.prev1 = count, ns
		if values[0] < 0 || values[1] < 0 || values == [2]int64{} {
			continue
		}
		p.stats.Value += values[1]
		if len(e.stk) == MaxStackDepth {
			p.stats.TruncVal += values[1]
		}
		if !e.haveLocs {
			// Records are already inline-expanded return PCs.
			e.locs = append([]uint64(nil), p.sym.locsForStack(e.stk)...)
			e.haveLocs = true
		}
		p.w.sample(values[:], e.locs, 0)
		p.stats.Samples++
	}
	return p.w.finish()
}

var cps struct {
	once sync.Once
	v    int64
	err  error
}

// CyclesPerSecond returns the rate of the runtime's cycle counter, which
// block and mutex records count in. runtime/pprof gets it from the runtime
// directly; this reads it from the "cycles/second=" header that
// runtime/pprof prints in the text (debug=1) form of the mutex profile. The
// value is exact. It is read once per process.
func CyclesPerSecond() (int64, error) {
	cps.once.Do(func() {
		var h headerWriter
		_ = pprof.Lookup("mutex").WriteTo(&h, 1)
		cps.v, cps.err = parseCyclesPerSecond(string(h.b))
	})
	return cps.v, cps.err
}

func parseCyclesPerSecond(s string) (int64, error) {
	const key = "cycles/second="
	i := strings.Index(s, key)
	if i < 0 {
		return 0, errors.New("no cycles/second in the mutex profile header")
	}
	s = s[i+len(key):]
	if j := strings.IndexByte(s, '\n'); j >= 0 {
		s = s[:j]
	}
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("bad cycles/second %q in the mutex profile header", s)
	}
	return v, nil
}

// headerWriter keeps the first bytes written to it and then fails, so the
// rest of the text profile is not kept.
type headerWriter struct{ b []byte }

var errEnough = errors.New("enough")

func (h *headerWriter) Write(b []byte) (int, error) {
	if len(h.b) >= 512 {
		return 0, errEnough
	}
	h.b = append(h.b, b[:min(len(b), 512-len(h.b))]...)
	return len(b), nil
}

var _ io.Writer = (*headerWriter)(nil)
