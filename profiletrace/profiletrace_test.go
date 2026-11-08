package profiletrace

import (
	"context"
	"runtime/pprof"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// goroutineLabels reads the calling goroutine's labels through a context
// that carries them (pprof exposes them only via ForLabels on a context).
func labelsOf(ctx context.Context) map[string]string {
	out := map[string]string{}
	pprof.ForLabels(ctx, func(k, v string) bool { out[k] = v; return true })
	return out
}

func TestSpanSetsAndRestoresLabels(t *testing.T) {
	tp := NewTracerProvider(sdktrace.NewTracerProvider())
	tr := tp.Tracer("t")
	base := pprof.WithLabels(context.Background(), pprof.Labels("keep", "me"))
	pprof.SetGoroutineLabels(base)

	ctx, root := tr.Start(base, "root")
	l := labelsOf(ctx)
	sc := root.SpanContext()
	if l[LabelTraceID] != sc.TraceID().String() || l[LabelSpanID] != sc.SpanID().String() || l["keep"] != "me" {
		t.Fatalf("root labels %v", l)
	}
	cctx, child := tr.Start(ctx, "child")
	if labelsOf(cctx)[LabelSpanID] != child.SpanContext().SpanID().String() {
		t.Fatal("child span id not labelled under ScopeAll")
	}
	if trace.SpanFromContext(cctx) != child {
		t.Fatal("returned context must carry the returned span")
	}
	child.End()
	root.End()
	if len(labelsOf(base)) != 1 {
		t.Fatal("base context mutated")
	}
}

func TestScopeRootOnlyLabelsLocalRoots(t *testing.T) {
	tr := NewTracerProvider(sdktrace.NewTracerProvider(), WithScope(ScopeRoot)).Tracer("t")
	ctx, root := tr.Start(context.Background(), "root")
	cctx, child := tr.Start(ctx, "child")
	if labelsOf(cctx)[LabelSpanID] != root.SpanContext().SpanID().String() {
		t.Fatal("child should inherit the root's span id")
	}
	child.End()
	root.End()
}

func TestUnsampledSpansAreNotLabelled(t *testing.T) {
	tr := NewTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))).Tracer("t")
	ctx, span := tr.Start(context.Background(), "x")
	defer span.End()
	if _, ok := labelsOf(ctx)[LabelTraceID]; ok {
		t.Fatal("unsampled span labelled")
	}
}

func BenchmarkSpanStartEnd(b *testing.B) {
	sdk := sdktrace.NewTracerProvider()
	for _, c := range []struct {
		name string
		tp   trace.TracerProvider
	}{{"plain", sdk}, {"labelled", NewTracerProvider(sdk)}} {
		b.Run(c.name, func(b *testing.B) {
			tr := c.tp.Tracer("b")
			b.ReportAllocs()
			for b.Loop() {
				_, s := tr.Start(context.Background(), "op")
				s.End()
			}
		})
	}
}
