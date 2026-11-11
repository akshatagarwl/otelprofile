package rtprof

import (
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// pprof profile.proto field numbers.
const (
	tagProfileSampleType  = 1
	tagProfileSample      = 2
	tagProfileMapping     = 3
	tagProfileLocation    = 4
	tagProfileFunction    = 5
	tagProfileStringTable = 6
	tagProfileTimeNanos   = 9
	tagProfilePeriodType  = 11
	tagProfilePeriod      = 12

	tagValueTypeType = 1
	tagValueTypeUnit = 2

	tagSampleLocation = 1
	tagSampleValue    = 2
	tagSampleLabel    = 3

	tagLabelKey = 1
	tagLabelNum = 3

	tagMappingID           = 1
	tagMappingStart        = 2
	tagMappingLimit        = 3
	tagMappingOffset       = 4
	tagMappingFilename     = 5
	tagMappingBuildID      = 6
	tagMappingHasFunctions = 7

	tagLocationID        = 1
	tagLocationMappingID = 2
	tagLocationAddress   = 3
	tagLocationLine      = 4

	tagLineFunctionID = 1
	tagLineLine       = 2

	tagFunctionID         = 1
	tagFunctionName       = 2
	tagFunctionSystemName = 3
	tagFunctionFilename   = 4
)

type valueType struct{ typ, unit string }

// writer encodes one uncompressed pprof profile at a time into a buffer it
// keeps. Locations and functions keep their IDs across profiles; each
// profile carries only the ones its samples use.
type writer struct {
	sym     *symbols
	buf     []byte
	msg     []byte // nested message scratch
	sub     []byte // doubly nested message scratch
	strs    []string
	strIdx  map[string]int64
	used    []*location
	usedFns []int32
}

func newWriter(sym *symbols) *writer {
	return &writer{sym: sym, strIdx: map[string]int64{}}
}

func (w *writer) str(s string) int64 {
	if i, ok := w.strIdx[s]; ok {
		return i
	}
	i := int64(len(w.strs))
	w.strs = append(w.strs, s)
	w.strIdx[s] = i
	return i
}

func appendVarintField(b []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendBytesField(b []byte, num protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func (w *writer) valueType(num protowire.Number, vt valueType) {
	w.msg = appendVarintField(w.msg[:0], tagValueTypeType, uint64(w.str(vt.typ)))
	w.msg = appendVarintField(w.msg, tagValueTypeUnit, uint64(w.str(vt.unit)))
	w.buf = appendBytesField(w.buf, num, w.msg)
}

// start begins a profile.
func (w *writer) start(period valueType, periodValue int64, types []valueType) {
	w.sym.gen++
	w.buf = w.buf[:0]
	w.strs = w.strs[:0]
	clear(w.strIdx)
	w.used = w.used[:0]
	w.usedFns = w.usedFns[:0]
	w.str("")
	w.valueType(tagProfilePeriodType, period)
	w.buf = appendVarintField(w.buf, tagProfilePeriod, uint64(periodValue))
	for _, t := range types {
		w.valueType(tagProfileSampleType, t)
	}
}

// sample adds a sample; blockSize, if not zero, becomes the numeric "bytes"
// label as in runtime/pprof's heap profile.
func (w *writer) sample(values []int64, locs []uint64, blockSize int64) {
	gen := w.sym.gen
	for _, id := range locs {
		l := w.sym.byID(id)
		if l.gen != gen {
			l.gen = gen
			w.used = append(w.used, l)
		}
	}
	m := w.msg[:0]
	m = protowire.AppendTag(m, tagSampleLocation, protowire.BytesType)
	n := 0
	for _, id := range locs {
		n += protowire.SizeVarint(id)
	}
	m = protowire.AppendVarint(m, uint64(n))
	for _, id := range locs {
		m = protowire.AppendVarint(m, id)
	}
	m = protowire.AppendTag(m, tagSampleValue, protowire.BytesType)
	n = 0
	for _, v := range values {
		n += protowire.SizeVarint(uint64(v))
	}
	m = protowire.AppendVarint(m, uint64(n))
	for _, v := range values {
		m = protowire.AppendVarint(m, uint64(v))
	}
	if blockSize != 0 {
		w.sub = appendVarintField(w.sub[:0], tagLabelKey, uint64(w.str("bytes")))
		w.sub = appendVarintField(w.sub, tagLabelNum, uint64(blockSize))
		m = appendBytesField(m, tagSampleLabel, w.sub)
	}
	w.msg = m
	w.buf = appendBytesField(w.buf, tagProfileSample, m)
}

// finish writes the used locations, their functions, the mappings and the
// string table, and returns the profile. It is valid until the next start.
func (w *writer) finish() []byte {
	gen := w.sym.gen
	for _, l := range w.used {
		m := appendVarintField(w.msg[:0], tagLocationID, l.id)
		if l.mapping >= 0 {
			m = appendVarintField(m, tagLocationMappingID, uint64(l.mapping+1))
		}
		m = appendVarintField(m, tagLocationAddress, l.addr)
		for _, ln := range l.lines {
			f := &w.sym.funcs[ln.fn]
			if f.gen != gen {
				f.gen = gen
				w.usedFns = append(w.usedFns, ln.fn)
			}
			w.sub = appendVarintField(w.sub[:0], tagLineFunctionID, uint64(ln.fn)+1)
			w.sub = appendVarintField(w.sub, tagLineLine, uint64(ln.line))
			m = appendBytesField(m, tagLocationLine, w.sub)
		}
		w.msg = m
		w.buf = appendBytesField(w.buf, tagProfileLocation, m)
	}
	for _, fi := range w.usedFns {
		f := &w.sym.funcs[fi]
		name := uint64(w.str(f.name))
		m := appendVarintField(w.msg[:0], tagFunctionID, uint64(fi)+1)
		m = appendVarintField(m, tagFunctionName, name)
		m = appendVarintField(m, tagFunctionSystemName, name)
		m = appendVarintField(m, tagFunctionFilename, uint64(w.str(f.file)))
		w.msg = m
		w.buf = appendBytesField(w.buf, tagProfileFunction, m)
	}
	for i := range w.sym.mem {
		mm := &w.sym.mem[i]
		m := appendVarintField(w.msg[:0], tagMappingID, uint64(i+1))
		m = appendVarintField(m, tagMappingStart, uint64(mm.start))
		m = appendVarintField(m, tagMappingLimit, uint64(mm.end))
		m = appendVarintField(m, tagMappingOffset, mm.offset)
		m = appendVarintField(m, tagMappingFilename, uint64(w.str(mm.file)))
		m = appendVarintField(m, tagMappingBuildID, uint64(w.str(mm.buildID)))
		if mm.funcs == lookupTried {
			m = appendVarintField(m, tagMappingHasFunctions, 1)
		}
		w.msg = m
		w.buf = appendBytesField(w.buf, tagProfileMapping, m)
	}
	w.buf = appendVarintField(w.buf, tagProfileTimeNanos, uint64(time.Now().UnixNano()))
	for _, s := range w.strs {
		w.buf = protowire.AppendTag(w.buf, tagProfileStringTable, protowire.BytesType)
		w.buf = protowire.AppendString(w.buf, s)
	}
	return w.buf
}
