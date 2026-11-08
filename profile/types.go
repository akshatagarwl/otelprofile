// Package profile collects Go runtime profiles on a fixed interval and hands
// them to an Exporter. It is shaped like a future go.opentelemetry.io/otel/sdk/profile.
//
// Defaults touch no process-wide runtime setting except the CPU profiler,
// which matches pyroscope-go's defaults (cpu + heap). Mutex and block
// profiling are opt-in because they change process-wide sampling rates;
// Shutdown restores any rate the Profiler changed.
package profile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Type is a Go runtime profile.
type Type uint8

const (
	// TypeCPU is the CPU profile (samples/count and cpu/nanoseconds), collected
	// as consecutive windows with runtime/pprof.StartCPUProfile.
	TypeCPU Type = iota + 1
	// TypeHeap is the heap profile: alloc_objects and alloc_space as deltas
	// over the window, inuse_objects and inuse_space as of the last GC.
	TypeHeap
	// TypeGoroutine is a snapshot of all goroutine stacks.
	TypeGoroutine
	// TypeMutex is the mutex contention profile (contentions/count,
	// delay/nanoseconds) as deltas. Opt-in.
	TypeMutex
	// TypeBlock is the blocking profile (contentions/count, delay/nanoseconds)
	// as deltas. Opt-in.
	TypeBlock
)

// DefaultTypes are collected when no types are configured. They match
// pyroscope-go's defaults.
var DefaultTypes = []Type{TypeCPU, TypeHeap}

// AllTypes lists every supported type.
var AllTypes = []Type{TypeCPU, TypeHeap, TypeGoroutine, TypeMutex, TypeBlock}

// String returns the runtime profile name.
func (t Type) String() string {
	switch t {
	case TypeCPU:
		return "cpu"
	case TypeHeap:
		return "heap"
	case TypeGoroutine:
		return "goroutine"
	case TypeMutex:
		return "mutex"
	case TypeBlock:
		return "block"
	}
	return fmt.Sprintf("Type(%d)", uint8(t))
}

// ParseTypes parses a comma-separated list such as "cpu,heap,mutex". It
// accepts the runtime names above and pyroscope-go's names (alloc_objects,
// inuse_space, goroutines, mutex_count, block_duration, ...). "all" selects
// every type.
func ParseTypes(s string) ([]Type, error) {
	var out []Type
	seen := map[Type]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		var ts []Type
		switch f {
		case "all":
			ts = AllTypes
		case "cpu", "process_cpu":
			ts = []Type{TypeCPU}
		case "heap", "memory", "alloc_objects", "alloc_space", "inuse_objects", "inuse_space", "allocs":
			ts = []Type{TypeHeap}
		case "goroutine", "goroutines":
			ts = []Type{TypeGoroutine}
		case "mutex", "mutex_count", "mutex_duration":
			ts = []Type{TypeMutex}
		case "block", "block_count", "block_duration":
			ts = []Type{TypeBlock}
		default:
			return nil, fmt.Errorf("unknown profile type %q", f)
		}
		for _, t := range ts {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// Record is one collected runtime profile, pprof-encoded (optionally
// gzipped) as runtime/pprof or godeltaprof writes it. Cumulative types are
// already deltas over [Start, Start+Duration).
type Record struct {
	Type     Type
	Start    time.Time
	Duration time.Duration
	Pprof    []byte
}

// Exporter sends records somewhere. Export is called from the Profiler's own
// goroutine, never from application code, and must honour ctx.
type Exporter interface {
	Export(ctx context.Context, res *resource.Resource, scope instrumentation.Scope, recs []Record) error
	ForceFlush(ctx context.Context) error
	Shutdown(ctx context.Context) error
}
