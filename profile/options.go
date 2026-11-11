package profile

import (
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
)

// DefaultInterval is how often profiles are collected and uploaded.
const DefaultInterval = 15 * time.Second

// DefaultCPUWindowIntervals is how many intervals a CPU profile spans by
// default. Each CPU profiler restart costs the runtime about 2 MB of
// allocations, the largest single cost of continuous profiling, so with the
// defaults CPU profiles cover 30s and everything else 15s.
const DefaultCPUWindowIntervals = 2

// Default sampling rates applied when TypeMutex or TypeBlock is enabled and
// the process has not set a rate of its own.
const (
	DefaultMutexProfileFraction = 5
	DefaultBlockProfileRate     = 10_000 // nanoseconds; sample ~1 blocking event per 10µs blocked
)

type config struct {
	exporter      Exporter
	res           *resource.Resource
	interval      time.Duration
	cpuWindow     time.Duration
	types         []Type
	mutexFraction int
	blockRate     int
	disableGCRuns bool
	exportTimeout time.Duration
	errorHandler  func(error)
	queueSize     int
}

func newConfig(opts []Option) config {
	c := config{
		interval:      DefaultInterval,
		mutexFraction: -1,
		blockRate:     -1,
		exportTimeout: 30 * time.Second,
		errorHandler:  otel.Handle,
		queueSize:     4,
	}
	for _, o := range opts {
		o(&c)
	}
	if c.interval <= 0 {
		c.interval = DefaultInterval
	}
	switch {
	case c.cpuWindow == 0:
		c.cpuWindow = DefaultCPUWindowIntervals * c.interval
	case c.cpuWindow < c.interval:
		c.cpuWindow = c.interval
	}
	if len(c.types) == 0 {
		c.types = DefaultTypes
	}
	if c.res == nil {
		c.res = resource.Default()
	}
	if c.errorHandler == nil {
		c.errorHandler = otel.Handle
	}
	if c.queueSize < 1 {
		c.queueSize = 1
	}
	return c
}

// Option configures a Profiler.
type Option func(*config)

// WithExporter sets the exporter. Required; without one, records are dropped.
func WithExporter(e Exporter) Option { return func(c *config) { c.exporter = e } }

// WithResource sets the resource describing the process.
func WithResource(r *resource.Resource) Option { return func(c *config) { c.res = r } }

// WithInterval sets the collection and upload interval (default 15s).
func WithInterval(d time.Duration) Option { return func(c *config) { c.interval = d } }

// WithCPUWindow sets how long each CPU profile runs before it is stopped,
// exported and restarted (default DefaultCPUWindowIntervals × the interval;
// values below the interval are raised to it). A window of one interval
// gives finer CPU time resolution at about 2 MB of extra allocations per
// extra restart.
func WithCPUWindow(d time.Duration) Option { return func(c *config) { c.cpuWindow = d } }

// WithTypes selects the profiles to collect (default DefaultTypes).
func WithTypes(ts ...Type) Option { return func(c *config) { c.types = append([]Type(nil), ts...) } }

// WithMutexProfileFraction sets runtime.SetMutexProfileFraction while the
// Profiler runs, when TypeMutex is enabled. The previous value is restored on
// Shutdown. Without this option the Profiler uses DefaultMutexProfileFraction
// only if the process has not set a fraction itself.
func WithMutexProfileFraction(n int) Option { return func(c *config) { c.mutexFraction = n } }

// WithBlockProfileRate sets runtime.SetBlockProfileRate while the Profiler
// runs, when TypeBlock is enabled. Shutdown sets the rate back to the value
// the Profiler found (0 unless set via this package before).
func WithBlockProfileRate(n int) Option { return func(c *config) { c.blockRate = n } }

// WithDisableGCRuns stops the Profiler from calling runtime.GC() before a
// heap collection when no GC ran during the window. By default the GC runs,
// which keeps inuse_* current.
func WithDisableGCRuns(disable bool) Option { return func(c *config) { c.disableGCRuns = disable } }

// WithExportTimeout bounds one Export call (default 30s).
func WithExportTimeout(d time.Duration) Option { return func(c *config) { c.exportTimeout = d } }

// WithErrorHandler receives collection and export errors. Default: otel.Handle.
func WithErrorHandler(h func(error)) Option { return func(c *config) { c.errorHandler = h } }
