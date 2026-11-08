// Command otlpdump profiles itself under a small workload and writes every
// OTLP request body the exporter would send to a directory, one file per
// request. An ExportProfilesServiceRequest has the same wire format as
// ProfilesData, so the files feed straight into profcheck:
//
//	otlpdump -dir out -duration 20s
//	profcheck -check-dupes -check-orphans out/request-0001.pb
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akshatagarwl/otelprofile/otlpprofilehttp"
	"github.com/akshatagarwl/otelprofile/profile"
	"github.com/akshatagarwl/otelprofile/profiletrace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type dumper struct {
	dir string
	enc *otlpprofilehttp.Exporter
	n   atomic.Int32
}

func (d *dumper) Export(_ context.Context, res *resource.Resource, scope instrumentation.Scope, recs []profile.Record) error {
	bodies, err := d.enc.Requests(res, scope, recs)
	for _, b := range bodies {
		name := filepath.Join(d.dir, fmt.Sprintf("request-%04d.pb", d.n.Add(1)))
		if werr := os.WriteFile(name, b, 0o644); werr != nil {
			return werr
		}
	}
	return err
}
func (d *dumper) ForceFlush(context.Context) error { return nil }
func (d *dumper) Shutdown(context.Context) error   { return nil }

//go:noinline
func work(d time.Duration) {
	x := 0
	for end := time.Now().Add(d); time.Now().Before(end); {
		x++
	}
	_ = x
}

func main() {
	dir := flag.String("dir", "otlp-out", "output directory")
	dur := flag.Duration("duration", 20*time.Second, "how long to profile")
	flag.Parse()
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	enc, err := otlpprofilehttp.New(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	d := &dumper{dir: *dir, enc: enc}
	res := resource.NewSchemaless(attribute.String("service.name", "otlpdump"))
	p := profile.NewProfiler(profile.WithExporter(d), profile.WithResource(res), profile.WithTypes(profile.AllTypes...), profile.WithInterval(5*time.Second))
	tr := profiletrace.NewTracerProvider(sdktrace.NewTracerProvider()).Tracer("otlpdump")
	var mu sync.Mutex
	end := time.Now().Add(*dur)
	for time.Now().Before(end) {
		ctx, span := tr.Start(context.Background(), "request")
		pprof.Do(ctx, pprof.Labels("endpoint", "/work"), func(context.Context) { work(20 * time.Millisecond) })
		go func() { mu.Lock(); time.Sleep(time.Millisecond); mu.Unlock() }()
		span.End()
	}
	if err := p.Shutdown(context.Background()); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %d requests to %s\n", d.n.Load(), *dir)
}
