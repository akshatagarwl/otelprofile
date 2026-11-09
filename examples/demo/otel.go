//go:build !nootel

package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/akshatagarwl/otelprofile-go"
	"github.com/akshatagarwl/otelprofile-go/otlpprofilehttp"
	"github.com/akshatagarwl/otelprofile-go/profile"
	"github.com/akshatagarwl/otelprofile-go/profiletrace"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
)

// teeExporter saves each record's raw pprof before exporting it.
type teeExporter struct {
	profile.Exporter
	dir string
	n   atomic.Int32
}

func (t *teeExporter) Export(ctx context.Context, res *resource.Resource, sc instrumentation.Scope, recs []profile.Record) error {
	for _, r := range recs {
		_ = os.WriteFile(fmt.Sprintf("%s/%04d-%s.pprof", t.dir, t.n.Add(1), r.Type), r.Pprof, 0o644)
	}
	return t.Exporter.Export(ctx, res, sc, recs)
}

func init() {
	startOtel = startOtelProfiler
	wrapTracerProvider = func(tp trace.TracerProvider) trace.TracerProvider { return profiletrace.NewTracerProvider(tp) }
}

func startOtelProfiler(svc string, ts []profile.Type) func() {
	var exp profile.Exporter
	if *dumpDir != "" {
		_ = os.MkdirAll(*dumpDir, 0o755)
		e, err := otlpprofilehttp.New(context.Background(), otlpprofilehttp.WithEndpointURL(*endpoint))
		if err != nil {
			log.Fatal(err)
		}
		exp = &teeExporter{Exporter: e, dir: *dumpDir}
	}
	p, err := otelprofile.Start(otelprofile.Config{
		Exporter:             exp,
		ServiceName:          svc,
		Endpoint:             *endpoint,
		Tags:                 map[string]string{"deployment.environment.name": "demo", "region": "local"},
		ProfileTypes:         ts,
		Interval:             *interval,
		CPUWindow:            *cpuWin,
		RuntimeDeltas:        *rtDeltas,
		MutexProfileFraction: *mutexF,
		BlockProfileRate:     *blockR,
	})
	if err != nil {
		log.Fatal(err)
	}
	slog.Info("otelprofile started", "service", svc, "endpoint", p.Endpoint(), "types", typeNames(ts))
	return func() { _ = p.Stop() }
}

func typeNames(ts []profile.Type) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.String()
	}
	return out
}
