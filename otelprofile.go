// Package otelprofile is continuous profiling for Go that pushes standard
// OTLP Profiles. One call starts it:
//
//	p, err := otelprofile.Start(otelprofile.Config{
//		ServiceName: "checkout",
//		Endpoint:    "http://localhost:4318",
//	})
//	if err != nil { ... }
//	defer p.Stop()
//
// Every field can come from the environment instead (OTEL_SERVICE_NAME,
// OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_HEADERS,
// OTEL_RESOURCE_ATTRIBUTES, OTEL_GO_X_PROFILES_TYPES, ...), so
// otelprofile.Start(otelprofile.Config{}) is enough when they are set.
//
// For more control use the profile, otlpprofilehttp and profiletrace
// packages directly.
package otelprofile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akshatagarwl/otelprofile/otlpprofilehttp"
	"github.com/akshatagarwl/otelprofile/profile"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

// ProfileType is a Go runtime profile.
type ProfileType = profile.Type

// Profile types, re-exported for convenience.
const (
	ProfileCPU       = profile.TypeCPU
	ProfileHeap      = profile.TypeHeap
	ProfileGoroutine = profile.TypeGoroutine
	ProfileMutex     = profile.TypeMutex
	ProfileBlock     = profile.TypeBlock
)

// Config configures Start. Zero values fall back to the environment, then to
// defaults. Explicit fields win over the environment.
type Config struct {
	// ServiceName is the service.name resource attribute.
	// Env: OTEL_SERVICE_NAME. Default: unknown_service:<executable>.
	ServiceName string
	// Endpoint is the OTLP/HTTP base URL (e.g. http://localhost:4318) or the
	// full profiles URL (anything ending in /v1development/profiles).
	// Env: OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT (full URL) or
	// OTEL_EXPORTER_OTLP_ENDPOINT (base URL). Default: http://localhost:4318.
	Endpoint string
	// Headers are sent with every request (e.g. Authorization).
	// Env: OTEL_EXPORTER_OTLP_[PROFILES_]HEADERS.
	Headers map[string]string
	// Tags are static key/values added to the resource, like pyroscope Tags.
	// Env: OTEL_RESOURCE_ATTRIBUTES.
	Tags map[string]string
	// ProfileTypes to collect. Env: OTEL_GO_X_PROFILES_TYPES
	// (e.g. "cpu,heap,goroutine,mutex,block" or "all"). Default: cpu, heap.
	ProfileTypes []profile.Type
	// Interval between uploads. Env: OTEL_GO_X_PROFILES_INTERVAL (Go
	// duration). Default: 15s.
	Interval time.Duration
	// CPUWindow is how long each CPU profile runs before it is exported and
	// restarted. Env: OTEL_GO_X_PROFILES_CPU_WINDOW. Default: 2 × Interval
	// (30s), the cadence pyroscope-go has in practice. See
	// profile.WithCPUWindow.
	CPUWindow time.Duration
	// MutexProfileFraction and BlockProfileRate are applied while profiling
	// when ProfileMutex / ProfileBlock are enabled, and restored on Stop.
	// Zero means use the profile package defaults.
	MutexProfileFraction int
	BlockProfileRate     int
	// DisableGCRuns stops the forced GC before a heap collection when no GC
	// ran during the window (same switch and default as pyroscope-go).
	DisableGCRuns bool
	// Logger receives errors (never fatal). Default: slog.Default().
	Logger *slog.Logger
	// Resource, if set, replaces the detected resource (ServiceName and Tags
	// are still merged in).
	Resource *resource.Resource
	// Exporter, if set, replaces the OTLP/HTTP exporter.
	Exporter profile.Exporter
}

// Profiler is a running profiler.
type Profiler struct {
	p        *profile.Profiler
	exporter profile.Exporter
	res      *resource.Resource
	types    []profile.Type
	url      string
}

// Start begins continuous profiling. It returns an error only for invalid
// configuration; collection and upload errors are logged, never returned or
// fatal. If OTEL_SDK_DISABLED=true, Start returns a profiler that does
// nothing.
func Start(cfg Config) (*Profiler, error) {
	if v, _ := strconv.ParseBool(os.Getenv("OTEL_SDK_DISABLED")); v {
		return &Profiler{}, nil
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	types := cfg.ProfileTypes
	if len(types) == 0 {
		if v := os.Getenv("OTEL_GO_X_PROFILES_TYPES"); v != "" {
			t, err := profile.ParseTypes(v)
			if err != nil {
				return nil, fmt.Errorf("otelprofile: OTEL_GO_X_PROFILES_TYPES: %w", err)
			}
			types = t
		}
	}
	if len(types) == 0 {
		types = profile.DefaultTypes
	}
	interval := cfg.Interval
	if interval == 0 {
		if v := os.Getenv("OTEL_GO_X_PROFILES_INTERVAL"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("otelprofile: OTEL_GO_X_PROFILES_INTERVAL=%q: want a positive Go duration like 15s", v)
			}
			interval = d
		}
	}

	cpuWindow := cfg.CPUWindow
	if cpuWindow == 0 {
		if v := os.Getenv("OTEL_GO_X_PROFILES_CPU_WINDOW"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("otelprofile: OTEL_GO_X_PROFILES_CPU_WINDOW=%q: want a positive Go duration", v)
			}
			cpuWindow = d
		}
	}

	res, err := buildResource(cfg)
	if err != nil {
		return nil, err
	}

	exp := cfg.Exporter
	url := ""
	if exp == nil {
		var opts []otlpprofilehttp.Option
		if cfg.Endpoint != "" {
			opts = append(opts, otlpprofilehttp.WithEndpointURL(baseOrFull(cfg.Endpoint)))
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlpprofilehttp.WithHeaders(cfg.Headers))
		}
		e, err := otlpprofilehttp.New(context.Background(), opts...)
		if err != nil {
			return nil, fmt.Errorf("otelprofile: %w", err)
		}
		exp, url = e, e.URL()
	}

	popts := []profile.Option{
		profile.WithExporter(exp),
		profile.WithResource(res),
		profile.WithTypes(types...),
		profile.WithInterval(interval),
		profile.WithCPUWindow(cpuWindow),
		profile.WithDisableGCRuns(cfg.DisableGCRuns),
		profile.WithErrorHandler(newLogHandler(logger).handle),
	}
	if cfg.MutexProfileFraction > 0 {
		popts = append(popts, profile.WithMutexProfileFraction(cfg.MutexProfileFraction))
	}
	if cfg.BlockProfileRate > 0 {
		popts = append(popts, profile.WithBlockProfileRate(cfg.BlockProfileRate))
	}
	return &Profiler{p: profile.NewProfiler(popts...), exporter: exp, res: res, types: types, url: url}, nil
}

// baseOrFull turns a base URL into the profiles URL; a URL that already has
// a path is used as-is.
func baseOrFull(u string) string {
	u = strings.TrimSpace(u)
	rest := u
	if i := strings.Index(u, "://"); i >= 0 {
		rest = u[i+3:]
	}
	if j := strings.Index(rest, "/"); j >= 0 && strings.Trim(rest[j:], "/") != "" {
		return u
	}
	return strings.TrimRight(u, "/") + otlpprofilehttp.DefaultURLPath
}

func buildResource(cfg Config) (*resource.Resource, error) {
	var attrs []attribute.KeyValue
	if cfg.ServiceName != "" {
		attrs = append(attrs, attribute.String("service.name", cfg.ServiceName))
	}
	for k, v := range cfg.Tags {
		attrs = append(attrs, attribute.String(k, v))
	}
	base := cfg.Resource
	if base == nil {
		var err error
		base, err = resource.New(context.Background(),
			resource.WithTelemetrySDK(),
			resource.WithHost(),
			resource.WithOS(),
			resource.WithProcessPID(),
			resource.WithProcessExecutableName(),
			resource.WithProcessRuntimeName(),
			resource.WithProcessRuntimeVersion(),
			resource.WithAttributes(attribute.String("service.instance.id", newInstanceID())),
			resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME
		)
		if err != nil && !errors.Is(err, resource.ErrPartialResource) {
			return nil, fmt.Errorf("otelprofile: resource: %w", err)
		}
		base, _ = resource.Merge(resource.Default(), base)
	}
	r, err := resource.Merge(base, resource.NewSchemaless(attrs...))
	if err != nil {
		return nil, fmt.Errorf("otelprofile: resource: %w", err)
	}
	return r, nil
}

func newInstanceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Stop exports the final window, restores runtime settings and stops the
// profiler. It waits at most 5 seconds, so an unreachable endpoint delays
// process exit by no more than that.
func (p *Profiler) Stop() error {
	if p == nil || p.p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.p.Shutdown(ctx)
}

// Flush exports what has been collected so far and waits for the upload.
func (p *Profiler) Flush(ctx context.Context) error {
	if p == nil || p.p == nil {
		return nil
	}
	return p.p.ForceFlush(ctx)
}

// Resource returns the resource attached to every profile.
func (p *Profiler) Resource() *resource.Resource { return p.res }

// Endpoint returns the URL profiles are sent to ("" for a custom exporter).
func (p *Profiler) Endpoint() string { return p.url }

// LabelSet is a set of dynamic labels.
type LabelSet = pprof.LabelSet

// Labels builds a LabelSet from key/value pairs, like pyroscope.Labels.
func Labels(kv ...string) LabelSet { return pprof.Labels(kv...) }

// TagWrapper runs fn with labels attached to every CPU and goroutine sample
// it (and goroutines it starts) produce, like pyroscope.TagWrapper. The
// labels become sample attributes that backends can filter on.
func TagWrapper(ctx context.Context, labels LabelSet, fn func(context.Context)) {
	pprof.Do(ctx, labels, fn)
}

// logHandler logs errors, collapsing repeats of the same message within a
// minute so a down endpoint does not flood the log.
type logHandler struct {
	l    *slog.Logger
	mu   sync.Mutex
	last string
	at   time.Time
	n    int
}

func newLogHandler(l *slog.Logger) *logHandler { return &logHandler{l: l} }

func (h *logHandler) handle(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	h.mu.Lock()
	if msg == h.last && time.Since(h.at) < time.Minute {
		h.n++
		h.mu.Unlock()
		return
	}
	repeated := h.n
	h.last, h.at, h.n = msg, time.Now(), 0
	h.mu.Unlock()
	if repeated > 0 {
		h.l.Error("otelprofile error", "err", msg, "repeated", repeated)
		return
	}
	h.l.Error("otelprofile error", "err", msg)
}
