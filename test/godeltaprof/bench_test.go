package godeltaprof_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"go.opentelemetry.io/otel/sdk/resource"
)

// BenchmarkCycle measures one collection cycle per path (godeltaprof or
// rtprof) and per profile kind, as otelprofile runs it:
//
//   - kind heap, mutex or block: writing that delta profile;
//   - kind all+encode: writing all three and encoding them into one OTLP
//     request with the exporter's encoder.
//
// Two workloads, both after recording the full set of stacks (512 shallow
// and 32 deep per kind) once:
//
//   - churn: every stack changes every cycle;
//   - steady: 16 stacks change per cycle, closer to a service in steady
//     state, where most of the cost is reading the records.
//
// Between iterations, with the timer stopped, the workload runs and a GC
// publishes the heap records.
func BenchmarkCycle(b *testing.B) {
	defer runtime.SetMutexProfileFraction(runtime.SetMutexProfileFraction(1))
	runtime.SetBlockProfileRate(1)
	defer runtime.SetBlockProfileRate(0)
	allocWork(1)
	contendWork(1 << treeDepth)
	res := transform.EncodeResource(resource.Default())
	scope := transform.EncScope{Name: "bench"}

	for _, load := range []struct {
		name string
		work func()
	}{
		{"churn", func() { allocWork(1); contendWork(1 << treeDepth) }},
		{"steady", func() { allocPaths(16); contendWork(16) }},
	} {
		for _, side := range []struct {
			name string
			new  func(testing.TB) paths
		}{
			{"godeltaprof", func(testing.TB) paths { return godeltaprofPaths() }},
			{"rtprof", func(t testing.TB) paths { p, _, _, _ := rtprofPaths(t); return p }},
		} {
			for _, kind := range []string{"heap", "mutex", "block", "all+encode"} {
				b.Run(load.name+"/"+side.name+"/"+kind, func(b *testing.B) {
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
						load.work()
						runtime.GC()
						b.StartTimer()
						switch kind {
						case "heap":
							p.heap()
						case "mutex":
							p.mutex()
						case "block":
							p.block()
						default:
							ins[0] = transform.Input{Name: "heap", Start: now, Duration: time.Second, Pprof: p.heap()}
							ins[1] = transform.Input{Name: "mutex", Start: now, Duration: time.Second, Pprof: p.mutex()}
							ins[2] = transform.Input{Name: "block", Start: now, Duration: time.Second, Pprof: p.block()}
							if _, _, err := enc.Encode(res, scope, ins); err != nil {
								b.Fatal(err)
							}
						}
					}
				})
			}
		}
	}
}
