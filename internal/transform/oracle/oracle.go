// Package oracle is an independent pprof → OTLP converter built on
// google/pprof and the generated OTLP protos. It is the reference the
// production streaming encoder (package transform) is tested against, and it
// is not linked into programs that use otelprofile.
package oracle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/akshatagarwl/otelprofile/internal/transform"
	"github.com/google/pprof/profile"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Attribute keys, as written by transform.
const (
	AttrProfileName     = transform.AttrProfileName
	LabelTraceID        = transform.LabelTraceID
	LabelSpanID         = transform.LabelSpanID
	attrComment         = "pprof.profile.comment"
	attrDocURL          = "pprof.profile.doc_url"
	attrDropFrames      = "pprof.profile.drop_frames"
	attrKeepFrames      = "pprof.profile.keep_frames"
	attrDefaultType     = "pprof.scope.default_sample_type"
	attrSampleTypeOrder = "pprof.scope.sample_type_order"
	attrIsFolded        = "pprof.location.is_folded"
	attrHasFunctions    = "pprof.mapping.has_functions"
	attrHasFilenames    = "pprof.mapping.has_filenames"
	attrHasLineNumbers  = "pprof.mapping.has_line_numbers"
	attrHasInline       = "pprof.mapping.has_inline_frames"
	attrBuildID         = "process.executable.build_id.gnu"
)

// Options and Input are transform's.
type (
	Options = transform.Options
)

// Input is one profile; Parsed may be set instead of Pprof.
type Input struct {
	Name     string
	Start    time.Time
	Duration time.Duration
	Pprof    []byte
	Parsed   *profile.Profile
}

// Builder accumulates profiles into a single ExportProfilesServiceRequest
// sharing one dictionary.
type Builder struct {
	opts    Options
	dict    *dictionary
	rps     []*profilespb.ResourceProfiles
	rpByRes map[*resourcepb.Resource]*profilespb.ResourceProfiles
	samples int
	valsize int
}

// NewBuilder returns an empty Builder.
func NewBuilder(opts Options) *Builder {
	if opts.TraceIDLabel == "" {
		opts.TraceIDLabel = LabelTraceID
	}
	if opts.SpanIDLabel == "" {
		opts.SpanIDLabel = LabelSpanID
	}
	if opts.ProfileID == nil {
		opts.ProfileID = newProfileID
	}
	return &Builder{opts: opts, dict: newDictionary(), rpByRes: map[*resourcepb.Resource]*profilespb.ResourceProfiles{}}
}

// Samples is the number of samples added so far.
func (b *Builder) Samples() int { return b.samples }

// EstimatedSize is a rough estimate of the encoded request size in bytes.
func (b *Builder) EstimatedSize() int { return b.dict.size() + b.valsize }

// Request returns the built request. The Builder must not be used afterwards.
func (b *Builder) Request() *collpb.ExportProfilesServiceRequest {
	return &collpb.ExportProfilesServiceRequest{ResourceProfiles: b.rps, Dictionary: b.dict.pb}
}

// Add converts one pprof profile and appends it under res/scope. Each input
// becomes one ScopeProfiles holding one Profile per pprof sample type, in the
// pprof order. Sample types with no non-zero samples are omitted. It returns
// the number of samples added.
func (b *Builder) Add(res *resourcepb.Resource, scope *commonpb.InstrumentationScope, in Input) (int, error) {
	p := in.Parsed
	if p == nil {
		var err error
		p, err = profile.ParseData(in.Pprof)
		if err != nil {
			return 0, fmt.Errorf("parse pprof %q: %w", in.Name, err)
		}
	}

	start := in.Start
	if start.IsZero() && p.TimeNanos != 0 {
		start = time.Unix(0, p.TimeNanos)
	}
	dur := in.Duration
	if dur == 0 {
		dur = time.Duration(p.DurationNanos)
	}

	c := converter{b: b, p: p, locs: make(map[*profile.Location]int32, len(p.Location))}
	var profs []*profilespb.Profile
	for ti, st := range p.SampleType {
		samples := c.samples(ti)
		if len(samples) == 0 {
			continue
		}
		pr := &profilespb.Profile{
			SampleType:   &profilespb.ValueType{TypeStrindex: b.dict.str(st.Type), UnitStrindex: b.dict.str(st.Unit)},
			Samples:      samples,
			TimeUnixNano: uint64(max(start.UnixNano(), 0)),
			DurationNano: uint64(max(dur, 0)),
			ProfileId:    b.opts.ProfileID(),
		}
		if p.PeriodType != nil {
			pr.PeriodType = &profilespb.ValueType{TypeStrindex: b.dict.str(p.PeriodType.Type), UnitStrindex: b.dict.str(p.PeriodType.Unit)}
			pr.Period = p.Period
		}
		if in.Name != "" {
			pr.AttributeIndices = append(pr.AttributeIndices, b.dict.attrString(AttrProfileName, in.Name))
		}
		if len(p.Comments) > 0 {
			pr.AttributeIndices = append(pr.AttributeIndices, b.dict.attrStrings(attrComment, p.Comments))
		}
		if p.DocURL != "" {
			pr.AttributeIndices = append(pr.AttributeIndices, b.dict.attrString(attrDocURL, p.DocURL))
		}
		if p.DropFrames != "" {
			pr.AttributeIndices = append(pr.AttributeIndices, b.dict.attrString(attrDropFrames, p.DropFrames))
		}
		if p.KeepFrames != "" {
			pr.AttributeIndices = append(pr.AttributeIndices, b.dict.attrString(attrKeepFrames, p.KeepFrames))
		}
		profs = append(profs, pr)
		b.samples += len(samples)
		b.valsize += len(samples) * 12
	}
	if len(profs) == 0 {
		return 0, nil
	}

	sc := &commonpb.InstrumentationScope{}
	if scope != nil {
		sc.Name, sc.Version = scope.Name, scope.Version
		sc.Attributes = append(sc.Attributes, scope.Attributes...)
	}
	if p.DefaultSampleType != "" {
		sc.Attributes = append(sc.Attributes, &commonpb.KeyValue{Key: attrDefaultType,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: p.DefaultSampleType}}})
	}
	if len(profs) != len(p.SampleType) || len(p.SampleType) > 1 {
		// Record the pprof index of each emitted sample type so the original
		// order can be restored.
		var order []*commonpb.AnyValue
		for ti := range p.SampleType {
			if c.kept[ti] {
				order = append(order, &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(ti)}})
			}
		}
		sc.Attributes = append(sc.Attributes, &commonpb.KeyValue{Key: attrSampleTypeOrder,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: order}}}})
	}

	rp := b.rpByRes[res]
	if rp == nil {
		rp = &profilespb.ResourceProfiles{Resource: res}
		b.rpByRes[res] = rp
		b.rps = append(b.rps, rp)
	}
	rp.ScopeProfiles = append(rp.ScopeProfiles, &profilespb.ScopeProfiles{Scope: sc, Profiles: profs})
	n := 0
	for _, pr := range profs {
		n += len(pr.Samples)
	}
	return n, nil
}

type converter struct {
	b    *Builder
	p    *profile.Profile
	locs map[*profile.Location]int32
	kept map[int]bool
	idx  []int32
}

func (c *converter) samples(ti int) []*profilespb.Sample {
	var out []*profilespb.Sample
	// Samples with the same identity (stack, attributes, link) are merged,
	// as the proto recommends.
	type identity struct {
		stack int32
		attrs string
		link  int32
	}
	seen := map[identity]int{}
	for _, s := range c.p.Sample {
		if ti >= len(s.Value) {
			continue
		}
		v := s.Value[ti]
		if v == 0 && !c.b.opts.KeepZeroSamples {
			continue
		}
		c.idx = c.idx[:0]
		for _, l := range s.Location {
			c.idx = append(c.idx, c.location(l))
		}
		stack := c.b.dict.stack(c.idx)
		attrs, link := c.labels(s)
		id := identity{stack, c.b.dict.indicesKey(attrs), link}
		if i, ok := seen[id]; ok {
			out[i].Values[0] += v
			continue
		}
		seen[id] = len(out)
		out = append(out, &profilespb.Sample{
			StackIndex:       stack,
			AttributeIndices: attrs,
			LinkIndex:        link,
			Values:           []int64{v},
		})
	}
	if len(out) > 0 {
		if c.kept == nil {
			c.kept = map[int]bool{}
		}
		c.kept[ti] = true
	}
	return out
}

func (c *converter) labels(s *profile.Sample) ([]int32, int32) {
	if len(s.Label) == 0 && len(s.NumLabel) == 0 {
		return nil, 0
	}
	d := c.b.dict
	var link int32
	traceHex, spanHex := first(s.Label[c.b.opts.TraceIDLabel]), first(s.Label[c.b.opts.SpanIDLabel])
	linked := false
	if traceHex != "" && spanHex != "" {
		var tid [16]byte
		var sid [8]byte
		if decodeHex(tid[:], traceHex) && decodeHex(sid[:], spanHex) && tid != [16]byte{} && sid != [8]byte{} {
			link = d.link(tid, sid)
			linked = true
		}
	}
	keys := make([]string, 0, len(s.Label)+len(s.NumLabel))
	for k := range s.Label {
		if linked && (k == c.b.opts.TraceIDLabel || k == c.b.opts.SpanIDLabel) {
			continue
		}
		keys = append(keys, k)
	}
	for k := range s.NumLabel {
		if _, dup := s.Label[k]; !dup {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var attrs []int32
	for _, k := range keys {
		if vs, ok := s.Label[k]; ok && len(vs) > 0 {
			if len(vs) == 1 {
				attrs = append(attrs, d.attrString(k, vs[0]))
			} else {
				attrs = append(attrs, d.attrStrings(k, vs))
			}
			continue
		}
		vs := s.NumLabel[k]
		if len(vs) == 0 {
			continue
		}
		unit := first(s.NumUnit[k])
		if len(vs) == 1 {
			attrs = append(attrs, d.attrInt(k, unit, vs[0]))
		} else {
			attrs = append(attrs, d.attrInts(k, unit, vs))
		}
	}
	return attrs, link
}

func (c *converter) location(l *profile.Location) int32 {
	if i, ok := c.locs[l]; ok {
		return i
	}
	d := c.b.dict
	loc := &profilespb.Location{Address: l.Address}
	if l.Mapping != nil {
		loc.MappingIndex = c.mapping(l.Mapping)
	}
	for _, ln := range l.Line {
		pl := &profilespb.Line{Line: ln.Line, Column: ln.Column}
		if f := ln.Function; f != nil && (f.Name != "" || f.SystemName != "" || f.Filename != "") {
			pl.FunctionIndex = d.function(f.Name, f.SystemName, f.Filename, f.StartLine)
		}
		loc.Lines = append(loc.Lines, pl)
	}
	if l.IsFolded {
		loc.AttributeIndices = []int32{d.attrBool(attrIsFolded, true)}
	}
	i := d.location(loc)
	c.locs[l] = i
	return i
}

func (c *converter) mapping(m *profile.Mapping) int32 {
	d := c.b.dict
	pm := &profilespb.Mapping{
		MemoryStart: m.Start,
		MemoryLimit: m.Limit,
		FileOffset:  m.Offset,
	}
	if m.File != "" {
		pm.FilenameStrindex = d.str(m.File)
	}
	if m.HasFunctions {
		pm.AttributeIndices = append(pm.AttributeIndices, d.attrBool(attrHasFunctions, true))
	}
	if m.HasFilenames {
		pm.AttributeIndices = append(pm.AttributeIndices, d.attrBool(attrHasFilenames, true))
	}
	if m.HasLineNumbers {
		pm.AttributeIndices = append(pm.AttributeIndices, d.attrBool(attrHasLineNumbers, true))
	}
	if m.HasInlineFrames {
		pm.AttributeIndices = append(pm.AttributeIndices, d.attrBool(attrHasInline, true))
	}
	if m.BuildID != "" {
		pm.AttributeIndices = append(pm.AttributeIndices, d.attrString(attrBuildID, m.BuildID))
	}
	if pm.MemoryStart != 0 && pm.MemoryLimit <= pm.MemoryStart {
		// profcheck requires start < limit or both zero.
		pm.MemoryStart, pm.MemoryLimit = 0, 0
	}
	return d.mapping(pm)
}

func first(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

func decodeHex(dst []byte, s string) bool {
	if len(s) != 2*len(dst) {
		return false
	}
	_, err := hex.Decode(dst, []byte(s))
	return err == nil
}

func newProfileID() []byte {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return id
}
