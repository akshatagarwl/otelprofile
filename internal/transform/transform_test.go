package transform_test

import (
	"bytes"
	"encoding/binary"
	"flag"
	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"github.com/akshatagarwl/otelprofile-go/internal/transform/oracle"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile-go/internal/otlpread"
	"github.com/google/pprof/profile"
	"github.com/open-telemetry/sig-profiling/profcheck"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

var update = flag.Bool("update", false, "rewrite golden files")

func testResource() *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "fixture"}}}}}
}

func testScope() *commonpb.InstrumentationScope {
	return &commonpb.InstrumentationScope{Name: "github.com/akshatagarwl/otelprofile-go/profile", Version: "test"}
}

func seqIDs() func() []byte {
	var n uint64
	return func() []byte {
		n++
		b := make([]byte, 16)
		binary.BigEndian.PutUint64(b[8:], n)
		return b
	}
}

func loadFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".pprof"))
	if err != nil {
		t.Fatalf("missing fixture %s (run go test -run TestRecordFixtures -regenerate): %v", name, err)
	}
	return b
}

func checkConformance(t testing.TB, req *collpb.ExportProfilesServiceRequest) {
	t.Helper()
	// Round-trip through the wire format first, like a receiver would.
	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var data profilespb.ProfilesData
	if err := proto.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	c := profcheck.ConformanceChecker{CheckDictionaryDuplicates: true, CheckSampleTimestampShape: true, CheckDictionaryOrphans: true}
	if err := c.Check(&data); err != nil {
		t.Fatalf("profcheck: %v", err)
	}
}

// TestFixturesConformAndRoundTrip converts every recorded runtime profile,
// runs profcheck with all checks, and verifies the OTLP output carries
// exactly the pprof per-stack totals (labels, links and values included).
func TestFixturesConformAndRoundTrip(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			raw := loadFixture(t, name)
			p, err := profile.ParseData(raw)
			if err != nil {
				t.Fatal(err)
			}
			b := oracle.NewBuilder(transform.Options{ProfileID: seqIDs()})
			n, err := b.Add(testResource(), testScope(), oracle.Input{Name: name, Start: time.Unix(1700000000, 0), Duration: 15 * time.Second, Pprof: raw})
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				t.Fatalf("%s: no samples", name)
			}
			req := b.Request()
			checkConformance(t, req)

			rows, err := otlpread.Rows(req)
			if err != nil {
				t.Fatal(err)
			}
			got, want := otlpread.Totals(rows), otlpread.Totals(otlpread.PprofRows(p, transform.LabelTraceID, transform.LabelSpanID))
			if len(got) != len(want) {
				t.Errorf("stack count: got %d want %d", len(got), len(want))
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("total for %q: got %d want %d", k, got[k], v)
				}
			}
			for _, r := range rows {
				if r.Name != name {
					t.Fatalf("go.pprof.name = %q, want %q", r.Name, name)
				}
			}

			// Golden, from the production encoder: catches drift when the
			// proto or the encoder changes.
			encReq := encodeWith(t, transform.NewEncoder(transform.Options{ProfileID: seqIDs()}), []transform.Input{{Name: name, Start: time.Unix(1700000000, 0), Duration: 15 * time.Second, Pprof: raw}})
			checkConformance(t, encReq)
			encRows, err := otlpread.Rows(encReq)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", name+".golden.txt")
			dump := otlpread.Dump(encRows)
			if *update {
				if err := os.WriteFile(golden, []byte(dump), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			wantDump, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if !bytes.Equal(wantDump, []byte(dump)) {
				t.Errorf("golden mismatch for %s; run go test -update and review the diff", name)
			}
		})
	}
}

// TestAllFixturesInOneRequest checks dictionary sharing across profiles.
func TestAllFixturesInOneRequest(t *testing.T) {
	b := oracle.NewBuilder(transform.Options{})
	res, sc := testResource(), testScope()
	for _, name := range fixtureNames {
		if _, err := b.Add(res, sc, oracle.Input{Name: name, Pprof: loadFixture(t, name)}); err != nil {
			t.Fatal(err)
		}
	}
	req := b.Request()
	checkConformance(t, req)
	if len(req.ResourceProfiles) != 1 {
		t.Fatalf("want 1 ResourceProfiles, got %d", len(req.ResourceProfiles))
	}
	if got := len(req.ResourceProfiles[0].ScopeProfiles); got != len(fixtureNames) {
		t.Fatalf("want one ScopeProfiles per pprof, got %d", got)
	}
}

func TestTraceLabelsBecomeLinks(t *testing.T) {
	raw := loadFixture(t, "cpu")
	b := oracle.NewBuilder(transform.Options{})
	if _, err := b.Add(testResource(), testScope(), oracle.Input{Name: "cpu", Pprof: raw}); err != nil {
		t.Fatal(err)
	}
	req := b.Request()
	rows, _ := otlpread.Rows(req)
	linked := 0
	for _, r := range rows {
		if _, ok := r.Labels[transform.LabelTraceID]; ok {
			t.Fatal("trace label left in attributes")
		}
		if r.TraceID != "" {
			linked++
			if r.TraceID != "0af7651916cd43dd8448eb211c80319c" || r.SpanID != "b7ad6b7169203331" {
				t.Fatalf("bad link %s/%s", r.TraceID, r.SpanID)
			}
			if r.Labels["endpoint"] != "/slow" {
				t.Fatalf("other labels must survive: %v", r.Labels)
			}
		}
	}
	if linked == 0 {
		t.Fatal("no linked samples")
	}
	if n := len(req.Dictionary.LinkTable); n != 2 {
		t.Fatalf("link table should hold the zero link plus one: %d", n)
	}
}

func TestInvalidTraceLabelStaysAttribute(t *testing.T) {
	p := synthetic()
	p.Sample[0].Label = map[string][]string{transform.LabelTraceID: {"nothex"}, transform.LabelSpanID: {"b7ad6b7169203331"}}
	b := oracle.NewBuilder(transform.Options{})
	if _, err := b.Add(testResource(), testScope(), oracle.Input{Name: "cpu", Parsed: p}); err != nil {
		t.Fatal(err)
	}
	rows, _ := otlpread.Rows(b.Request())
	if rows[0].TraceID != "" || rows[0].Labels[transform.LabelTraceID] != "nothex" {
		t.Fatalf("invalid trace id must not become a link: %+v", rows[0])
	}
	checkConformance(t, b.Request())
}

func TestEmptyProfileAddsNothing(t *testing.T) {
	p := synthetic()
	for _, s := range p.Sample {
		s.Value = []int64{0, 0}
	}
	b := oracle.NewBuilder(transform.Options{})
	n, err := b.Add(testResource(), testScope(), oracle.Input{Name: "cpu", Parsed: p})
	if err != nil || n != 0 || b.Samples() != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(b.Request().ResourceProfiles) != 0 {
		t.Fatal("empty profile must not emit ResourceProfiles")
	}
	if len(b.Request().Dictionary.StringTable) != 1 {
		t.Fatal("empty profile must not leave orphans in the dictionary")
	}
}

func TestEmptyStackReusesZeroStack(t *testing.T) {
	p := synthetic()
	p.Sample = append(p.Sample, &profile.Sample{Value: []int64{2, 20000000}})
	b := oracle.NewBuilder(transform.Options{})
	if _, err := b.Add(testResource(), testScope(), oracle.Input{Name: "cpu", Parsed: p}); err != nil {
		t.Fatal(err)
	}
	checkConformance(t, b.Request())
	for _, s := range b.Request().ResourceProfiles[0].ScopeProfiles[0].Profiles[0].Samples {
		if len(b.Request().Dictionary.StackTable[s.StackIndex].LocationIndices) == 0 && s.StackIndex != 0 {
			t.Fatal("empty stack must use index 0")
		}
	}
}

func TestMultiValueLabelsAndNumLabels(t *testing.T) {
	p := synthetic()
	p.Sample[0].Label = map[string][]string{"k": {"a", "b"}}
	p.Sample[0].NumLabel = map[string][]int64{"bytes": {512}}
	p.Sample[0].NumUnit = map[string][]string{"bytes": {"bytes"}}
	b := oracle.NewBuilder(transform.Options{})
	if _, err := b.Add(testResource(), testScope(), oracle.Input{Name: "heap", Parsed: p}); err != nil {
		t.Fatal(err)
	}
	checkConformance(t, b.Request())
	rows, _ := otlpread.Rows(b.Request())
	if rows[0].Labels["k"] != "[a,b]" || rows[0].Labels["bytes"] != "512" {
		t.Fatalf("labels: %v", rows[0].Labels)
	}
}

func synthetic() *profile.Profile {
	m := &profile.Mapping{ID: 1, Start: 0x1000, Limit: 0x9000, File: "/bin/app", HasFunctions: true}
	f1 := &profile.Function{ID: 1, Name: "main.main", Filename: "main.go"}
	f2 := &profile.Function{ID: 2, Name: "main.work", Filename: "main.go"}
	l1 := &profile.Location{ID: 1, Mapping: m, Address: 0x1100, Line: []profile.Line{{Function: f1, Line: 10}}}
	l2 := &profile.Location{ID: 2, Mapping: m, Address: 0x1200, Line: []profile.Line{{Function: f2, Line: 20}}}
	return &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		PeriodType: &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:     10000000,
		Sample: []*profile.Sample{
			{Location: []*profile.Location{l2, l1}, Value: []int64{3, 30000000}},
			{Location: []*profile.Location{l1}, Value: []int64{1, 10000000}},
		},
		Mapping:  []*profile.Mapping{m},
		Location: []*profile.Location{l1, l2},
		Function: []*profile.Function{f1, f2},
	}
}

func BenchmarkTransformCPU(b *testing.B)  { benchFixture(b, "cpu") }
func BenchmarkTransformHeap(b *testing.B) { benchFixture(b, "heap") }

func benchFixture(b *testing.B, name string) {
	raw := loadFixture(b, name)
	res, sc := testResource(), testScope()
	b.ReportAllocs()
	for b.Loop() {
		bl := oracle.NewBuilder(transform.Options{})
		if _, err := bl.Add(res, sc, oracle.Input{Name: name, Pprof: raw}); err != nil {
			b.Fatal(err)
		}
		if _, err := proto.Marshal(bl.Request()); err != nil {
			b.Fatal(err)
		}
	}
}
