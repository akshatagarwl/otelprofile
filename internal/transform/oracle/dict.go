package oracle

import (
	"encoding/binary"
	"math"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
)

type functionKey struct {
	name, systemName, filename int32
	startLine                  int64
}

type mappingKey struct {
	start, limit, offset uint64
	filename             int32
	attrs                string
}

type locationKey struct {
	mapping int32
	address uint64
	lines   string
	attrs   string
}

type attrKey struct {
	key, unit int32
	kind      byte
	value     string
}

// dictionary accumulates one ProfilesDictionary shared by every profile in a
// request.
type dictionary struct {
	pb *profilespb.ProfilesDictionary

	strings   map[string]int32
	functions map[functionKey]int32
	mappings  map[mappingKey]int32
	locations map[locationKey]int32
	stacks    map[string]int32
	attrs     map[attrKey]int32
	links     map[[24]byte]int32

	scratch []byte
}

func newDictionary() *dictionary {
	return &dictionary{
		pb: &profilespb.ProfilesDictionary{
			MappingTable:  []*profilespb.Mapping{{}},
			LocationTable: []*profilespb.Location{{}},
			FunctionTable: []*profilespb.Function{{}},
			// profcheck requires the literal zero Link{} at index 0, although the
			// proto comment suggests zero-filled 16/8-byte IDs.
			LinkTable:      []*profilespb.Link{{}},
			StringTable:    []string{""},
			AttributeTable: []*profilespb.KeyValueAndUnit{{}},
			StackTable:     []*profilespb.Stack{{}},
		},
		// Each table's zero value is already at index 0; map it so an empty
		// stack (or location, mapping, function) reuses it instead of
		// adding a duplicate.
		strings:   map[string]int32{"": 0},
		functions: map[functionKey]int32{{}: 0},
		mappings:  map[mappingKey]int32{{}: 0},
		locations: map[locationKey]int32{{}: 0},
		stacks:    map[string]int32{"": 0},
		attrs:     map[attrKey]int32{},
		links:     map[[24]byte]int32{},
	}
}

func (d *dictionary) str(s string) int32 {
	if i, ok := d.strings[s]; ok {
		return i
	}
	i := int32(len(d.pb.StringTable))
	d.pb.StringTable = append(d.pb.StringTable, s)
	d.strings[s] = i
	return i
}

func (d *dictionary) function(name, systemName, filename string, startLine int64) int32 {
	k := functionKey{d.str(name), d.str(systemName), d.str(filename), startLine}
	if i, ok := d.functions[k]; ok {
		return i
	}
	i := int32(len(d.pb.FunctionTable))
	d.pb.FunctionTable = append(d.pb.FunctionTable, &profilespb.Function{
		NameStrindex:       k.name,
		SystemNameStrindex: k.systemName,
		FilenameStrindex:   k.filename,
		StartLine:          startLine,
	})
	d.functions[k] = i
	return i
}

func (d *dictionary) indicesKey(idx []int32) string {
	d.scratch = d.scratch[:0]
	for _, v := range idx {
		d.scratch = binary.AppendUvarint(d.scratch, uint64(v))
	}
	return string(d.scratch)
}

func (d *dictionary) mapping(m *profilespb.Mapping) int32 {
	k := mappingKey{m.MemoryStart, m.MemoryLimit, m.FileOffset, m.FilenameStrindex, d.indicesKey(m.AttributeIndices)}
	if i, ok := d.mappings[k]; ok {
		return i
	}
	i := int32(len(d.pb.MappingTable))
	d.pb.MappingTable = append(d.pb.MappingTable, m)
	d.mappings[k] = i
	return i
}

func (d *dictionary) location(l *profilespb.Location) int32 {
	var lines []byte
	for _, ln := range l.Lines {
		lines = binary.AppendUvarint(lines, uint64(ln.FunctionIndex))
		lines = binary.AppendVarint(lines, ln.Line)
		lines = binary.AppendVarint(lines, ln.Column)
	}
	k := locationKey{l.MappingIndex, l.Address, string(lines), d.indicesKey(l.AttributeIndices)}
	if i, ok := d.locations[k]; ok {
		return i
	}
	i := int32(len(d.pb.LocationTable))
	d.pb.LocationTable = append(d.pb.LocationTable, l)
	d.locations[k] = i
	return i
}

func (d *dictionary) stack(locs []int32) int32 {
	k := d.indicesKey(locs)
	if i, ok := d.stacks[k]; ok {
		return i
	}
	i := int32(len(d.pb.StackTable))
	d.pb.StackTable = append(d.pb.StackTable, &profilespb.Stack{LocationIndices: append([]int32(nil), locs...)})
	d.stacks[k] = i
	return i
}

func (d *dictionary) link(traceID [16]byte, spanID [8]byte) int32 {
	var k [24]byte
	copy(k[:16], traceID[:])
	copy(k[16:], spanID[:])
	if i, ok := d.links[k]; ok {
		return i
	}
	i := int32(len(d.pb.LinkTable))
	d.pb.LinkTable = append(d.pb.LinkTable, &profilespb.Link{
		TraceId: append([]byte(nil), traceID[:]...),
		SpanId:  append([]byte(nil), spanID[:]...),
	})
	d.links[k] = i
	return i
}

const (
	kindString byte = iota + 1
	kindInt
	kindBool
	kindStrings
	kindInts
)

func (d *dictionary) attr(key string, unit string, k attrKey, v *commonpb.AnyValue) int32 {
	k.key = d.str(key)
	if unit != "" {
		k.unit = d.str(unit)
	}
	if i, ok := d.attrs[k]; ok {
		return i
	}
	i := int32(len(d.pb.AttributeTable))
	d.pb.AttributeTable = append(d.pb.AttributeTable, &profilespb.KeyValueAndUnit{
		KeyStrindex:  k.key,
		Value:        v,
		UnitStrindex: k.unit,
	})
	d.attrs[k] = i
	return i
}

func (d *dictionary) attrString(key, value string) int32 {
	return d.attr(key, "", attrKey{kind: kindString, value: value},
		&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}})
}

func (d *dictionary) attrBool(key string, value bool) int32 {
	s := "f"
	if value {
		s = "t"
	}
	return d.attr(key, "", attrKey{kind: kindBool, value: s},
		&commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: value}})
}

func (d *dictionary) attrInt(key, unit string, value int64) int32 {
	return d.attr(key, unit, attrKey{kind: kindInt, value: string(binary.AppendVarint(nil, value))},
		&commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value}})
}

func (d *dictionary) attrStrings(key string, values []string) int32 {
	vals := make([]*commonpb.AnyValue, len(values))
	for i, v := range values {
		vals[i] = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}
	}
	return d.attr(key, "", attrKey{kind: kindStrings, value: strings.Join(values, "\x00")},
		&commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: vals}}})
}

func (d *dictionary) attrInts(key, unit string, values []int64) int32 {
	vals := make([]*commonpb.AnyValue, len(values))
	var enc []byte
	for i, v := range values {
		vals[i] = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}
		enc = binary.AppendVarint(enc, v)
	}
	return d.attr(key, unit, attrKey{kind: kindInts, value: string(enc)},
		&commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: vals}}})
}

// size is a cheap upper-bound estimate of the dictionary's encoded size,
// used only to decide when to split requests.
func (d *dictionary) size() int {
	n := 0
	for _, s := range d.pb.StringTable {
		n += len(s) + 3
	}
	n += len(d.pb.FunctionTable) * 12
	n += len(d.pb.LocationTable) * 24
	n += len(d.pb.MappingTable) * 32
	n += len(d.pb.AttributeTable) * 16
	n += len(d.pb.LinkTable) * 30
	for _, s := range d.pb.StackTable {
		n += 4 + len(s.LocationIndices)*3
	}
	return int(math.Min(float64(n), math.MaxInt32))
}
