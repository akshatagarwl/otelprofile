package transform_test

import (
	"bytes"
	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"github.com/akshatagarwl/otelprofile-go/internal/transform/oracle"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/pprof/profile"
)

// FuzzTransform feeds arbitrary bytes. Whatever google/pprof accepts must
// convert without panicking, and the output must pass profcheck.
func FuzzTransform(f *testing.F) {
	for _, name := range fixtureNames {
		f.Add(loadFixture(f, name))
	}
	var buf bytes.Buffer
	_ = synthetic().Write(&buf)
	f.Add(buf.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := profile.ParseData(data)
		if err != nil || !validUTF8(p) {
			// OTLP strings must be UTF-8; the Go runtime only writes
			// UTF-8. The uncompressed heap/mutex/block seeds let the
			// fuzzer mutate string bytes directly.
			return
		}
		b := oracle.NewBuilder(transform.Options{})
		if _, err := b.Add(testResource(), testScope(), oracle.Input{Name: "fuzz", Start: time.Unix(1, 0), Parsed: p}); err != nil {
			return
		}
		if b.Samples() == 0 {
			return
		}
		checkConformance(t, b.Request())
	})
}

// validUTF8 reports whether every string in p is valid UTF-8.
func validUTF8(p *profile.Profile) bool {
	ok := func(ss ...string) bool {
		for _, s := range ss {
			if !utf8.ValidString(s) {
				return false
			}
		}
		return true
	}
	for _, st := range append(append([]*profile.ValueType{}, p.SampleType...), p.PeriodType) {
		if st != nil && !ok(st.Type, st.Unit) {
			return false
		}
	}
	for _, f := range p.Function {
		if !ok(f.Name, f.SystemName, f.Filename) {
			return false
		}
	}
	for _, m := range p.Mapping {
		if !ok(m.File, m.BuildID) {
			return false
		}
	}
	for _, s := range p.Sample {
		for k, vs := range s.Label {
			if !ok(k) || !ok(vs...) {
				return false
			}
		}
		for k, us := range s.NumUnit {
			if !ok(k) || !ok(us...) {
				return false
			}
		}
		for k := range s.NumLabel {
			if !ok(k) {
				return false
			}
		}
	}
	return ok(p.Comments...) && ok(p.DropFrames, p.KeepFrames, p.DefaultSampleType, p.DocURL)
}
