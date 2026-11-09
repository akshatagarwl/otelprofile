package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	otelprofile "github.com/akshatagarwl/otelprofile-go"
	"github.com/akshatagarwl/otelprofile-go/otlpprofilehttp"
	"github.com/akshatagarwl/otelprofile-go/profile"
	"github.com/akshatagarwl/otelprofile-go/profiletrace"
	otelpyroscope "github.com/grafana/otel-profiling-go"
	"github.com/grafana/pyroscope-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type spanRecord struct {
	Trace      string            `json:"trace"`
	Span       string            `json:"span"`
	Parent     string            `json:"parent"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes"`
}

type spanWriter struct {
	mu    sync.Mutex
	spans []spanRecord
}

func (s *spanWriter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, span := range spans {
		r := spanRecord{Trace: span.SpanContext().TraceID().String(), Span: span.SpanContext().SpanID().String(), Parent: span.Parent().SpanID().String(), Name: span.Name(), Attributes: map[string]string{}}
		for _, a := range span.Attributes() {
			r.Attributes[string(a.Key)] = a.Value.Emit()
		}
		s.spans = append(s.spans, r)
	}
	return nil
}
func (*spanWriter) Shutdown(context.Context) error { return nil }

type savedExporter struct {
	profile.Exporter
	dir string
	n   atomic.Uint32
}

func (e *savedExporter) Export(ctx context.Context, r *resource.Resource, s instrumentation.Scope, records []profile.Record) error {
	for _, p := range records {
		if err := os.WriteFile(filepath.Join(e.dir, fmt.Sprintf("%04d-%s.pprof", e.n.Add(1), p.Type)), p.Pprof, 0600); err != nil {
			return err
		}
	}
	return e.Exporter.Export(ctx, r, s, records)
}

type capturedClient struct {
	dir string
	n   atomic.Uint32
}

func (c *capturedClient) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	copyReq := req.Clone(req.Context())
	copyReq.Body = io.NopCloser(bytes.NewReader(body))
	if err := copyReq.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}
	if copyReq.MultipartForm != nil {
		defer copyReq.MultipartForm.RemoveAll()
	}
	f, _, err := copyReq.FormFile("profile")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(c.dir, fmt.Sprintf("%04d-grafana.pprof", c.n.Add(1))), p, 0600); err != nil {
		return nil, err
	}
	return (&http.Client{Timeout: 30 * time.Second}).Do(req)
}

type runRecord struct {
	Mode       string       `json:"mode"`
	Scope      string       `json:"scope"`
	Types      string       `json:"types"`
	Duration   float64      `json:"duration_s"`
	CPU        float64      `json:"cpu_s"`
	PeakRSS    int64        `json:"peak_rss_bytes"`
	Allocated  uint64       `json:"allocated_bytes"`
	Requests   uint64       `json:"requests"`
	Iterations uint64       `json:"iterations"`
	Checksum   uint64       `json:"checksum"`
	Spans      []spanRecord `json:"spans,omitempty"`
}

var sink uint64

//go:noinline
func burn(n int) uint64 { return spinLCG(n) }

//go:noinline
func spinLCG(n int) uint64 {
	x := uint64(7)
	for i := 0; i < n; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	return x
}

//go:noinline
func allocate() [][]byte {
	out := make([][]byte, 32)
	for i := range out {
		out[i] = make([]byte, 32768)
		for k := 0; k < len(out[i]); k += 4096 {
			out[i][k] = byte(i + 1)
		}
	}
	return out
}

func usage() (float64, int64, uint64) {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		log.Fatal(err)
	}
	rss := r.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	m := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(m)
	return float64(r.Utime.Nano()+r.Stime.Nano()) / 1e9, rss, m[0].Value.Uint64()
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "analyze" {
		analyze(os.Args[2])
		return
	}
	mode := flag.String("mode", "otel", "none, otel, grafana")
	scope := flag.String("scope", "default", "default, all, root")
	types := flag.String("types", "default", "default, all")
	endpoint := flag.String("endpoint", "http://127.0.0.1:14040", "profile receiver")
	service := flag.String("service", "compare", "service name")
	duration := flag.Duration("duration", 60*time.Second, "workload duration")
	interval := flag.Duration("interval", 15*time.Second, "upload interval")
	matched := flag.Bool("matched", false, "match CPU window to upload interval")
	capture := flag.String("capture", "", "directory for raw profiles and spans")
	out := flag.String("out", "run.json", "run statistics")
	flag.Parse()
	if *capture != "" {
		if err := os.MkdirAll(*capture, 0700); err != nil {
			log.Fatal(err)
		}
	}
	writer := &spanWriter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(writer))
	defer tp.Shutdown(context.Background())
	var provider trace.TracerProvider = tp
	stop := func() error { return nil }
	switch *mode {
	case "none":
	case "otel":
		cfg := otelprofile.Config{ServiceName: *service, Endpoint: *endpoint, Interval: *interval}
		if *types == "all" {
			cfg.ProfileTypes = profile.AllTypes
		}
		if *matched {
			cfg.CPUWindow = *interval
		}
		if *capture != "" {
			exp, err := otlpprofilehttp.New(context.Background(), otlpprofilehttp.WithEndpointURL(*endpoint))
			if err != nil {
				log.Fatal(err)
			}
			cfg.Exporter = &savedExporter{Exporter: exp, dir: *capture}
		}
		p, err := otelprofile.Start(cfg)
		if err != nil {
			log.Fatal(err)
		}
		stop = p.Stop
		var opts []profiletrace.Option
		if *scope == "root" {
			opts = append(opts, profiletrace.WithScope(profiletrace.ScopeRoot))
		}
		provider = profiletrace.NewTracerProvider(tp, opts...)
	case "grafana":
		cfg := pyroscope.Config{ApplicationName: *service, ServerAddress: *endpoint, UploadRate: *interval, Logger: pyroscope.StandardLogger}
		if *types == "all" {
			cfg.ProfileTypes = append(append([]pyroscope.ProfileType{}, pyroscope.DefaultProfileTypes...), pyroscope.ProfileGoroutines, pyroscope.ProfileMutexCount, pyroscope.ProfileMutexDuration, pyroscope.ProfileBlockCount, pyroscope.ProfileBlockDuration)
			runtime.SetMutexProfileFraction(5)
			runtime.SetBlockProfileRate(10000)
		}
		if *capture != "" {
			cfg.HTTPClient = &capturedClient{dir: *capture}
		}
		p, err := pyroscope.Start(cfg)
		if err != nil {
			log.Fatal(err)
		}
		stop = p.Stop
		var opts []otelpyroscope.Option
		if *scope == "all" {
			opts = append(opts, otelpyroscope.WithSpanIDLabelScope(otelpyroscope.ScopeAllSpans), otelpyroscope.WithSpanNameLabelScope(otelpyroscope.ScopeAllSpans))
		}
		provider = otelpyroscope.NewTracerProvider(tp, opts...)
	default:
		log.Fatal("unknown mode")
	}
	var background sync.WaitGroup
	done := make(chan struct{})
	if *types == "all" {
		var mu sync.Mutex
		for range 2 {
			background.Go(func() {
				for {
					select {
					case <-done:
						return
					default:
					}
					mu.Lock()
					time.Sleep(2 * time.Millisecond)
					mu.Unlock()
				}
			})
		}
		background.Go(func() { <-done })
	}
	tracer := provider.Tracer("comparison")
	cpu0, _, alloc0 := usage()
	start := time.Now()
	var requests, iterations uint64
	var retained [16][][]byte
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for range int(*duration / (100 * time.Millisecond)) {
		<-ticker.C
		pprof.Do(context.Background(), pprof.Labels("route", "/work"), func(ctx context.Context) {
			ctx, root := tracer.Start(ctx, "request")
			sink ^= burn(1500000)
			_, child := tracer.Start(ctx, "compute")
			sink ^= burn(18000000)
			retained[requests%16] = allocate()
			child.End()
			sink ^= burn(500000)
			root.SetAttributes(attribute.Int64("request.index", int64(requests)))
			root.End()
		})
		requests++
		iterations += 20000000
	}
	close(done)
	background.Wait()
	if err := stop(); err != nil {
		log.Fatal(err)
	}
	cpu1, rss, alloc1 := usage()
	r := runRecord{Mode: *mode, Scope: *scope, Types: *types, Duration: time.Since(start).Seconds(), CPU: cpu1 - cpu0, PeakRSS: rss, Allocated: alloc1 - alloc0, Requests: requests, Iterations: iterations, Checksum: sink}
	if *capture != "" {
		r.Spans = writer.spans
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, b, 0600); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s requests=%d cpu=%.3fs rss=%.1fMiB elapsed=%.3fs\n", *mode, requests, r.CPU, float64(rss)/(1<<20), r.Duration)
	runtime.KeepAlive(retained)
}
