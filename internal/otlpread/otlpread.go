// Package otlpread flattens OTLP Profiles back into per-sample rows. It is
// the inverse used by tests, golden fixtures and comparison tools.
package otlpread

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/google/pprof/profile"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// Row is one sample value.
type Row struct {
	Service string
	Name    string // go.pprof.name
	Type    string
	Unit    string
	Stack   []string // root first, function names (inlined frames expanded)
	Labels  map[string]string
	TraceID string
	SpanID  string
	Value   int64
}

// Rows flattens a request. It returns an error on any out-of-range index.
func Rows(req *collpb.ExportProfilesServiceRequest) ([]Row, error) {
	d := req.GetDictionary()
	if d == nil {
		return nil, nil
	}
	str := func(i int32) (string, error) {
		if i < 0 || int(i) >= len(d.StringTable) {
			return "", fmt.Errorf("string index %d out of range", i)
		}
		return d.StringTable[i], nil
	}
	var rows []Row
	for _, rp := range req.ResourceProfiles {
		svc := ""
		for _, kv := range rp.GetResource().GetAttributes() {
			if kv.Key == "service.name" {
				svc = kv.GetValue().GetStringValue()
			}
		}
		for _, sp := range rp.ScopeProfiles {
			for _, p := range sp.Profiles {
				typ, err := str(p.GetSampleType().GetTypeStrindex())
				if err != nil {
					return nil, err
				}
				unit, err := str(p.GetSampleType().GetUnitStrindex())
				if err != nil {
					return nil, err
				}
				name := ""
				for _, ai := range p.AttributeIndices {
					a := d.AttributeTable[ai]
					if k, _ := str(a.KeyStrindex); k == "go.pprof.name" {
						name = a.GetValue().GetStringValue()
					}
				}
				for _, s := range p.Samples {
					if int(s.StackIndex) >= len(d.StackTable) {
						return nil, fmt.Errorf("stack index %d out of range", s.StackIndex)
					}
					var stack []string
					for _, li := range d.StackTable[s.StackIndex].LocationIndices {
						if int(li) >= len(d.LocationTable) {
							return nil, fmt.Errorf("location index %d out of range", li)
						}
						loc := d.LocationTable[li]
						if len(loc.Lines) == 0 {
							stack = append(stack, fmt.Sprintf("0x%x", loc.Address))
						}
						for _, ln := range loc.Lines {
							if int(ln.FunctionIndex) >= len(d.FunctionTable) {
								return nil, fmt.Errorf("function index %d out of range", ln.FunctionIndex)
							}
							fn, err := str(d.FunctionTable[ln.FunctionIndex].NameStrindex)
							if err != nil {
								return nil, err
							}
							stack = append(stack, fn)
						}
					}
					reverse(stack)
					labels := map[string]string{}
					for _, ai := range s.AttributeIndices {
						if int(ai) >= len(d.AttributeTable) {
							return nil, fmt.Errorf("attribute index %d out of range", ai)
						}
						a := d.AttributeTable[ai]
						k, err := str(a.KeyStrindex)
						if err != nil {
							return nil, err
						}
						labels[k] = anyString(a.Value)
					}
					r := Row{Service: svc, Name: name, Type: typ, Unit: unit, Stack: stack, Labels: labels}
					if s.LinkIndex != 0 {
						if int(s.LinkIndex) >= len(d.LinkTable) {
							return nil, fmt.Errorf("link index %d out of range", s.LinkIndex)
						}
						l := d.LinkTable[s.LinkIndex]
						r.TraceID, r.SpanID = hex.EncodeToString(l.TraceId), hex.EncodeToString(l.SpanId)
					}
					if len(s.Values) == 0 {
						r.Value = int64(len(s.TimestampsUnixNano))
						rows = append(rows, r)
						continue
					}
					for _, v := range s.Values {
						r.Value = v
						rows = append(rows, r)
					}
				}
			}
		}
	}
	return rows, nil
}

func anyString(v *commonpb.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprint(x.IntValue)
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprint(x.BoolValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprint(x.DoubleValue)
	case *commonpb.AnyValue_ArrayValue:
		var parts []string
		for _, e := range x.ArrayValue.Values {
			parts = append(parts, anyString(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	return ""
}

func reverse(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// Key identifies a stack with its labels and link, for totals.
func (r Row) Key() string {
	var lk []string
	for k, v := range r.Labels {
		lk = append(lk, k+"="+v)
	}
	sort.Strings(lk)
	return r.Type + "|" + strings.Join(r.Stack, ";") + "|" + strings.Join(lk, ",") + "|" + r.TraceID + r.SpanID
}

// PprofRows flattens a pprof profile the same way, treating the
// traceLabel/spanLabel labels as the link. Zero values are dropped.
func PprofRows(p *profile.Profile, traceLabel, spanLabel string) []Row {
	var rows []Row
	for _, s := range p.Sample {
		var stack []string
		for _, l := range s.Location {
			if len(l.Line) == 0 {
				stack = append(stack, fmt.Sprintf("0x%x", l.Address))
			}
			for _, ln := range l.Line {
				name := ""
				if ln.Function != nil {
					name = ln.Function.Name
				}
				stack = append(stack, name)
			}
		}
		reverse(stack)
		labels := map[string]string{}
		var tid, sid string
		for k, vs := range s.Label {
			if len(vs) == 0 {
				continue
			}
			v := vs[0]
			if len(vs) > 1 {
				v = "[" + strings.Join(vs, ",") + "]"
			}
			labels[k] = v
		}
		if t, sp := labels[traceLabel], labels[spanLabel]; len(t) == 32 && len(sp) == 16 {
			if _, e1 := hex.DecodeString(t); e1 == nil {
				if _, e2 := hex.DecodeString(sp); e2 == nil && t != strings.Repeat("0", 32) && sp != strings.Repeat("0", 16) {
					tid, sid = t, sp
					delete(labels, traceLabel)
					delete(labels, spanLabel)
				}
			}
		}
		for k, vs := range s.NumLabel {
			if _, dup := s.Label[k]; dup || len(vs) == 0 {
				continue
			}
			if len(vs) == 1 {
				labels[k] = fmt.Sprint(vs[0])
			} else {
				var parts []string
				for _, v := range vs {
					parts = append(parts, fmt.Sprint(v))
				}
				labels[k] = "[" + strings.Join(parts, ",") + "]"
			}
		}
		for i, st := range p.SampleType {
			if i >= len(s.Value) || s.Value[i] == 0 {
				continue
			}
			rows = append(rows, Row{Type: st.Type, Unit: st.Unit, Stack: stack, Labels: labels, TraceID: tid, SpanID: sid, Value: s.Value[i]})
		}
	}
	return rows
}

// Totals sums values by Key.
func Totals(rows []Row) map[string]int64 {
	m := map[string]int64{}
	for _, r := range rows {
		m[r.Key()] += r.Value
	}
	return m
}

// Dump renders rows as sorted text lines, for golden files.
func Dump(rows []Row) string {
	t := Totals(rows)
	var lines []string
	for k, v := range t {
		lines = append(lines, fmt.Sprintf("%s %d", k, v))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}
