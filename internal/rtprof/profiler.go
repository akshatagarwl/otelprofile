package rtprof

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"math"
	"runtime/pprof"

	"github.com/klauspost/compress/gzip"
	"google.golang.org/protobuf/encoding/protowire"
)

// pprof profile.proto field numbers used here.
const (
	fieldSample   = 2
	fieldLocation = 4
	fieldPeriod   = 12

	fieldSampleLocation = 1
	fieldSampleValue    = 2
	fieldSampleLabel    = 3

	fieldLabelNum = 3

	fieldLocationID      = 1
	fieldLocationAddress = 3
)

type kind uint8

const (
	kindHeap kind = iota
	kindCountCycle
)

// Profiler writes delta profiles of one runtime/pprof profile.
type Profiler struct {
	name string
	kind kind

	gz   bytes.Buffer // runtime/pprof's gzipped output
	src  bytes.Reader
	zr   *gzip.Reader
	raws [2]bytes.Buffer // the decompressed cumulative profile, this one and the last
	cur  int             // index of this one in raws
	seed maphash.Seed
	out  []byte
	msg  []byte
	key  []byte
	ids  []uint64
	vals []uint64

	locs    []locInfo // by location ID
	gen     uint32
	rate    int64
	entries map[string]*entry  // by stack
	samples map[uint64]sampled // the previous profile's samples, by a hash of their bytes
	touched []*entry
	stats   Stats
}

type locInfo struct {
	start, end int // field bytes (tag included) in raw
	addr       uint64
	gen        uint32 // set when the location was seen in this profile
	used       uint32 // set when an emitted sample uses it
}

// entry is the state of one stack (and object size, for the heap).
type entry struct {
	acc0, acc1   int64 // this profile: alloc/inuse objects, or count/delay
	prev0, prev1 int64 // last profile: alloc objects, or count/delay
	tag          int64 // object size for the heap
	gen          uint32
	sample       [2]int // first sample with this key in this profile (message bytes in raw)
}

// sampled is what a sample message added to its entry. A sample whose
// bytes are the same as in the previous profile, with the same location
// IDs meaning the same addresses, adds the same again, so it is not
// decoded a second time. In a steady state most samples are unchanged.
// The bytes are compared, not just hashed, against the previous profile,
// which is kept for that.
//
// runtime/pprof numbers locations in the order it meets them, and the
// runtime lists the newest records first, so a cycle that brings a new
// stack renumbers the locations and decodes every sample once; the cycles
// after it reuse them again.
type sampled struct {
	e      *entry
	v0, v1 int64
	off, n int // the sample message in the profile it was last seen in
	gen    uint32
}

// Stats describes the last profile written, for tests and benchmarks.
type Stats struct {
	Samples   int // samples in runtime/pprof's cumulative profile
	Decoded   int // of which were decoded (new or changed since the last profile)
	Written   int // delta samples written
	Locations int // locations written
}

// NewHeap returns a profiler for the heap profile. NewMutex and NewBlock
// return profilers for the mutex and block profiles. The first Profile call
// covers the time since the process started.
func NewHeap() *Profiler  { return newProfiler("heap", kindHeap) }
func NewMutex() *Profiler { return newProfiler("mutex", kindCountCycle) }
func NewBlock() *Profiler { return newProfiler("block", kindCountCycle) }

func newProfiler(name string, k kind) *Profiler {
	return &Profiler{name: name, kind: k, entries: map[string]*entry{}, samples: map[uint64]sampled{}, seed: maphash.MakeSeed()}
}

// Stats returns the statistics of the last Profile call.
func (p *Profiler) Stats() Stats { return p.stats }

var errMalformed = errors.New("malformed runtime/pprof profile")

// Profile returns the delta profile since the previous call, uncompressed.
// The bytes are valid until the next call.
func (p *Profiler) Profile() ([]byte, error) {
	prof := pprof.Lookup(p.name)
	if prof == nil {
		return nil, fmt.Errorf("no runtime profile %q", p.name)
	}
	p.gz.Reset()
	if err := prof.WriteTo(&p.gz, 0); err != nil {
		return nil, err
	}
	if err := p.gunzip(); err != nil {
		return nil, fmt.Errorf("%s profile: %w", p.name, err)
	}
	out, err := p.delta(p.raws[p.cur].Bytes(), p.raws[1-p.cur].Bytes())
	if err != nil {
		clear(p.samples)
		return nil, fmt.Errorf("%s profile: %w", p.name, err)
	}
	p.cur = 1 - p.cur
	if cap(p.raws[p.cur].Bytes()) > 64<<20 {
		p.raws[p.cur] = bytes.Buffer{} // do not keep a huge buffer after one huge profile
	}
	return out, nil
}

func (p *Profiler) gunzip() error {
	p.src.Reset(p.gz.Bytes())
	if p.zr == nil {
		zr, err := gzip.NewReader(&p.src)
		if err != nil {
			return err
		}
		p.zr = zr
	} else if err := p.zr.Reset(&p.src); err != nil {
		return err
	}
	raw := &p.raws[p.cur]
	raw.Reset()
	_, err := io.Copy(raw, p.zr)
	return err
}

// delta turns the cumulative profile raw into the delta profile. last is
// the previous call's raw.
func (p *Profiler) delta(raw, last []byte) ([]byte, error) {
	p.gen++
	p.stats = Stats{}
	p.touched = p.touched[:0]

	// Pass 1: index locations by ID (for their addresses) and the period.
	rate := int64(0)
	moved := false
	for i := 0; i < len(raw); {
		f, err := nextField(raw, i)
		if err != nil {
			return nil, err
		}
		i = f.end
		switch {
		case f.num == fieldPeriod && f.typ == protowire.VarintType:
			rate = int64(f.v)
		case f.num == fieldLocation && f.typ == protowire.BytesType:
			id, addr, err := locationIDAddr(f.b)
			if err != nil {
				return nil, err
			}
			if id == 0 || id > uint64(len(raw)) {
				return nil, errMalformed
			}
			for uint64(len(p.locs)) <= id {
				p.locs = append(p.locs, locInfo{})
			}
			l := &p.locs[id]
			if l.gen == p.gen {
				return nil, errMalformed // duplicate ID
			}
			if l.gen == p.gen-1 && l.addr != addr {
				moved = true
			}
			*l = locInfo{start: f.start, end: f.end, addr: addr, gen: p.gen}
		}
	}
	if moved || rate != p.rate {
		// Location IDs mean other addresses now (or the heap rate
		// changed): the previous profile's samples cannot be reused.
		clear(p.samples)
	}
	p.rate = rate

	// Pass 2: accumulate the samples' raw values per stack.
	for i := 0; i < len(raw); {
		f, err := nextField(raw, i)
		if err != nil {
			return nil, err
		}
		i = f.end
		if f.num != fieldSample || f.typ != protowire.BytesType {
			continue
		}
		p.stats.Samples++
		off, n := f.end-len(f.b), len(f.b)
		h := maphash.Bytes(p.seed, f.b)
		if s, ok := p.samples[h]; ok && s.gen >= p.gen-1 {
			var was []byte
			switch {
			case s.gen == p.gen && s.off+s.n <= len(raw):
				was = raw[s.off : s.off+s.n] // a repeat within this profile
			case s.gen == p.gen-1 && s.off+s.n <= len(last):
				was = last[s.off : s.off+s.n]
			}
			if bytes.Equal(was, f.b) {
				s.off, s.gen = off, p.gen
				p.samples[h] = s
				p.add(s.e, s.v0, s.v1, off, n)
				continue
			}
		}
		p.stats.Decoded++
		e, v0, v1, err := p.decodeSample(f.b, rate)
		if err != nil {
			return nil, err
		}
		p.samples[h] = sampled{e: e, v0: v0, v1: v1, off: off, n: n, gen: p.gen}
		p.add(e, v0, v1, off, n)
	}
	for k, s := range p.samples {
		if s.gen != p.gen {
			delete(p.samples, k)
		}
	}

	// Pass 3: write everything but the samples and locations as is, then
	// the delta samples and the locations they use.
	out := p.out[:0]
	for i := 0; i < len(raw); {
		f, err := nextField(raw, i)
		if err != nil {
			return nil, err
		}
		i = f.end
		if f.num != fieldSample && f.num != fieldLocation {
			out = append(out, raw[f.start:f.end]...)
		}
	}
	for _, e := range p.touched {
		var ok bool
		var err error
		if out, ok, err = p.appendDelta(out, raw, e, rate); err != nil {
			return nil, err
		} else if ok {
			p.stats.Written++
		}
	}
	for id := range p.locs {
		l := &p.locs[id]
		if l.gen == p.gen && l.used == p.gen {
			out = append(out, raw[l.start:l.end]...)
			p.stats.Locations++
		}
	}
	p.out = out
	return out, nil
}

// add adds a sample's raw values to its entry; off and n locate the sample
// message in the profile.
func (p *Profiler) add(e *entry, v0, v1 int64, off, n int) {
	if e.gen != p.gen {
		e.gen = p.gen
		e.acc0, e.acc1 = 0, 0
		e.sample = [2]int{off, off + n}
		p.touched = append(p.touched, e)
	}
	e.acc0 += v0
	e.acc1 += v1
}

// locationIDAddr returns a Location message's id and address.
func locationIDAddr(b []byte) (id, addr uint64, err error) {
	for i := 0; i < len(b); {
		f, err := nextField(b, i)
		if err != nil {
			return 0, 0, err
		}
		i = f.end
		if f.typ == protowire.VarintType {
			switch f.num {
			case fieldLocationID:
				id = f.v
			case fieldLocationAddress:
				addr = f.v
			}
		}
	}
	return id, addr, nil
}

// decodeSample finds a sample's entry and its raw values. The entry key is
// the sample's location addresses (stable for the life of the process,
// unlike location IDs) and, for the heap, the object size label.
func (p *Profiler) decodeSample(b []byte, rate int64) (*entry, int64, int64, error) {
	p.ids, p.vals = p.ids[:0], p.vals[:0]
	var tag int64
	var err error
	for i := 0; i < len(b); {
		f, ferr := nextField(b, i)
		if ferr != nil {
			return nil, 0, 0, ferr
		}
		i = f.end
		switch f.num {
		case fieldSampleLocation:
			if p.ids, err = appendVarints(p.ids, f); err != nil {
				return nil, 0, 0, err
			}
		case fieldSampleValue:
			if p.vals, err = appendVarints(p.vals, f); err != nil {
				return nil, 0, 0, err
			}
		case fieldSampleLabel:
			if f.typ != protowire.BytesType {
				return nil, 0, 0, errMalformed
			}
			for j := 0; j < len(f.b); {
				g, gerr := nextField(f.b, j)
				if gerr != nil {
					return nil, 0, 0, gerr
				}
				j = g.end
				if g.num == fieldLabelNum && g.typ == protowire.VarintType {
					tag = int64(g.v)
				}
			}
		}
	}
	key := p.key[:0]
	for _, id := range p.ids {
		if id >= uint64(len(p.locs)) || p.locs[id].gen != p.gen {
			return nil, 0, 0, errMalformed
		}
		key = binary.LittleEndian.AppendUint64(key, p.locs[id].addr)
	}
	key = append(key, 0xff) // the stack ends here; the size follows
	key = binary.LittleEndian.AppendUint64(key, uint64(tag))
	p.key = key

	var v0, v1 int64
	switch p.kind {
	case kindHeap:
		if len(p.vals) != 4 {
			return nil, 0, 0, errMalformed
		}
		// runtime/pprof scales each record; godeltaprof scaled each
		// window's raw delta. Recover the raw counts, which is exact.
		v0 = unscale(int64(p.vals[0]), tag, rate)
		v1 = unscale(int64(p.vals[2]), tag, rate)
	default:
		if len(p.vals) != 2 {
			return nil, 0, 0, errMalformed
		}
		v0, v1 = int64(p.vals[0]), int64(p.vals[1])
	}
	e := p.entries[string(key)]
	if e == nil {
		e = &entry{tag: tag}
		p.entries[string(key)] = e
	}
	return e, v0, v1, nil
}

// appendDelta writes the entry's delta sample, if it has one, reusing the
// location IDs and labels of its first sample in this profile.
func (p *Profiler) appendDelta(out, raw []byte, e *entry, rate int64) ([]byte, bool, error) {
	var vals [4]int64
	n := 2
	switch p.kind {
	case kindHeap:
		alloc := e.acc0 - e.prev0
		e.prev0 = e.acc0
		if alloc < 0 {
			return out, false, nil // cannot happen; treat as a counter reset
		}
		vals[0], vals[1] = scaleHeapSample(alloc, alloc*e.tag, rate)
		vals[2], vals[3] = scaleHeapSample(e.acc1, e.acc1*e.tag, rate)
		n = 4
	default:
		count, delay := e.acc0-e.prev0, e.acc1-e.prev1
		e.prev0, e.prev1 = e.acc0, e.acc1
		if count < 0 || delay < 0 {
			return out, false, nil
		}
		vals[0], vals[1] = count, delay
	}
	if vals == [4]int64{} {
		return out, false, nil
	}
	sample := raw[e.sample[0]:e.sample[1]]
	m := p.msg[:0]
	var err error
	for i := 0; i < len(sample); {
		f, ferr := nextField(sample, i)
		if ferr != nil {
			return out, false, ferr
		}
		i = f.end
		switch f.num {
		case fieldSampleLocation:
			m = append(m, sample[f.start:f.end]...)
			if p.ids, err = appendVarints(p.ids[:0], f); err != nil {
				return out, false, err
			}
			for _, id := range p.ids {
				if id >= uint64(len(p.locs)) || p.locs[id].gen != p.gen {
					return out, false, errMalformed
				}
				p.locs[id].used = p.gen
			}
		case fieldSampleLabel:
			m = append(m, sample[f.start:f.end]...)
		}
	}
	size := 0
	for _, v := range vals[:n] {
		size += protowire.SizeVarint(uint64(v))
	}
	m = protowire.AppendTag(m, fieldSampleValue, protowire.BytesType)
	m = protowire.AppendVarint(m, uint64(size))
	for _, v := range vals[:n] {
		m = protowire.AppendVarint(m, uint64(v))
	}
	p.msg = m
	out = protowire.AppendTag(out, fieldSample, protowire.BytesType)
	out = protowire.AppendBytes(out, m)
	return out, true, nil
}

// heapScale is the factor runtime/pprof's scaleHeapSample multiplies a heap
// record by: 1 / (1 - exp(-avgSize/rate)).
func heapScale(size, rate int64) float64 {
	return 1 / (1 - math.Exp(-float64(size)/float64(rate)))
}

// scaleHeapSample is runtime/pprof's (Copyright 2016 The Go Authors, under
// the BSD-style license in NOTICE): it adjusts a heap sample for its
// probability of being sampled.
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

// unscale returns the raw record count n for which runtime/pprof wrote the
// scaled count v: int64(float64(n) * scale) == v. Since scale > 1, at most
// one n fits.
func unscale(v, size, rate int64) int64 {
	if v <= 0 || size <= 0 || rate <= 1 {
		return v
	}
	s := heapScale(size, rate)
	n := int64(float64(v) / s)
	for c := max(n-1, 1); c <= n+2; c++ {
		if int64(float64(c)*s) == v {
			return c
		}
	}
	return int64(math.Round(float64(v) / s))
}

// field is one protobuf field: its number and type, its varint value v or
// bytes b, and its byte range [start, end) in the message.
type field struct {
	num        protowire.Number
	typ        protowire.Type
	v          uint64
	b          []byte
	start, end int
}

// nextField decodes the field that starts at msg[i].
func nextField(msg []byte, i int) (field, error) {
	f := field{start: i}
	num, typ, n := protowire.ConsumeTag(msg[i:])
	if n < 0 {
		return f, errMalformed
	}
	f.num, f.typ = num, typ
	i += n
	switch typ {
	case protowire.VarintType:
		f.v, n = protowire.ConsumeVarint(msg[i:])
	case protowire.BytesType:
		f.b, n = protowire.ConsumeBytes(msg[i:])
	default:
		n = protowire.ConsumeFieldValue(num, typ, msg[i:])
	}
	if n < 0 {
		return f, errMalformed
	}
	f.end = i + n
	return f, nil
}

// appendVarints appends the values of a repeated varint field, packed or
// not.
func appendVarints(dst []uint64, f field) ([]uint64, error) {
	switch f.typ {
	case protowire.VarintType:
		return append(dst, f.v), nil
	case protowire.BytesType:
		for b := f.b; len(b) > 0; {
			x, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return dst, errMalformed
			}
			dst = append(dst, x)
			b = b[n:]
		}
		return dst, nil
	}
	return dst, errMalformed
}
