// Package transform converts pprof-encoded runtime profiles into OTLP
// Profiles (opentelemetry-proto profiles/v1development, proto v1.10+ layout)
// with a streaming Encoder that reads the pprof wire format and writes OTLP
// protobuf directly. It imports neither google/pprof nor the generated OTLP
// packages, so neither is linked into the profiled program. Every table in
// the shared ProfilesDictionary starts with its zero value, entries are
// de-duplicated by value, and an entry is added only when a kept sample
// references it, so the output passes profcheck with the duplicate and
// orphan checks enabled. The oracle subpackage is an independent converter
// the Encoder is tested against.
package transform

import "time"

// Attribute keys written by the transform.
const (
	// AttrProfileName names the Go runtime profile a Profile came from
	// (cpu, heap, goroutine, mutex, block). pprof itself has no such field,
	// and mutex and block profiles share sample types, so without it a
	// consumer cannot tell them apart.
	AttrProfileName = "go.pprof.name"

	attrComment         = "pprof.profile.comment"
	attrDocURL          = "pprof.profile.doc_url"
	attrDropFrames      = "pprof.profile.drop_frames"
	attrKeepFrames      = "pprof.profile.keep_frames"
	attrDefaultType     = "pprof.scope.default_sample_type"
	attrSampleTypeOrder = "pprof.scope.sample_type_order"
	attrIsFolded        = "pprof.location.is_folded"
	attrHasFunctions    = "pprof.mapping.has_functions"
	attrHasFilenames    = "pprof.mapping.has_filenames"
	attrHasLineNumbers  = "pprof.mapping.has_line_numbers"
	attrHasInline       = "pprof.mapping.has_inline_frames"
	attrBuildID         = "process.executable.build_id.gnu"
)

// Default pprof label keys that carry trace context. They become
// Sample.link_index instead of sample attributes.
const (
	LabelTraceID = "otel.trace_id"
	LabelSpanID  = "otel.span_id"
)

// Input is one pprof-encoded profile.
type Input struct {
	// Name is the runtime profile name (cpu, heap, goroutine, mutex, block).
	Name string
	// Start and Duration describe the collection window. When zero, the
	// pprof TimeNanos/DurationNanos are used.
	Start    time.Time
	Duration time.Duration
	// Pprof is the encoded profile, optionally gzipped.
	Pprof []byte
	// Cumulative marks a profile whose counting sample types (alloc_objects,
	// alloc_space, contentions, delay) are totals since the process started,
	// as runtime/pprof writes them. The Encoder subtracts the previous
	// cumulative profile with the same Name, per stack and label set, so the
	// output holds deltas for the window. Snapshot types (inuse_*) pass
	// through. See Encoder.CommitDeltas.
	Cumulative bool
	// Baseline marks the first cumulative profile of a Name: it only seeds
	// the delta state and produces no samples.
	Baseline bool
}

// Options tune the transform.
type Options struct {
	// TraceIDLabel and SpanIDLabel name the pprof labels that are mapped to
	// Sample.link_index. Defaults: LabelTraceID and LabelSpanID.
	TraceIDLabel, SpanIDLabel string
	// KeepZeroSamples keeps samples whose value is zero for a sample type.
	KeepZeroSamples bool
	// ProfileID generates Profile.profile_id. Default: 16 random bytes.
	ProfileID func() []byte
}
