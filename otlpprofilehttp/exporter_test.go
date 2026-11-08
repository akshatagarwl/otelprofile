package otlpprofilehttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile/profile"
	"github.com/open-telemetry/sig-profiling/profcheck"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

func fixture(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "internal", "transform", "testdata", name+".pprof"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func records(t *testing.T) []profile.Record {
	now := time.Now()
	var out []profile.Record
	for _, n := range []struct {
		name string
		typ  profile.Type
	}{{"cpu", profile.TypeCPU}, {"heap", profile.TypeHeap}, {"goroutine", profile.TypeGoroutine}, {"mutex", profile.TypeMutex}, {"block", profile.TypeBlock}} {
		out = append(out, profile.Record{Type: n.typ, Start: now, Duration: 15 * time.Second, Pprof: fixture(t, n.name)})
	}
	return out
}

var res = resource.NewSchemaless(attribute.String("service.name", "svc"))
var scope = instrumentation.Scope{Name: "test"}

type captured struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  [][]byte
}

func (c *captured) handler(status func(n int) (int, http.Header)) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			body = zr
		}
		b, _ := io.ReadAll(body)
		c.mu.Lock()
		c.headers = append(c.headers, r.Header.Clone())
		c.bodies = append(c.bodies, b)
		c.mu.Unlock()
		code, h := status(int(n.Add(1)))
		for k, v := range h {
			w.Header()[k] = v
		}
		w.WriteHeader(code)
	}
}

func TestExportSendsConformingGzippedProtobuf(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(func(int) (int, http.Header) { return 200, nil }))
	defer srv.Close()
	e, err := New(context.Background(), WithEndpointURL(srv.URL), WithCompression(GzipCompression), WithHeaders(map[string]string{"Authorization": "Bearer x"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(e.URL(), "/v1development/profiles") {
		t.Fatalf("url %s", e.URL())
	}
	if err := e.Export(context.Background(), res, scope, records(t)); err != nil {
		t.Fatal(err)
	}
	if len(c.bodies) != 1 {
		t.Fatalf("want one request, got %d", len(c.bodies))
	}
	h := c.headers[0]
	if h.Get("Content-Type") != "application/x-protobuf" || h.Get("Content-Encoding") != "gzip" || h.Get("Authorization") != "Bearer x" {
		t.Fatalf("headers %v", h)
	}
	if h.Get("OTLP-Profiles-Development-Version") != "" {
		t.Fatal("revision 1 must not send the development version header")
	}
	var req collpb.ExportProfilesServiceRequest
	if err := proto.Unmarshal(c.bodies[0], &req); err != nil {
		t.Fatal(err)
	}
	data := &profilespb.ProfilesData{ResourceProfiles: req.ResourceProfiles, Dictionary: req.Dictionary}
	if err := (profcheck.ConformanceChecker{CheckDictionaryDuplicates: true, CheckDictionaryOrphans: true, CheckSampleTimestampShape: true}).Check(data); err != nil {
		t.Fatal(err)
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(func(n int) (int, http.Header) {
		if n == 1 {
			return 429, http.Header{"Retry-After": {"1"}}
		}
		if n == 2 {
			return 503, nil
		}
		return 200, nil
	}))
	defer srv.Close()
	e, _ := New(context.Background(), WithEndpointURL(srv.URL), WithRetry(RetryConfig{Enabled: true, InitialInterval: 50 * time.Millisecond, MaxInterval: time.Second, MaxElapsedTime: 10 * time.Second}))
	start := time.Now()
	if err := e.Export(context.Background(), res, scope, records(t)[:1]); err != nil {
		t.Fatal(err)
	}
	if len(c.bodies) != 3 {
		t.Fatalf("want 3 attempts, got %d", len(c.bodies))
	}
	if time.Since(start) < time.Second {
		t.Fatal("Retry-After was not honoured")
	}
}

func TestNonRetryableErrorIsReturnedOnce(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(func(int) (int, http.Header) { return 400, nil }))
	defer srv.Close()
	e, _ := New(context.Background(), WithEndpointURL(srv.URL))
	err := e.Export(context.Background(), res, scope, records(t)[:1])
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want 400 error, got %v", err)
	}
	if len(c.bodies) != 1 {
		t.Fatalf("400 must not be retried: %d attempts", len(c.bodies))
	}
}

func TestRetriesStopAtContextDeadline(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(func(int) (int, http.Header) { return 503, nil }))
	defer srv.Close()
	e, _ := New(context.Background(), WithEndpointURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := e.Export(ctx, res, scope, records(t)[:1]); err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("export outlived its context: %v", time.Since(start))
	}
}

func TestHangingServerTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	e, _ := New(context.Background(), WithEndpointURL(srv.URL), WithTimeout(200*time.Millisecond), WithRetry(RetryConfig{}))
	start := time.Now()
	if err := e.Export(context.Background(), res, scope, records(t)[:1]); err == nil {
		t.Fatal("want timeout error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestPartialSuccessIsAnError(t *testing.T) {
	resp, _ := proto.Marshal(&collpb.ExportProfilesServiceResponse{PartialSuccess: &collpb.ExportProfilesPartialSuccess{RejectedProfiles: 2, ErrorMessage: "bad"}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(resp)
	}))
	defer srv.Close()
	e, _ := New(context.Background(), WithEndpointURL(srv.URL))
	err := e.Export(context.Background(), res, scope, records(t)[:1])
	if err == nil || !strings.Contains(err.Error(), "2 profiles rejected") {
		t.Fatalf("got %v", err)
	}
}

func TestLargeExportsAreSplit(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(func(int) (int, http.Header) { return 200, nil }))
	defer srv.Close()
	e, _ := New(context.Background(), WithEndpointURL(srv.URL), WithMaxRequestBytes(4000), WithCompression(NoCompression))
	err := e.Export(context.Background(), res, scope, records(t))
	if len(c.bodies) < 2 {
		t.Fatalf("want several requests, got %d (err %v)", len(c.bodies), err)
	}
	for _, b := range c.bodies {
		if len(b) > 4000 {
			t.Fatalf("request of %d bytes exceeds the cap", len(b))
		}
	}
}

func TestEnvironment(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector:4318/base/")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer%20abc,x-team=perf")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "2500")
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
	e, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if e.URL() != "http://collector:4318/base/v1development/profiles" {
		t.Fatalf("url %s", e.URL())
	}
	if e.cfg.headers["authorization"] != "Bearer abc" || e.cfg.headers["x-team"] != "perf" {
		t.Fatalf("headers %v", e.cfg.headers)
	}
	if e.cfg.timeout != 2500*time.Millisecond || e.cfg.compression != NoCompression {
		t.Fatalf("timeout %v compression %v", e.cfg.timeout, e.cfg.compression)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT", "https://x.example/custom")
	e, _ = New(context.Background())
	if e.URL() != "https://x.example/custom" {
		t.Fatalf("signal endpoint used as-is: %s", e.URL())
	}
	// Options win over the environment.
	e, _ = New(context.Background(), WithEndpointURL("http://opt:1"))
	if e.URL() != "http://opt:1/v1development/profiles" {
		t.Fatalf("option url %s", e.URL())
	}
}

func TestEmptyRecordsSendNothing(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(func(int) (int, http.Header) { return 200, nil }))
	defer srv.Close()
	e, _ := New(context.Background(), WithEndpointURL(srv.URL))
	if err := e.Export(context.Background(), res, scope, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.bodies) != 0 {
		t.Fatal("no request expected")
	}
	if err := e.Export(context.Background(), res, scope, []profile.Record{{Type: profile.TypeCPU, Pprof: []byte("garbage")}}); err == nil {
		t.Fatal("unparseable pprof should be reported")
	}
	_ = bytes.MinRead
}
