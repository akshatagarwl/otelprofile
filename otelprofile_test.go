package otelprofile_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile-go"
	"github.com/akshatagarwl/otelprofile-go/internal/otlpread"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

type sink struct {
	mu   sync.Mutex
	reqs []*collpb.ExportProfilesServiceRequest
}

func (s *sink) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1development/profiles" {
			http.NotFound(w, r)
			return
		}
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			body = zr
		}
		b, _ := io.ReadAll(body)
		var req collpb.ExportProfilesServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, &req)
		s.mu.Unlock()
	}))
}

func (s *sink) rows(t *testing.T) []otlpread.Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []otlpread.Row
	for _, r := range s.reqs {
		rows, err := otlpread.Rows(r)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, rows...)
	}
	return all
}

func TestEnvOnlyConfiguration(t *testing.T) {
	var s sink
	srv := s.server(t)
	defer srv.Close()
	t.Setenv("OTEL_SERVICE_NAME", "env-svc")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "team=perf")
	t.Setenv("OTEL_GO_X_PROFILES_TYPES", "goroutine,heap")
	t.Setenv("OTEL_GO_X_PROFILES_INTERVAL", "100ms")
	p, err := otelprofile.Start(otelprofile.Config{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	rows := s.rows(t)
	if len(rows) == 0 {
		t.Fatal("nothing received")
	}
	types := map[string]bool{}
	for _, r := range rows {
		if r.Service != "env-svc" {
			t.Fatalf("service %q", r.Service)
		}
		types[r.Name] = true
	}
	if !types["goroutine"] || !types["heap"] || types["cpu"] {
		t.Fatalf("types from env not honoured: %v", types)
	}
	attrs := map[string]string{}
	for _, kv := range s.reqs[0].ResourceProfiles[0].Resource.Attributes {
		attrs[kv.Key] = kv.Value.GetStringValue()
	}
	if attrs["team"] != "perf" || attrs["service.instance.id"] == "" || attrs["host.name"] == "" {
		t.Fatalf("resource %v", attrs)
	}
}

func TestTagWrapperLabelsReachSamples(t *testing.T) {
	var s sink
	srv := s.server(t)
	defer srv.Close()
	p, err := otelprofile.Start(otelprofile.Config{
		ServiceName:  "tags",
		Endpoint:     srv.URL,
		Tags:         map[string]string{"region": "eu"},
		ProfileTypes: []otelprofile.ProfileType{otelprofile.ProfileCPU},
		Interval:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	otelprofile.TagWrapper(context.Background(), otelprofile.Labels("endpoint", "/slow"), func(context.Context) {
		x := 0
		for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); {
			x++
		}
		_ = x
	})
	_ = p.Stop()
	found := false
	for _, r := range s.rows(t) {
		if r.Labels["endpoint"] == "/slow" {
			found = true
		}
	}
	if !found {
		t.Fatal("no sample carried the endpoint label")
	}
	if p.Resource() == nil {
		t.Fatal("resource")
	}
}

func TestUnreachableEndpointIsLoggedNotFatal(t *testing.T) {
	var logs bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &mu}, nil))
	p, err := otelprofile.Start(otelprofile.Config{
		ServiceName:  "down",
		Endpoint:     "http://127.0.0.1:1", // nothing listens
		ProfileTypes: []otelprofile.ProfileType{otelprofile.ProfileGoroutine},
		Interval:     50 * time.Millisecond,
		Logger:       logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Retries back off for a while before an export gives up, so the first
	// error can take a few seconds to appear; the app is never blocked.
	logged := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(logs.String(), "otelprofile error")
	}
	for deadline := time.Now().Add(40 * time.Second); !logged() && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}
	if !logged() {
		t.Fatal("error not logged")
	}
	start := time.Now()
	_ = p.Stop()
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("Stop took %v", d)
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

func TestDisabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	p, err := otelprofile.Start(otelprofile.Config{Endpoint: "http://127.0.0.1:1"})
	if err != nil || p.Stop() != nil {
		t.Fatal(err)
	}
}

func TestBadEnvIsAnError(t *testing.T) {
	t.Setenv("OTEL_GO_X_PROFILES_TYPES", "cpu,bogus")
	if _, err := otelprofile.Start(otelprofile.Config{}); err == nil {
		t.Fatal("want error")
	}
}
