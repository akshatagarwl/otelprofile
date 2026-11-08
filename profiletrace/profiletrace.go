// Package profiletrace links CPU and goroutine profile samples to spans.
//
// NewTracerProvider wraps a TracerProvider so that every sampled span (or
// only local root spans, see WithScope) sets the pprof labels otel.trace_id
// and otel.span_id on the goroutine that started it, and restores the
// previous labels when the span ends. The OTLP exporter turns those labels
// into Sample.link, which backends use to open a span's profile.
//
// pprof labels are inherited by goroutines started with the returned
// context's labels (via pprof.Do or SetGoroutineLabels), and by goroutines
// spawned after Start from the labelled goroutine.
package profiletrace

import (
	"context"
	"runtime/pprof"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// Label keys. They fit the eBPF profiler's limits (key ≤16 B, value ≤48 B).
const (
	LabelTraceID = "otel.trace_id"
	LabelSpanID  = "otel.span_id"
)

// Scope selects which spans set labels.
type Scope int

const (
	// ScopeAll labels every sampled span, so each span's own samples can be
	// opened (default).
	ScopeAll Scope = iota
	// ScopeRoot labels only local root spans (spans whose parent is absent or
	// remote); child spans' samples are attributed to the local root.
	ScopeRoot
	// ScopeNone disables labelling.
	ScopeNone
)

type config struct {
	scope     Scope
	unsampled bool
}

// Option configures the wrapper.
type Option func(*config)

// WithScope selects which spans set labels (default ScopeAll).
func WithScope(s Scope) Option { return func(c *config) { c.scope = s } }

// WithUnsampledSpans also labels spans that are not sampled. Off by default
// because an unsampled span is never stored, so the link would dangle.
func WithUnsampledSpans() Option { return func(c *config) { c.unsampled = true } }

// NewTracerProvider wraps tp.
func NewTracerProvider(tp trace.TracerProvider, opts ...Option) trace.TracerProvider {
	c := config{}
	for _, o := range opts {
		o(&c)
	}
	return &tracerProvider{tp: tp, cfg: c}
}

type tracerProvider struct {
	embedded.TracerProvider
	tp  trace.TracerProvider
	cfg config
}

func (p *tracerProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return &tracer{tr: p.tp.Tracer(name, opts...), cfg: p.cfg}
}

type tracer struct {
	embedded.Tracer
	tr  trace.Tracer
	cfg config
}

func (t *tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	parent := trace.SpanContextFromContext(ctx)
	ctx2, span := t.tr.Start(ctx, name, opts...)
	if t.cfg.scope == ScopeNone {
		return ctx2, span
	}
	sc := span.SpanContext()
	if !sc.IsValid() || (!sc.IsSampled() && !t.cfg.unsampled) {
		return ctx2, span
	}
	if t.cfg.scope == ScopeRoot && parent.IsValid() && !parent.IsRemote() {
		return ctx2, span
	}
	tid, sid := sc.TraceID(), sc.SpanID()
	ctx3 := pprof.WithLabels(ctx2, pprof.Labels(LabelTraceID, tid.String(), LabelSpanID, sid.String()))
	pprof.SetGoroutineLabels(ctx3)
	ls := &labelledSpan{Span: span, restore: ctx}
	return trace.ContextWithSpan(ctx3, ls), ls
}

// labelledSpan restores the goroutine labels that were in effect before the
// span started when it ends.
type labelledSpan struct {
	trace.Span
	restore context.Context
}

func (s *labelledSpan) End(opts ...trace.SpanEndOption) {
	s.Span.End(opts...)
	pprof.SetGoroutineLabels(s.restore)
}
