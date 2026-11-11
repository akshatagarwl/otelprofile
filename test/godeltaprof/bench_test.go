package godeltaprof_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"go.opentelemetry.io/otel/sdk/resource"
)

// BenchmarkCycle measures one collection cycle of heap, mutex and block
// profiles as otelprofile runs it, per path: "profile" is writing the three
// delta profiles; "pipeline" adds converting them to one OTLP request with
// the exporter's encoder. Between iterations (timer stopped) the workload
// adds new values to every stack and a GC publishes the heap records, so
// each cycle writes a full set of samples.
func BenchmarkCycle(b *testing.B) {
	defer runtime.SetMutexProfileFraction(runtime.SetMutexProfileFraction(1))
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)
	res := transform.EncodeResource(resource.Default())
	scope := transform.EncScope{Name: "bench"}

	for _, side := range []struct {
		name string
		new  func(testing.TB) paths
	}{
		{"godeltaprof", func(testing.TB) paths { return godeltaprofPaths() }},
		{"rtprof", func(t testing.TB) paths { p, _, _, _ := rtprofPaths(t); return p }},
	} {
		for _, pipeline := range []bool{false, true} {
			name := side.name + "/profile"
			if pipeline {
				name = side.name + "/pipeline"
			}
			b.Run(name, func(b *testing.B) {
				p := side.new(b)
				enc := transform.NewEncoder(transform.Options{})
				p.heap()
				p.mutex()
				p.block()
				ins := make([]transform.Input, 3)
				now := time.Now()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					allocWork(1)
					contendWork(16)
					runtime.GC()
					b.StartTimer()
					ins[0] = transform.Input{Name: "heap", Start: now, Duration: time.Second, Pprof: p.heap()}
					ins[1] = transform.Input{Name: "mutex", Start: now, Duration: time.Second, Pprof: p.mutex()}
					ins[2] = transform.Input{Name: "block", Start: now, Duration: time.Second, Pprof: p.block()}
					if pipeline {
						if _, _, err := enc.Encode(res, scope, ins); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
