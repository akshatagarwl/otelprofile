package transform_test

import (
	"bytes"
	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"github.com/akshatagarwl/otelprofile-go/internal/transform/oracle"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile-go/internal/otlpread"
	"github.com/google/pprof/profile"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

func encodeWith(t testing.TB, e *transform.Encoder, ins []transform.Input) *collpb.ExportProfilesServiceRequest {
	t.Helper()
	res, err := proto.Marshal(testResource())
	if err != nil {
		t.Fatal(err)
	}
	out, n, err := e.Encode(res, transform.EncScope{Name: testScope().Name, Version: testScope().Version}, ins)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return &collpb.ExportProfilesServiceRequest{}
	}
	var req collpb.ExportProfilesServiceRequest
	if err := proto.Unmarshal(out, &req); err != nil {
		t.Fatalf("encoder output does not decode: %v", err)
	}
	return &req
}

// TestEncoderMatchesBuilder checks the streaming encoder against the
// google/pprof-based Builder on every fixture, alone and all together, and
// twice in a row on the same Encoder (state reuse).
func TestEncoderMatchesBuilder(t *testing.T) {
	e := transform.NewEncoder(transform.Options{})
	var all []transform.Input
	for _, name := range fixtureNames {
		in := transform.Input{Name: name, Start: time.Unix(1700000000, 0), Duration: 15 * time.Second, Pprof: loadFixture(t, name)}
		all = append(all, in)
		for round := 0; round < 2; round++ {
			got := encodeWith(t, e, []transform.Input{in})
			checkConformance(t, got)
			b := oracle.NewBuilder(transform.Options{})
			if _, err := b.Add(testResource(), testScope(), toOracle(in)); err != nil {
				t.Fatal(err)
			}
			compareRows(t, name, b.Request(), got)
		}
	}
	got := encodeWith(t, e, all)
	checkConformance(t, got)
	b := oracle.NewBuilder(transform.Options{})
	for _, in := range all {
		if _, err := b.Add(testResource(), testScope(), toOracle(in)); err != nil {
			t.Fatal(err)
		}
	}
	compareRows(t, "all", b.Request(), got)
	if len(got.ResourceProfiles[0].ScopeProfiles) != len(fixtureNames) {
		t.Fatal("one ScopeProfiles per pprof")
	}
}

func compareRows(t *testing.T, name string, want, got *collpb.ExportProfilesServiceRequest) {
	t.Helper()
	wr, err := otlpread.Rows(want)
	if err != nil {
		t.Fatal(err)
	}
	gr, err := otlpread.Rows(got)
	if err != nil {
		t.Fatal(err)
	}
	if otlpread.Dump(wr) != otlpread.Dump(gr) {
		t.Fatalf("%s: encoder rows differ from builder rows\nwant:\n%s\ngot:\n%s", name, otlpread.Dump(wr), otlpread.Dump(gr))
	}
	for i := range wr {
		if wr[i].Name != gr[i].Name {
			t.Fatalf("profile name %q vs %q", wr[i].Name, gr[i].Name)
		}
	}
}

func TestEncoderSyntheticCases(t *testing.T) {
	cases := map[string]func(*profile.Profile){
		"empty stack": func(p *profile.Profile) { p.Sample = append(p.Sample, &profile.Sample{Value: []int64{2, 20000000}}) },
		"multi labels": func(p *profile.Profile) {
			p.Sample[0].Label = map[string][]string{"k": {"a", "b"}, "z": {"1"}}
			p.Sample[0].NumLabel = map[string][]int64{"bytes": {512}, "k": {9}}
			p.Sample[0].NumUnit = map[string][]string{"bytes": {"bytes"}, "k": {""}}
		},
		"bad trace": func(p *profile.Profile) {
			p.Sample[0].Label = map[string][]string{transform.LabelTraceID: {"nothex"}, transform.LabelSpanID: {"b7ad6b7169203331"}}
		},
		"link": func(p *profile.Profile) {
			p.Sample[0].Label = map[string][]string{transform.LabelTraceID: {"0af7651916cd43dd8448eb211c80319c"}, transform.LabelSpanID: {"b7ad6b7169203331"}, "e": {"x"}}
			p.Sample[1].Label = p.Sample[0].Label
		},
		"comments and frames": func(p *profile.Profile) {
			p.Comments = []string{"c1", "c2"}
			p.DropFrames, p.KeepFrames, p.DocURL = "drop.*", "keep", "https://x"
			p.DefaultSampleType = "cpu"
			p.Location[0].IsFolded = true
			p.Mapping[0].BuildID = "abc"
		},
		"zero type": func(p *profile.Profile) {
			for _, s := range p.Sample {
				s.Value[0] = 0
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := synthetic()
			mutate(p)
			var buf bytes.Buffer
			if err := p.Write(&buf); err != nil {
				t.Fatal(err)
			}
			in := transform.Input{Name: "cpu", Start: time.Unix(1, 0), Pprof: buf.Bytes()}
			got := encodeWith(t, transform.NewEncoder(transform.Options{}), []transform.Input{in})
			checkConformance(t, got)
			b := oracle.NewBuilder(transform.Options{})
			if _, err := b.Add(testResource(), testScope(), toOracle(in)); err != nil {
				t.Fatal(err)
			}
			compareRows(t, name, b.Request(), got)
		})
	}
}

func FuzzEncoder(f *testing.F) {
	for _, name := range fixtureNames {
		f.Add(loadFixture(f, name))
	}
	var buf bytes.Buffer
	_ = synthetic().Write(&buf)
	f.Add(buf.Bytes())
	e := transform.NewEncoder(transform.Options{})
	f.Fuzz(func(t *testing.T, data []byte) {
		res, _ := proto.Marshal(testResource())
		out, n, err := e.Encode(res, transform.EncScope{Name: "fuzz"}, []transform.Input{{Name: "fuzz", Start: time.Unix(1, 0), Pprof: data}})
		if err != nil || n == 0 {
			return
		}
		var req collpb.ExportProfilesServiceRequest
		if err := proto.Unmarshal(out, &req); err != nil {
			t.Fatalf("output does not decode: %v", err)
		}
		checkConformance(t, &req)
		// Whatever google/pprof also accepts must give the same rows.
		if p, perr := profile.ParseData(data); perr == nil {
			b := oracle.NewBuilder(transform.Options{})
			if _, berr := b.Add(testResource(), testScope(), oracle.Input{Name: "fuzz", Start: time.Unix(1, 0), Parsed: p}); berr == nil {
				compareRows(t, "fuzz", b.Request(), &req)
			}
		}
	})
}

func BenchmarkEncoderCPU(b *testing.B)  { benchEncoder(b, "cpu") }
func BenchmarkEncoderHeap(b *testing.B) { benchEncoder(b, "heap") }

func benchEncoder(b *testing.B, name string) {
	raw := loadFixture(b, name)
	res, _ := proto.Marshal(testResource())
	e := transform.NewEncoder(transform.Options{})
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := e.Encode(res, transform.EncScope{Name: "b"}, []transform.Input{{Name: name, Pprof: raw}}); err != nil {
			b.Fatal(err)
		}
	}
}

func toOracle(in transform.Input) oracle.Input {
	return oracle.Input{Name: in.Name, Start: in.Start, Duration: in.Duration, Pprof: in.Pprof}
}
