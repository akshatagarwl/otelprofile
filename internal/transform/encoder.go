package transform

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Encoder converts pprof profiles straight into an encoded
// ExportProfilesServiceRequest. It decodes the pprof wire format itself and
// writes OTLP protobuf directly, reusing its tables and buffers between
// calls, so a steady-state collection cycle allocates almost nothing. Its
// output is equivalent to Builder's (tests check this) and passes profcheck.
//
// An Encoder is not safe for concurrent use.
type Encoder struct {
	opts Options

	intern map[string]string // strings and binary keys kept across requests

	// Per-request dictionary.
	strIdx   map[string]int32
	strs     []string
	funcIdx  map[functionKey]int32
	funcs    []functionKey
	mapIdx   map[mappingKey]int32
	maps     []encMapping
	locIdx   map[string]int32
	locs     []string // encoded Location message bodies
	stackIdx map[string]int32
	stacks   []string // encoded packed location indices
	attrIdx  map[attrKey]int32
	attrs    []encAttr
	linkIdx  map[[24]byte]int32
	links    [][24]byte
	setIdx   map[string]int32 // attribute index sets, for sample identity
	sets     []string         // packed attribute indices by set id

	// Per-pprof scratch.
	pStrs    [][2]int32 // string table offsets into raw
	pTypes   [][2]int64
	pMaps    []pMapping
	pMapByID map[uint64]int32
	pFuncs   []pFunction
	pFuncBy  map[uint64]int32
	pLocs    []pLocation
	pLocByID map[uint64]int32
	pLines   []pLine
	pSamples [][2]int32 // sample message offsets into raw
	pComment []int64
	locIDs   []uint64
	values   []int64
	labels   []pLabel
	dictLocs []int32
	attrSet  []int32
	samples  []encSample
	sampleBy map[sampleKey]int32
	key      []byte

	gz gunzipper

	// Delta state for cumulative inputs: per profile Name, the cumulative
	// values per sample type for each stack+labels key. pending holds what
	// the current Encode computed until CommitDeltas.
	deltaPrev    map[string]map[string][]int64
	deltaPending map[string]map[string][]int64
	cum          *cumulativeInput // set while encoding a cumulative input

	// Output buffers by nesting depth.
	bufs [6][]byte
	out  []byte
}

type encMapping struct {
	start, limit, offset uint64
	file                 int32
	attrs                [5]int32
	nattrs               int
}

type encAttr struct {
	key, unit int32
	kind      byte
	s         string
	i         int64
	b         bool
	ss        []string
	is        []int64
}

type pMapping struct {
	id, start, limit, offset uint64
	file, buildID            int64
	hasFunctions, hasFiles   bool
	hasLines, hasInline      bool
	dict                     int32 // -1 until used
}

type pFunction struct {
	id                 uint64
	name, system, file int64
	startLine          int64
	dict               int32
}

type pLocation struct {
	id, mappingID, address uint64
	lineStart, lineEnd     int32
	folded                 bool
	dict                   int32
}

type pLine struct {
	funcID       uint64
	line, column int64
}

type pLabel struct {
	key, str, num, numUnit int64
}

type encSample struct {
	stack, link, set int32
	value            int64
}

type sampleKey struct{ stack, link, set int32 }

// NewEncoder returns an Encoder.
func NewEncoder(opts Options) *Encoder {
	if opts.TraceIDLabel == "" {
		opts.TraceIDLabel = LabelTraceID
	}
	if opts.SpanIDLabel == "" {
		opts.SpanIDLabel = LabelSpanID
	}
	return &Encoder{
		opts:     opts,
		intern:   map[string]string{},
		strIdx:   map[string]int32{},
		funcIdx:  map[functionKey]int32{},
		mapIdx:   map[mappingKey]int32{},
		locIdx:   map[string]int32{},
		stackIdx: map[string]int32{},
		attrIdx:  map[attrKey]int32{},
		linkIdx:  map[[24]byte]int32{},
		setIdx:   map[string]int32{},
		pMapByID: map[uint64]int32{},
		pFuncBy:  map[uint64]int32{},
		pLocByID: map[uint64]int32{},
		sampleBy: map[sampleKey]int32{},

		deltaPrev:    map[string]map[string][]int64{},
		deltaPending: map[string]map[string][]int64{},
	}
}

// cumulativeInput is the per-input state for turning cumulative counts into
// deltas.
type cumulativeInput struct {
	delta []bool             // per sample type: subtract the previous value
	keys  []string           // per pprof sample: stack+labels key
	first map[string]int     // key → first sample index with that key
	cur   map[string][]int64 // key → summed cumulative values per sample type
	prev  map[string][]int64 // key → previous cumulative values (may be nil)
}

// cumulativeTypes are the pprof sample types runtime/pprof reports as totals
// since the process started.
var cumulativeTypes = map[string]bool{"alloc_objects": true, "alloc_space": true, "contentions": true, "delay": true}

// CommitDeltas makes the cumulative state computed by the last Encode the
// baseline for the next one. Call it once that output has been sent (or
// will never be sent again); an Encode whose output is discarded, for
// example to split it, must not be committed.
func (e *Encoder) CommitDeltas() {
	for name, st := range e.deltaPending {
		e.deltaPrev[name] = st
	}
	clear(e.deltaPending)
}

// prepareCumulative indexes a cumulative input: a stable key per sample
// (function names, lines and labels, which survive between runtime/pprof
// writes, unlike location IDs) and the summed values per key.
func (e *Encoder) prepareCumulative(raw []byte, in Input) error {
	c := &cumulativeInput{
		delta: make([]bool, len(e.pTypes)),
		keys:  make([]string, len(e.pSamples)),
		first: make(map[string]int, len(e.pSamples)),
		cur:   make(map[string][]int64, len(e.pSamples)),
	}
	for ti, st := range e.pTypes {
		c.delta[ti] = cumulativeTypes[e.rawString(raw, st[0])]
	}
	var key []byte
	for si, so := range e.pSamples {
		if err := e.decodeSample(raw[so[0]:so[1]]); err != nil {
			return err
		}
		key = key[:0]
		for _, id := range e.locIDs {
			li, ok := e.pLocByID[id]
			if !ok {
				return fmt.Errorf("sample references missing location id %d", id)
			}
			l := e.pLocs[li]
			if l.lineEnd == l.lineStart {
				key = binary.AppendUvarint(key, l.address)
			}
			for _, ln := range e.pLines[l.lineStart:l.lineEnd] {
				if fi, ok := e.pFuncBy[ln.funcID]; ok {
					key = append(key, e.rawBytes(raw, e.pFuncs[fi].name)...)
				}
				key = append(key, 0)
				key = binary.AppendVarint(key, ln.line)
			}
			key = append(key, 1)
		}
		key = append(key, 2)
		for i := 1; i < len(e.labels); i++ { // labels in key order
			for j := i; j > 0 && e.rawString(raw, e.labels[j].key) < e.rawString(raw, e.labels[j-1].key); j-- {
				e.labels[j], e.labels[j-1] = e.labels[j-1], e.labels[j]
			}
		}
		for _, l := range e.labels {
			key = append(key, e.rawBytes(raw, l.key)...)
			key = append(key, 0)
			key = append(key, e.rawBytes(raw, l.str)...)
			key = binary.AppendVarint(key, l.num)
			key = append(key, 0)
		}
		k := string(key)
		c.keys[si] = k
		vals := c.cur[k]
		if vals == nil {
			vals = make([]int64, len(e.pTypes))
			c.cur[k] = vals
			c.first[k] = si
		}
		for ti := range vals {
			if ti < len(e.values) {
				vals[ti] += e.values[ti]
			}
		}
	}
	// A cumulative profile seen earlier in this Encode is the baseline for a
	// later one with the same name.
	if p, ok := e.deltaPending[in.Name]; ok {
		c.prev = p
	} else {
		c.prev = e.deltaPrev[in.Name]
	}
	e.deltaPending[in.Name] = c.cur
	e.cum = c
	return nil
}

// maxInterned bounds the names (functions, files, label keys and values)
// kept across requests; they repeat every cycle. Binary keys for stacks and
// locations churn and are not interned. The table is cleared when it
// outgrows the bound.
const maxInterned = 2048

func (e *Encoder) internBytes(b []byte) string {
	if s, ok := e.intern[string(b)]; ok {
		return s
	}
	s := string(b)
	e.intern[s] = s
	return s
}

func (e *Encoder) resetDict() {
	if len(e.intern) > maxInterned {
		clear(e.intern)
	}
	clear(e.strIdx)
	clear(e.funcIdx)
	clear(e.mapIdx)
	clear(e.locIdx)
	clear(e.stackIdx)
	clear(e.attrIdx)
	clear(e.linkIdx)
	clear(e.setIdx)
	e.strs = append(e.strs[:0], "")
	e.strIdx[""] = 0
	e.funcs = append(e.funcs[:0], functionKey{})
	e.funcIdx[functionKey{}] = 0
	e.maps = append(e.maps[:0], encMapping{})
	e.mapIdx[mappingKey{}] = 0
	e.locs = append(e.locs[:0], "")
	e.locIdx[""] = 0
	e.stacks = append(e.stacks[:0], "")
	e.stackIdx[""] = 0
	e.attrs = append(e.attrs[:0], encAttr{})
	e.links = append(e.links[:0], [24]byte{})
	e.setIdx[""] = 0
	e.sets = append(e.sets[:0], "")
}

func (e *Encoder) str(s string) int32 {
	if i, ok := e.strIdx[s]; ok {
		return i
	}
	i := int32(len(e.strs))
	e.strs = append(e.strs, s)
	e.strIdx[s] = i
	return i
}

func (e *Encoder) strBytes(b []byte) int32 {
	if i, ok := e.strIdx[string(b)]; ok {
		return i
	}
	return e.str(e.internBytes(b))
}

func (e *Encoder) attr(k attrKey, a encAttr) int32 {
	if i, ok := e.attrIdx[k]; ok {
		return i
	}
	i := int32(len(e.attrs))
	a.key, a.unit = k.key, k.unit
	e.attrs = append(e.attrs, a)
	e.attrIdx[k] = i
	return i
}

func (e *Encoder) attrStr(key int32, value string) int32 {
	return e.attr(attrKey{key: key, kind: kindString, value: value}, encAttr{kind: kindString, s: value})
}

func (e *Encoder) attrBool(key int32) int32 {
	return e.attr(attrKey{key: key, kind: kindBool, value: "t"}, encAttr{kind: kindBool, b: true})
}

// EncScope describes the InstrumentationScope written for each pprof.
type EncScope struct {
	Name, Version string
	// Attributes is a pre-encoded sequence of KeyValue fields (field 3 of
	// InstrumentationScope), or nil.
	Attributes []byte
}

// Encode converts ins into one encoded ExportProfilesServiceRequest with a
// single ResourceProfiles. resource is the encoded Resource message. The
// returned slice aliases the Encoder's buffer and is valid until the next
// call. Inputs with no non-zero samples are skipped; samples is 0 when
// nothing was encoded.
func (e *Encoder) Encode(resource []byte, scope EncScope, ins []Input) (out []byte, samples int, err error) {
	e.resetDict()
	clear(e.deltaPending)
	var errs []error
	scopes := e.bufs[1][:0]
	for _, in := range ins {
		n, sp, perr := e.encodeOne(scope, in)
		if perr != nil {
			errs = append(errs, perr)
			continue
		}
		if n == 0 {
			continue
		}
		samples += n
		scopes = protowire.AppendTag(scopes, 2, protowire.BytesType)
		scopes = protowire.AppendBytes(scopes, sp)
	}
	e.bufs[1] = scopes
	if samples == 0 {
		return nil, 0, errors.Join(errs...)
	}
	// ResourceProfiles{resource=1, scope_profiles=2...}
	rp := e.bufs[0][:0]
	rp = protowire.AppendTag(rp, 1, protowire.BytesType)
	rp = protowire.AppendBytes(rp, resource)
	rp = append(rp, scopes...)
	e.bufs[0] = rp

	o := e.out[:0]
	o = protowire.AppendTag(o, 1, protowire.BytesType)
	o = protowire.AppendBytes(o, rp)
	o = protowire.AppendTag(o, 2, protowire.BytesType)
	o = e.appendDictionary(o)
	e.out = o
	return o, samples, errors.Join(errs...)
}

// encodeOne converts one pprof into an encoded ScopeProfiles message (in
// e.bufs[2]). It returns the number of samples.
func (e *Encoder) encodeOne(scope EncScope, in Input) (int, []byte, error) {
	raw, err := e.gz.gunzip(in.Pprof)
	if err != nil {
		return 0, nil, fmt.Errorf("decompress pprof %q: %w", in.Name, err)
	}
	hdr, err := e.parse(raw)
	if err != nil {
		return 0, nil, fmt.Errorf("parse pprof %q: %w", in.Name, err)
	}
	e.cum = nil
	if in.Cumulative {
		if err := e.prepareCumulative(raw, in); err != nil {
			return 0, nil, fmt.Errorf("pprof %q: %w", in.Name, err)
		}
		defer func() { e.cum = nil }()
		if in.Baseline {
			return 0, nil, nil
		}
	}
	start := in.Start
	if start.IsZero() && hdr.timeNanos != 0 {
		start = time.Unix(0, hdr.timeNanos)
	}
	dur := in.Duration
	if dur == 0 {
		dur = time.Duration(hdr.durationNanos)
	}

	// Profiles, in pprof sample-type order.
	profs := e.bufs[3][:0]
	total := 0
	var kept []int64
	for ti, st := range e.pTypes {
		n, err := e.collectSamples(raw, ti)
		if err != nil {
			return 0, nil, err
		}
		if n == 0 {
			continue
		}
		kept = append(kept, int64(ti))
		total += n
		profs = protowire.AppendTag(profs, 2, protowire.BytesType)
		profs = e.appendProfile(profs, raw, st, hdr, start, dur, in.Name)
	}
	e.bufs[3] = profs
	if total == 0 {
		return 0, nil, nil
	}

	sp := e.bufs[2][:0]
	// scope=1: InstrumentationScope{name=1, version=2, attributes=3}
	sc := e.bufs[4][:0]
	if scope.Name != "" {
		sc = protowire.AppendTag(sc, 1, protowire.BytesType)
		sc = protowire.AppendString(sc, scope.Name)
	}
	if scope.Version != "" {
		sc = protowire.AppendTag(sc, 2, protowire.BytesType)
		sc = protowire.AppendString(sc, scope.Version)
	}
	sc = append(sc, scope.Attributes...)
	if hdr.defaultSampleType > 0 {
		sc = appendKeyValueString(sc, 3, attrDefaultType, e.rawString(raw, hdr.defaultSampleType))
	}
	if len(kept) != len(e.pTypes) || len(e.pTypes) > 1 {
		sc = appendKeyValueInts(sc, 3, attrSampleTypeOrder, kept)
	}
	e.bufs[4] = sc
	sp = protowire.AppendTag(sp, 1, protowire.BytesType)
	sp = protowire.AppendBytes(sp, sc)
	sp = append(sp, profs...)
	e.bufs[2] = sp
	return total, sp, nil
}

type header struct {
	timeNanos, durationNanos, period int64
	periodType                       [2]int64
	hasPeriodType                    bool
	defaultSampleType, docURL        int64
	dropFrames, keepFrames           int64
}

func (e *Encoder) rawString(raw []byte, idx int64) string {
	if idx <= 0 || idx >= int64(len(e.pStrs)) {
		return ""
	}
	o := e.pStrs[idx]
	return e.internBytes(raw[o[0]:o[1]])
}

func (e *Encoder) rawStrIdx(raw []byte, idx int64) int32 {
	if idx <= 0 || idx >= int64(len(e.pStrs)) {
		return 0
	}
	o := e.pStrs[idx]
	return e.strBytes(raw[o[0]:o[1]])
}

var errMalformed = errors.New("malformed pprof")

// parse indexes a raw pprof: string offsets, mappings, functions, locations,
// and the offsets of each sample message.
func (e *Encoder) parse(raw []byte) (header, error) {
	var h header
	e.pStrs, e.pTypes, e.pMaps, e.pFuncs, e.pLocs, e.pLines, e.pSamples, e.pComment =
		e.pStrs[:0], e.pTypes[:0], e.pMaps[:0], e.pFuncs[:0], e.pLocs[:0], e.pLines[:0], e.pSamples[:0], e.pComment[:0]
	clear(e.pMapByID)
	clear(e.pFuncBy)
	clear(e.pLocByID)
	b := raw
	off := 0
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return h, errMalformed
		}
		b, off = b[n:], off+n
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return h, errMalformed
			}
			start := off + (m - len(v))
			switch num {
			case 1, 11: // sample_type, period_type
				vt, err := parseValueType(v)
				if err != nil {
					return h, err
				}
				if num == 1 {
					e.pTypes = append(e.pTypes, vt)
				} else {
					h.periodType, h.hasPeriodType = vt, true
				}
			case 2:
				e.pSamples = append(e.pSamples, [2]int32{int32(start), int32(start + len(v))})
			case 3:
				if err := e.parseMapping(v); err != nil {
					return h, err
				}
			case 4:
				if err := e.parseLocation(v); err != nil {
					return h, err
				}
			case 5:
				if err := e.parseFunction(v); err != nil {
					return h, err
				}
			case 6:
				e.pStrs = append(e.pStrs, [2]int32{int32(start), int32(start + len(v))})
			case 13: // packed comments
				if err := appendPackedInt64(&e.pComment, v); err != nil {
					return h, err
				}
			}
			b, off = b[m:], off+m
			continue
		}
		var val uint64
		switch typ {
		case protowire.VarintType:
			x, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return h, errMalformed
			}
			val, b, off = x, b[m:], off+m
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return h, errMalformed
			}
			b, off = b[m:], off+m
			continue
		}
		switch num {
		case 7:
			h.dropFrames = int64(val)
		case 8:
			h.keepFrames = int64(val)
		case 9:
			h.timeNanos = int64(val)
		case 10:
			h.durationNanos = int64(val)
		case 12:
			h.period = int64(val)
		case 13:
			e.pComment = append(e.pComment, int64(val))
		case 14:
			h.defaultSampleType = int64(val)
		case 15:
			h.docURL = int64(val)
		}
	}
	if len(e.pStrs) == 0 {
		return h, errors.New("pprof has an empty string table")
	}
	if o := e.pStrs[0]; o[1] != o[0] {
		return h, errors.New("pprof string table must start with \"\"")
	}
	for _, t := range e.pTypes {
		if t[0] < 0 || t[0] >= int64(len(e.pStrs)) || t[1] < 0 || t[1] >= int64(len(e.pStrs)) {
			return h, errMalformed
		}
	}
	return h, nil
}

func parseValueType(b []byte) ([2]int64, error) {
	var vt [2]int64
	err := eachVarint(b, func(num protowire.Number, v uint64) {
		if num == 1 || num == 2 {
			vt[num-1] = int64(v)
		}
	}, nil)
	return vt, err
}

// eachVarint walks a message calling fv for varint fields and fb for bytes
// fields; other wire types are skipped.
func eachVarint(b []byte, fv func(protowire.Number, uint64), fb func(protowire.Number, []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return errMalformed
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return errMalformed
			}
			if fv != nil {
				fv(num, v)
			}
			b = b[m:]
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return errMalformed
			}
			if fb != nil {
				if err := fb(num, v); err != nil {
					return err
				}
			}
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return errMalformed
			}
			b = b[m:]
		}
	}
	return nil
}

func appendPackedInt64(dst *[]int64, b []byte) error {
	for len(b) > 0 {
		v, m := protowire.ConsumeVarint(b)
		if m < 0 {
			return errMalformed
		}
		*dst = append(*dst, int64(v))
		b = b[m:]
	}
	return nil
}

func appendPackedUint64(dst *[]uint64, b []byte) error {
	for len(b) > 0 {
		v, m := protowire.ConsumeVarint(b)
		if m < 0 {
			return errMalformed
		}
		*dst = append(*dst, v)
		b = b[m:]
	}
	return nil
}

func (e *Encoder) parseMapping(b []byte) error {
	m := pMapping{dict: -1}
	err := eachVarint(b, func(num protowire.Number, v uint64) {
		switch num {
		case 1:
			m.id = v
		case 2:
			m.start = v
		case 3:
			m.limit = v
		case 4:
			m.offset = v
		case 5:
			m.file = int64(v)
		case 6:
			m.buildID = int64(v)
		case 7:
			m.hasFunctions = v != 0
		case 8:
			m.hasFiles = v != 0
		case 9:
			m.hasLines = v != 0
		case 10:
			m.hasInline = v != 0
		}
	}, nil)
	if err != nil {
		return err
	}
	e.pMapByID[m.id] = int32(len(e.pMaps))
	e.pMaps = append(e.pMaps, m)
	return nil
}

func (e *Encoder) parseFunction(b []byte) error {
	f := pFunction{dict: -1}
	err := eachVarint(b, func(num protowire.Number, v uint64) {
		switch num {
		case 1:
			f.id = v
		case 2:
			f.name = int64(v)
		case 3:
			f.system = int64(v)
		case 4:
			f.file = int64(v)
		case 5:
			f.startLine = int64(v)
		}
	}, nil)
	if err != nil {
		return err
	}
	e.pFuncBy[f.id] = int32(len(e.pFuncs))
	e.pFuncs = append(e.pFuncs, f)
	return nil
}

func (e *Encoder) parseLocation(b []byte) error {
	l := pLocation{dict: -1, lineStart: int32(len(e.pLines))}
	err := eachVarint(b, func(num protowire.Number, v uint64) {
		switch num {
		case 1:
			l.id = v
		case 2:
			l.mappingID = v
		case 3:
			l.address = v
		case 5:
			l.folded = v != 0
		}
	}, func(num protowire.Number, v []byte) error {
		if num != 4 {
			return nil
		}
		var ln pLine
		err := eachVarint(v, func(n protowire.Number, x uint64) {
			switch n {
			case 1:
				ln.funcID = x
			case 2:
				ln.line = int64(x)
			case 3:
				ln.column = int64(x)
			}
		}, nil)
		e.pLines = append(e.pLines, ln)
		return err
	})
	if err != nil {
		return err
	}
	l.lineEnd = int32(len(e.pLines))
	e.pLocByID[l.id] = int32(len(e.pLocs))
	e.pLocs = append(e.pLocs, l)
	return nil
}

// decodeSample fills e.locIDs, e.values and e.labels from one Sample message.
func (e *Encoder) decodeSample(b []byte) error {
	e.locIDs, e.values, e.labels = e.locIDs[:0], e.values[:0], e.labels[:0]
	return eachVarint(b, func(num protowire.Number, v uint64) {
		switch num {
		case 1:
			e.locIDs = append(e.locIDs, v)
		case 2:
			e.values = append(e.values, int64(v))
		}
	}, func(num protowire.Number, v []byte) error {
		switch num {
		case 1:
			return appendPackedUint64(&e.locIDs, v)
		case 2:
			return appendPackedInt64(&e.values, v)
		case 3:
			var l pLabel
			err := eachVarint(v, func(n protowire.Number, x uint64) {
				switch n {
				case 1:
					l.key = int64(x)
				case 2:
					l.str = int64(x)
				case 3:
					l.num = int64(x)
				case 4:
					l.numUnit = int64(x)
				}
			}, nil)
			e.labels = append(e.labels, l)
			return err
		}
		return nil
	})
}

// collectSamples fills e.samples with the merged non-zero samples of sample
// type ti.
func (e *Encoder) collectSamples(raw []byte, ti int) (int, error) {
	e.samples = e.samples[:0]
	clear(e.sampleBy)
	for si, so := range e.pSamples {
		if err := e.decodeSample(raw[so[0]:so[1]]); err != nil {
			return 0, err
		}
		if ti >= len(e.values) {
			continue
		}
		v := e.values[ti]
		if c := e.cum; c != nil && c.delta[ti] {
			// The key's summed delta is emitted once, on its first sample.
			k := c.keys[si]
			if c.first[k] != si {
				continue
			}
			v = c.cur[k][ti]
			if prev, ok := c.prev[k]; ok && ti < len(prev) && prev[ti] <= v {
				v -= prev[ti]
			}
		}
		if v == 0 && !e.opts.KeepZeroSamples {
			continue
		}
		e.dictLocs = e.dictLocs[:0]
		for _, id := range e.locIDs {
			li, ok := e.pLocByID[id]
			if !ok {
				return 0, fmt.Errorf("sample references missing location id %d", id)
			}
			d, err := e.location(raw, li)
			if err != nil {
				return 0, err
			}
			e.dictLocs = append(e.dictLocs, d)
		}
		stack := e.stack(e.dictLocs)
		set, link := e.sampleLabels(raw)
		k := sampleKey{stack, link, set}
		if i, ok := e.sampleBy[k]; ok {
			e.samples[i].value += v
			continue
		}
		e.sampleBy[k] = int32(len(e.samples))
		e.samples = append(e.samples, encSample{stack: stack, link: link, set: set, value: v})
	}
	return len(e.samples), nil
}

func (e *Encoder) stack(locs []int32) int32 {
	e.key = e.key[:0]
	for _, l := range locs {
		e.key = protowire.AppendVarint(e.key, uint64(l))
	}
	if i, ok := e.stackIdx[string(e.key)]; ok {
		return i
	}
	k := string(e.key) // per-request key; only names are interned
	i := int32(len(e.stacks))
	e.stacks = append(e.stacks, k)
	e.stackIdx[k] = i
	return i
}

func (e *Encoder) location(raw []byte, li int32) (int32, error) {
	l := &e.pLocs[li]
	if l.dict >= 0 {
		return l.dict, nil
	}
	// Location{mapping_index=1, address=2, lines=3, attribute_indices=4}
	var mi int32
	if l.mappingID != 0 {
		pm, ok := e.pMapByID[l.mappingID]
		if !ok {
			return 0, fmt.Errorf("location %d references missing mapping %d", l.id, l.mappingID)
		}
		mi = e.mapping(raw, pm)
	}
	k := e.bufs[5][:0]
	if mi != 0 {
		k = protowire.AppendTag(k, 1, protowire.VarintType)
		k = protowire.AppendVarint(k, uint64(mi))
	}
	if l.address != 0 {
		k = protowire.AppendTag(k, 2, protowire.VarintType)
		k = protowire.AppendVarint(k, l.address)
	}
	for _, ln := range e.pLines[l.lineStart:l.lineEnd] {
		var fi int32
		if ln.funcID != 0 {
			pf, ok := e.pFuncBy[ln.funcID]
			if !ok {
				return 0, fmt.Errorf("location %d references missing function %d", l.id, ln.funcID)
			}
			fi = e.function(raw, pf)
		}
		var line [32]byte
		lb := line[:0]
		if fi != 0 {
			lb = protowire.AppendTag(lb, 1, protowire.VarintType)
			lb = protowire.AppendVarint(lb, uint64(fi))
		}
		if ln.line != 0 {
			lb = protowire.AppendTag(lb, 2, protowire.VarintType)
			lb = protowire.AppendVarint(lb, uint64(ln.line))
		}
		if ln.column != 0 {
			lb = protowire.AppendTag(lb, 3, protowire.VarintType)
			lb = protowire.AppendVarint(lb, uint64(ln.column))
		}
		k = protowire.AppendTag(k, 3, protowire.BytesType)
		k = protowire.AppendBytes(k, lb)
	}
	if l.folded {
		ai := e.attrBool(e.str(attrIsFolded))
		k = protowire.AppendTag(k, 4, protowire.BytesType)
		k = protowire.AppendVarint(k, uint64(protowire.SizeVarint(uint64(ai))))
		k = protowire.AppendVarint(k, uint64(ai))
	}
	e.bufs[5] = k
	if i, ok := e.locIdx[string(k)]; ok {
		l.dict = i
		return i, nil
	}
	key := string(k)
	i := int32(len(e.locs))
	e.locs = append(e.locs, key)
	e.locIdx[key] = i
	l.dict = i
	return i, nil
}

func (e *Encoder) function(raw []byte, pf int32) int32 {
	f := &e.pFuncs[pf]
	if f.dict >= 0 {
		return f.dict
	}
	k := functionKey{e.rawStrIdx(raw, f.name), e.rawStrIdx(raw, f.system), e.rawStrIdx(raw, f.file), f.startLine}
	if k.name == 0 && k.systemName == 0 && k.filename == 0 {
		f.dict = 0
		return 0
	}
	i, ok := e.funcIdx[k]
	if !ok {
		i = int32(len(e.funcs))
		e.funcs = append(e.funcs, k)
		e.funcIdx[k] = i
	}
	f.dict = i
	return i
}

func (e *Encoder) mapping(raw []byte, pm int32) int32 {
	m := &e.pMaps[pm]
	if m.dict >= 0 {
		return m.dict
	}
	em := encMapping{start: m.start, limit: m.limit, offset: m.offset, file: e.rawStrIdx(raw, m.file)}
	add := func(a int32) { em.attrs[em.nattrs] = a; em.nattrs++ }
	if m.hasFunctions {
		add(e.attrBool(e.str(attrHasFunctions)))
	}
	if m.hasFiles {
		add(e.attrBool(e.str(attrHasFilenames)))
	}
	if m.hasLines {
		add(e.attrBool(e.str(attrHasLineNumbers)))
	}
	if m.hasInline {
		add(e.attrBool(e.str(attrHasInline)))
	}
	if s := e.rawString(raw, m.buildID); s != "" {
		add(e.attrStr(e.str(attrBuildID), s))
	}
	if em.start != 0 && em.limit <= em.start {
		em.start, em.limit = 0, 0
	}
	e.key = e.key[:0]
	for _, a := range em.attrs[:em.nattrs] {
		e.key = protowire.AppendVarint(e.key, uint64(a))
	}
	k := mappingKey{em.start, em.limit, em.offset, em.file, string(e.key)}
	i, ok := e.mapIdx[k]
	if !ok {
		i = int32(len(e.maps))
		e.maps = append(e.maps, em)
		e.mapIdx[k] = i
	}
	m.dict = i
	return i
}

// sampleLabels turns the decoded labels into an attribute-set index and a
// link index.
func (e *Encoder) sampleLabels(raw []byte) (set int32, link int32) {
	if len(e.labels) == 0 {
		return 0, 0
	}
	traceKey, spanKey := e.opts.TraceIDLabel, e.opts.SpanIDLabel
	var tid [16]byte
	var sid [8]byte
	var haveT, haveS bool
	for _, l := range e.labels {
		if l.str == 0 {
			continue
		}
		k := e.rawString(raw, l.key)
		if k == traceKey && !haveT {
			haveT = decodeHexBytes(tid[:], e.rawBytes(raw, l.str))
		} else if k == spanKey && !haveS {
			haveS = decodeHexBytes(sid[:], e.rawBytes(raw, l.str))
		}
	}
	linked := haveT && haveS && tid != [16]byte{} && sid != [8]byte{}
	if linked {
		var lk [24]byte
		copy(lk[:16], tid[:])
		copy(lk[16:], sid[:])
		if i, ok := e.linkIdx[lk]; ok {
			link = i
		} else {
			link = int32(len(e.links))
			e.links = append(e.links, lk)
			e.linkIdx[lk] = link
		}
	}
	// Labels by key, sorted; string labels win over numeric ones with the
	// same key; repeated keys become arrays. This matches Builder.
	e.attrSet = e.attrSet[:0]
	// google/pprof drops labels with neither a string nor a number.
	kept := e.labels[:0]
	for _, l := range e.labels {
		if l.str != 0 || l.num != 0 || l.numUnit != 0 {
			kept = append(kept, l)
		}
	}
	e.labels = kept
	// Stable insertion sort by key; label lists are short.
	for i := 1; i < len(e.labels); i++ {
		for j := i; j > 0 && e.rawString(raw, e.labels[j].key) < e.rawString(raw, e.labels[j-1].key); j-- {
			e.labels[j], e.labels[j-1] = e.labels[j-1], e.labels[j]
		}
	}
	for i := 0; i < len(e.labels); {
		k := e.rawString(raw, e.labels[i].key)
		j := i
		var strs, nums int
		for j < len(e.labels) && e.rawString(raw, e.labels[j].key) == k {
			if e.labels[j].str != 0 {
				strs++
			} else {
				nums++
			}
			j++
		}
		group := e.labels[i:j]
		i = j
		if linked && (k == traceKey || k == spanKey) && strs > 0 {
			continue
		}
		ki := e.str(k)
		switch {
		case strs == 1:
			for _, l := range group {
				if l.str != 0 {
					e.attrSet = append(e.attrSet, e.attrStr(ki, e.rawString(raw, l.str)))
				}
			}
		case strs > 1:
			var ss []string
			for _, l := range group {
				if l.str != 0 {
					ss = append(ss, e.rawString(raw, l.str))
				}
			}
			e.attrSet = append(e.attrSet, e.attr(attrKey{key: ki, kind: kindStrings, value: joinNUL(ss)}, encAttr{kind: kindStrings, ss: ss}))
		case nums == 1:
			l := group[0]
			unit := e.rawString(raw, l.numUnit)
			var ui int32
			if unit != "" {
				ui = e.str(unit)
			}
			e.attrSet = append(e.attrSet, e.attr(attrKey{key: ki, unit: ui, kind: kindInt, value: varintString(l.num)}, encAttr{kind: kindInt, i: l.num}))
		case nums > 1:
			var is []int64
			var enc []byte
			for _, l := range group {
				is = append(is, l.num)
				enc = binary.AppendVarint(enc, l.num)
			}
			unit := e.rawString(raw, group[0].numUnit)
			var ui int32
			if unit != "" {
				ui = e.str(unit)
			}
			e.attrSet = append(e.attrSet, e.attr(attrKey{key: ki, unit: ui, kind: kindInts, value: string(enc)}, encAttr{kind: kindInts, is: is}))
		}
	}
	if len(e.attrSet) == 0 {
		return 0, link
	}
	e.key = e.key[:0]
	for _, a := range e.attrSet {
		e.key = protowire.AppendVarint(e.key, uint64(a))
	}
	if i, ok := e.setIdx[string(e.key)]; ok {
		return i, link
	}
	k := string(e.key)
	i := int32(len(e.sets))
	e.sets = append(e.sets, k)
	e.setIdx[k] = i
	return i, link
}

func (e *Encoder) rawBytes(raw []byte, idx int64) []byte {
	if idx <= 0 || idx >= int64(len(e.pStrs)) {
		return nil
	}
	o := e.pStrs[idx]
	return raw[o[0]:o[1]]
}

func decodeHexBytes(dst []byte, s []byte) bool {
	if len(s) != 2*len(dst) {
		return false
	}
	_, err := hex.Decode(dst, s)
	return err == nil
}

func joinNUL(ss []string) string {
	n := 0
	for _, s := range ss {
		n += len(s) + 1
	}
	b := make([]byte, 0, n)
	for i, s := range ss {
		if i > 0 {
			b = append(b, 0)
		}
		b = append(b, s...)
	}
	return string(b)
}

func varintString(v int64) string {
	return string(binary.AppendVarint(nil, v))
}

// appendProfile appends one length-prefixed Profile message for the samples
// in e.samples.
func (e *Encoder) appendProfile(dst []byte, raw []byte, st [2]int64, h header, start time.Time, dur time.Duration, name string) []byte {
	p := e.bufs[4][:0]
	// sample_type=1 ValueType{type=1, unit=2}
	p = appendValueType(p, 1, e.rawStrIdx(raw, st[0]), e.rawStrIdx(raw, st[1]))
	// samples=2
	for _, s := range e.samples {
		var sb [64]byte
		b := sb[:0]
		if s.stack != 0 {
			b = protowire.AppendTag(b, 1, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(s.stack))
		}
		p = protowire.AppendTag(p, 2, protowire.BytesType)
		var attrs string
		if s.set != 0 {
			attrs = e.sets[s.set]
		}
		size := len(b)
		if len(attrs) > 0 {
			size += 1 + protowire.SizeBytes(len(attrs))
		}
		if s.link != 0 {
			size += 1 + protowire.SizeVarint(uint64(s.link))
		}
		valSize := protowire.SizeVarint(uint64(s.value))
		size += 1 + protowire.SizeBytes(valSize)
		p = protowire.AppendVarint(p, uint64(size))
		p = append(p, b...)
		if len(attrs) > 0 {
			p = protowire.AppendTag(p, 2, protowire.BytesType)
			p = protowire.AppendString(p, attrs)
		}
		if s.link != 0 {
			p = protowire.AppendTag(p, 3, protowire.VarintType)
			p = protowire.AppendVarint(p, uint64(s.link))
		}
		p = protowire.AppendTag(p, 4, protowire.BytesType)
		p = protowire.AppendVarint(p, uint64(valSize))
		p = protowire.AppendVarint(p, uint64(s.value))
	}
	// time_unix_nano=3 fixed64, duration_nano=4
	if ns := start.UnixNano(); ns > 0 {
		p = protowire.AppendTag(p, 3, protowire.Fixed64Type)
		p = protowire.AppendFixed64(p, uint64(ns))
	}
	if dur > 0 {
		p = protowire.AppendTag(p, 4, protowire.VarintType)
		p = protowire.AppendVarint(p, uint64(dur))
	}
	if h.hasPeriodType {
		p = appendValueType(p, 5, e.rawStrIdx(raw, h.periodType[0]), e.rawStrIdx(raw, h.periodType[1]))
	}
	if h.period != 0 {
		p = protowire.AppendTag(p, 6, protowire.VarintType)
		p = protowire.AppendVarint(p, uint64(h.period))
	}
	// profile_id=7
	var id [16]byte
	if e.opts.ProfileID != nil {
		copy(id[:], e.opts.ProfileID())
	} else {
		_, _ = rand.Read(id[:])
	}
	p = protowire.AppendTag(p, 7, protowire.BytesType)
	p = protowire.AppendBytes(p, id[:])
	// attribute_indices=11
	var ai [8]int32
	attrs := ai[:0]
	if name != "" {
		attrs = append(attrs, e.attrStr(e.str(AttrProfileName), name))
	}
	if len(e.pComment) > 0 {
		var ss []string
		for _, c := range e.pComment {
			ss = append(ss, e.rawString(raw, c))
		}
		attrs = append(attrs, e.attr(attrKey{key: e.str(attrComment), kind: kindStrings, value: joinNUL(ss)}, encAttr{kind: kindStrings, ss: ss}))
	}
	if s := e.rawString(raw, h.docURL); s != "" {
		attrs = append(attrs, e.attrStr(e.str(attrDocURL), s))
	}
	if s := e.rawString(raw, h.dropFrames); s != "" {
		attrs = append(attrs, e.attrStr(e.str(attrDropFrames), s))
	}
	if s := e.rawString(raw, h.keepFrames); s != "" {
		attrs = append(attrs, e.attrStr(e.str(attrKeepFrames), s))
	}
	p = appendPackedInt32(p, 11, attrs)
	e.bufs[4] = p
	return protowire.AppendBytes(dst, p)
}

func appendValueType(dst []byte, field protowire.Number, typ, unit int32) []byte {
	var b [16]byte
	v := b[:0]
	if typ != 0 {
		v = protowire.AppendTag(v, 1, protowire.VarintType)
		v = protowire.AppendVarint(v, uint64(typ))
	}
	if unit != 0 {
		v = protowire.AppendTag(v, 2, protowire.VarintType)
		v = protowire.AppendVarint(v, uint64(unit))
	}
	dst = protowire.AppendTag(dst, field, protowire.BytesType)
	return protowire.AppendBytes(dst, v)
}

func appendPackedInt32(dst []byte, field protowire.Number, vs []int32) []byte {
	if len(vs) == 0 {
		return dst
	}
	n := 0
	for _, v := range vs {
		n += protowire.SizeVarint(uint64(v))
	}
	dst = protowire.AppendTag(dst, field, protowire.BytesType)
	dst = protowire.AppendVarint(dst, uint64(n))
	for _, v := range vs {
		dst = protowire.AppendVarint(dst, uint64(v))
	}
	return dst
}

// appendKeyValueString appends a KeyValue{key, AnyValue{string_value}} field.
func appendKeyValueString(dst []byte, field protowire.Number, key, value string) []byte {
	av := protowire.AppendTag(nil, 1, protowire.BytesType)
	av = protowire.AppendString(av, value)
	kv := protowire.AppendTag(nil, 1, protowire.BytesType)
	kv = protowire.AppendString(kv, key)
	kv = protowire.AppendTag(kv, 2, protowire.BytesType)
	kv = protowire.AppendBytes(kv, av)
	dst = protowire.AppendTag(dst, field, protowire.BytesType)
	return protowire.AppendBytes(dst, kv)
}

// appendKeyValueInts appends a KeyValue{key, AnyValue{array_value of ints}}.
func appendKeyValueInts(dst []byte, field protowire.Number, key string, vs []int64) []byte {
	var arr []byte
	for _, v := range vs {
		var x [12]byte
		iv := protowire.AppendTag(x[:0], 3, protowire.VarintType)
		iv = protowire.AppendVarint(iv, uint64(v))
		arr = protowire.AppendTag(arr, 1, protowire.BytesType)
		arr = protowire.AppendBytes(arr, iv)
	}
	av := protowire.AppendTag(nil, 5, protowire.BytesType)
	av = protowire.AppendBytes(av, arr)
	kv := protowire.AppendTag(nil, 1, protowire.BytesType)
	kv = protowire.AppendString(kv, key)
	kv = protowire.AppendTag(kv, 2, protowire.BytesType)
	kv = protowire.AppendBytes(kv, av)
	dst = protowire.AppendTag(dst, field, protowire.BytesType)
	return protowire.AppendBytes(dst, kv)
}

func appendAnyValue(dst []byte, a encAttr) []byte {
	switch a.kind {
	case kindString:
		dst = protowire.AppendTag(dst, 1, protowire.BytesType)
		return protowire.AppendString(dst, a.s)
	case kindBool:
		dst = protowire.AppendTag(dst, 2, protowire.VarintType)
		return protowire.AppendVarint(dst, protowire.EncodeBool(a.b))
	case kindInt:
		dst = protowire.AppendTag(dst, 3, protowire.VarintType)
		return protowire.AppendVarint(dst, uint64(a.i))
	case kindStrings, kindInts:
		var arr []byte
		if a.kind == kindStrings {
			for _, s := range a.ss {
				iv := protowire.AppendTag(nil, 1, protowire.BytesType)
				iv = protowire.AppendString(iv, s)
				arr = protowire.AppendTag(arr, 1, protowire.BytesType)
				arr = protowire.AppendBytes(arr, iv)
			}
		} else {
			for _, i := range a.is {
				iv := protowire.AppendTag(nil, 3, protowire.VarintType)
				iv = protowire.AppendVarint(iv, uint64(i))
				arr = protowire.AppendTag(arr, 1, protowire.BytesType)
				arr = protowire.AppendBytes(arr, iv)
			}
		}
		dst = protowire.AppendTag(dst, 5, protowire.BytesType)
		return protowire.AppendBytes(dst, arr)
	}
	return dst
}

// appendDictionary appends the length-prefixed ProfilesDictionary.
func (e *Encoder) appendDictionary(dst []byte) []byte {
	d := e.bufs[2][:0]
	var tmp [96]byte
	for _, m := range e.maps {
		b := tmp[:0]
		if m.start != 0 {
			b = protowire.AppendTag(b, 1, protowire.VarintType)
			b = protowire.AppendVarint(b, m.start)
		}
		if m.limit != 0 {
			b = protowire.AppendTag(b, 2, protowire.VarintType)
			b = protowire.AppendVarint(b, m.limit)
		}
		if m.offset != 0 {
			b = protowire.AppendTag(b, 3, protowire.VarintType)
			b = protowire.AppendVarint(b, m.offset)
		}
		if m.file != 0 {
			b = protowire.AppendTag(b, 4, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(m.file))
		}
		b = appendPackedInt32(b, 5, m.attrs[:m.nattrs])
		d = protowire.AppendTag(d, 1, protowire.BytesType)
		d = protowire.AppendBytes(d, b)
	}
	for _, l := range e.locs {
		d = protowire.AppendTag(d, 2, protowire.BytesType)
		d = protowire.AppendString(d, l)
	}
	for _, f := range e.funcs {
		b := tmp[:0]
		if f.name != 0 {
			b = protowire.AppendTag(b, 1, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(f.name))
		}
		if f.systemName != 0 {
			b = protowire.AppendTag(b, 2, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(f.systemName))
		}
		if f.filename != 0 {
			b = protowire.AppendTag(b, 3, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(f.filename))
		}
		if f.startLine != 0 {
			b = protowire.AppendTag(b, 4, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(f.startLine))
		}
		d = protowire.AppendTag(d, 3, protowire.BytesType)
		d = protowire.AppendBytes(d, b)
	}
	for i, l := range e.links {
		b := tmp[:0]
		if i > 0 {
			b = protowire.AppendTag(b, 1, protowire.BytesType)
			b = protowire.AppendBytes(b, l[:16])
			b = protowire.AppendTag(b, 2, protowire.BytesType)
			b = protowire.AppendBytes(b, l[16:])
		}
		d = protowire.AppendTag(d, 4, protowire.BytesType)
		d = protowire.AppendBytes(d, b)
	}
	for _, s := range e.strs {
		d = protowire.AppendTag(d, 5, protowire.BytesType)
		d = protowire.AppendString(d, s)
	}
	for i, a := range e.attrs {
		b := e.bufs[5][:0]
		if i > 0 {
			b = protowire.AppendTag(b, 1, protowire.VarintType)
			b = protowire.AppendVarint(b, uint64(a.key))
			b = protowire.AppendTag(b, 2, protowire.BytesType)
			b = protowire.AppendBytes(b, appendAnyValue(tmp[:0], a))
			if a.unit != 0 {
				b = protowire.AppendTag(b, 3, protowire.VarintType)
				b = protowire.AppendVarint(b, uint64(a.unit))
			}
		}
		e.bufs[5] = b
		d = protowire.AppendTag(d, 6, protowire.BytesType)
		d = protowire.AppendBytes(d, b)
	}
	for _, s := range e.stacks {
		d = protowire.AppendTag(d, 7, protowire.BytesType)
		if s == "" {
			d = protowire.AppendVarint(d, 0)
			continue
		}
		d = protowire.AppendVarint(d, uint64(1+protowire.SizeBytes(len(s))))
		d = protowire.AppendTag(d, 1, protowire.BytesType)
		d = protowire.AppendString(d, s)
	}
	e.bufs[2] = d
	return protowire.AppendBytes(dst, d)
}
