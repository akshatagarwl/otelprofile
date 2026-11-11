package profile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/akshatagarwl/otelprofile-go/internal/rtprof"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
)

// ScopeName is the instrumentation scope reported with every profile.
const ScopeName = "github.com/akshatagarwl/otelprofile-go/profile"

// Version is this module's version.
const Version = "0.1.0"

// Profiler collects runtime profiles periodically and exports them. It never
// blocks or panics in application goroutines: collection and export run on
// the Profiler's own goroutines, and every error goes to the error handler.
type Profiler struct {
	cfg   config
	scope instrumentation.Scope

	mu      sync.Mutex // guards collection state below
	start   time.Time
	cpuBuf  *bytes.Buffer
	cpuOn   bool
	cpuErrd bool
	heap    *rtprof.HeapProfiler
	mutex   *rtprof.MutexProfiler
	block   *rtprof.MutexProfiler
	lastGC  uint32
	enabled map[Type]bool
	buf     bytes.Buffer // reused for every non-CPU profile write
	cpuFrom time.Time    // when the current CPU window started
	cpuTick time.Time    // the collection tick that started it, for scheduling

	prevMutexFraction int
	setMutex          bool
	setBlock          bool

	queue    chan batch
	qmu      sync.RWMutex
	qclosed  bool
	stop     chan struct{}
	loopDone chan struct{}
	expDone  chan struct{}
	stopOnce sync.Once
	stopped  bool
	shutErr  error
}

type batch struct {
	recs []Record
	done chan error // nil for async batches
}

// NewProfiler starts collecting immediately, like metric.NewPeriodicReader.
func NewProfiler(opts ...Option) *Profiler {
	cfg := newConfig(opts)
	p := &Profiler{
		cfg: cfg,
		scope: instrumentation.Scope{
			Name:    ScopeName,
			Version: Version,
			Attributes: attribute.NewSet(
				attribute.String("process.runtime.name", "go"),
				attribute.String("process.runtime.version", runtime.Version()),
			),
		},
		enabled:  map[Type]bool{},
		queue:    make(chan batch, cfg.queueSize),
		stop:     make(chan struct{}),
		loopDone: make(chan struct{}),
		expDone:  make(chan struct{}),
	}
	for _, t := range cfg.types {
		p.enabled[t] = true
	}
	p.begin()
	go p.exportLoop()
	go p.collectLoop()
	return p
}

func (p *Profiler) handle(err error) {
	if err == nil {
		return
	}
	defer func() { _ = recover() }() // a panicking handler must not take the host down
	p.cfg.errorHandler(fmt.Errorf("otelprofile: %w", err))
}

// begin sets rates and primes the delta profilers so the first window
// covers only time since start.
func (p *Profiler) begin() {
	defer p.recover("start")
	p.mu.Lock()
	defer p.mu.Unlock()
	p.start = time.Now()
	var syms rtprof.Symbols
	if p.enabled[TypeHeap] || p.enabled[TypeMutex] || p.enabled[TypeBlock] {
		syms = rtprof.NewSymbols()
	}
	if p.enabled[TypeMutex] || p.enabled[TypeBlock] {
		// Read the cycle rate (once per process) before raising the
		// sampling rates: it walks the mutex profile, which is still
		// small now unless the program set a fraction itself.
		_, _ = rtprof.CyclesPerSecond()
	}
	if p.enabled[TypeMutex] {
		prev := runtime.SetMutexProfileFraction(-1)
		p.prevMutexFraction = prev
		switch {
		case p.cfg.mutexFraction >= 0:
			runtime.SetMutexProfileFraction(p.cfg.mutexFraction)
			p.setMutex = true
		case prev == 0:
			runtime.SetMutexProfileFraction(DefaultMutexProfileFraction)
			p.setMutex = true
		}
		if m, err := rtprof.NewMutexProfiler(syms); err != nil {
			p.handle(fmt.Errorf("mutex profile disabled: %w", err))
		} else {
			m.Profile() // discard the since-start profile
			p.mutex = m
		}
	}
	if p.enabled[TypeBlock] {
		rate := p.cfg.blockRate
		if rate < 0 {
			rate = DefaultBlockProfileRate
		}
		runtime.SetBlockProfileRate(rate)
		p.setBlock = true
		if b, err := rtprof.NewBlockProfiler(syms); err != nil {
			p.handle(fmt.Errorf("block profile disabled: %w", err))
		} else {
			b.Profile() // discard the since-start profile
			p.block = b
		}
	}
	if p.enabled[TypeHeap] {
		p.heap = rtprof.NewHeapProfiler(syms)
		p.heap.Profile() // discard the since-start profile
		p.lastGC = numGC()
	}
	if p.enabled[TypeCPU] {
		p.cpuBuf = &bytes.Buffer{}
		p.cpuTick = p.start
		p.startCPU()
	}
}

func (p *Profiler) startCPU() {
	p.cpuBuf.Reset()
	p.cpuFrom = time.Now()
	if err := pprof.StartCPUProfile(p.cpuBuf); err != nil {
		// Another profiler (net/http/pprof, a test, ...) holds the CPU
		// profiler. Report once, then retry quietly every interval.
		if !p.cpuErrd {
			p.handle(fmt.Errorf("cpu profile not collected, will retry each interval: %w", err))
			p.cpuErrd = true
		}
		p.cpuOn = false
		return
	}
	if p.cpuErrd {
		p.cpuErrd = false
	}
	p.cpuOn = true
}

func (p *Profiler) recover(what string) {
	if r := recover(); r != nil {
		p.handle(fmt.Errorf("panic during %s: %v\n%s", what, r, debug.Stack()))
	}
}

func (p *Profiler) collectLoop() {
	defer close(p.loopDone)
	t := time.NewTicker(p.cfg.interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			recs := p.collect(true, false)
			if len(recs) == 0 {
				continue
			}
			select {
			case p.queue <- batch{recs: recs}:
			default:
				// Exporter is behind; drop the oldest batch to bound memory.
				select {
				case old := <-p.queue:
					if old.done != nil {
						old.done <- errors.New("dropped: export queue full")
					}
				default:
				}
				p.handle(errors.New("export queue full, dropped one batch of profiles"))
				select {
				case p.queue <- batch{recs: recs}:
				default:
				}
			}
		}
	}
}

// collect ends the current window, returns its records, and (if restart)
// starts the next window. The CPU window ends only when it is due (see
// WithCPUWindow) or when force is set.
func (p *Profiler) collect(restart, force bool) (recs []Record) {
	defer p.recover("collect")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return nil
	}
	end := time.Now()
	start := p.start
	p.start = end
	rec := func(t Type, b []byte) {
		recs = append(recs, Record{Type: t, Start: start, Duration: end.Sub(start), Pprof: b})
	}

	// A window is due once it is within half an interval of its length, so
	// ticker jitter cannot push it to the next tick.
	// Scheduling uses tick times, not the moment StartCPUProfile returned
	// (StopCPUProfile can take ~100ms to drain the profile writer).
	cpuDue := force || !p.cpuOn || end.Sub(p.cpuTick) >= p.cfg.cpuWindow-p.cfg.interval/2
	if p.enabled[TypeCPU] && cpuDue {
		if p.cpuOn {
			pprof.StopCPUProfile()
			p.cpuOn = false
			if p.cpuBuf.Len() > 0 {
				recs = append(recs, Record{Type: TypeCPU, Start: p.cpuFrom, Duration: end.Sub(p.cpuFrom), Pprof: bytes.Clone(p.cpuBuf.Bytes())})
			}
		}
		if restart {
			p.cpuTick = end
			p.startCPU()
		}
	}
	buf := &p.buf
	buf.Reset()
	if p.enabled[TypeGoroutine] {
		if gp := pprof.Lookup("goroutine"); gp != nil {
			if err := gp.WriteTo(buf, 0); err != nil {
				p.handle(fmt.Errorf("goroutine profile: %w", err))
			} else {
				rec(TypeGoroutine, bytes.Clone(buf.Bytes()))
			}
		}
		buf.Reset()
	}
	if p.mutex != nil {
		rec(TypeMutex, bytes.Clone(p.mutex.Profile()))
	}
	if p.block != nil {
		rec(TypeBlock, bytes.Clone(p.block.Profile()))
	}
	if p.heap != nil {
		gc := numGC()
		if gc == p.lastGC && !p.cfg.disableGCRuns {
			runtime.GC()
			gc = numGC()
		}
		if gc != p.lastGC {
			rec(TypeHeap, bytes.Clone(p.heap.Profile()))
			p.lastGC = gc
		}
	}
	return recs
}

func (p *Profiler) exportLoop() {
	defer close(p.expDone)
	for b := range p.queue {
		err := p.export(b.recs)
		if b.done != nil {
			b.done <- err
		} else {
			p.handle(err)
		}
	}
}

func (p *Profiler) export(recs []Record) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic during export: %v\n%s", r, debug.Stack())
		}
	}()
	if p.cfg.exporter == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.exportTimeout)
	defer cancel()
	return p.cfg.exporter.Export(ctx, p.cfg.res, p.scope, recs)
}

// ForceFlush ends the current window now, exports it, and waits for the
// export (bounded by ctx).
func (p *Profiler) ForceFlush(ctx context.Context) error {
	recs := p.collect(true, true)
	if err := p.enqueueWait(ctx, recs); err != nil {
		return err
	}
	if p.cfg.exporter != nil {
		return p.cfg.exporter.ForceFlush(ctx)
	}
	return nil
}

func (p *Profiler) enqueueWait(ctx context.Context, recs []Record) error {
	if len(recs) == 0 {
		return nil
	}
	done := make(chan error, 1)
	p.qmu.RLock()
	if p.qclosed {
		p.qmu.RUnlock()
		return errors.New("profiler is shut down")
	}
	select {
	case p.queue <- batch{recs: recs, done: done}:
		p.qmu.RUnlock()
	case <-ctx.Done():
		p.qmu.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown stops collection, exports the final partial window, stops the CPU
// profiler, restores runtime rates it changed, and shuts the exporter down.
// It is safe to call more than once; later calls return the first result.
func (p *Profiler) Shutdown(ctx context.Context) error {
	p.stopOnce.Do(func() {
		close(p.stop)
		<-p.loopDone
		recs := p.collect(false, true)
		var errs []error
		errs = append(errs, p.enqueueWait(ctx, recs))
		p.mu.Lock()
		p.stopped = true
		if p.cpuOn {
			pprof.StopCPUProfile()
			p.cpuOn = false
		}
		if p.setMutex {
			runtime.SetMutexProfileFraction(p.prevMutexFraction)
		}
		if p.setBlock {
			runtime.SetBlockProfileRate(0)
		}
		p.mu.Unlock()
		p.qmu.Lock()
		p.qclosed = true
		close(p.queue)
		p.qmu.Unlock()
		select {
		case <-p.expDone:
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
		}
		if p.cfg.exporter != nil {
			errs = append(errs, p.cfg.exporter.Shutdown(ctx))
		}
		p.shutErr = errors.Join(errs...)
	})
	return p.shutErr
}

var gcSample = []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
var gcMu sync.Mutex

// numGC reads the completed GC cycle count without stopping the world.
func numGC() uint32 {
	gcMu.Lock()
	defer gcMu.Unlock()
	metrics.Read(gcSample)
	if gcSample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return uint32(gcSample[0].Value.Uint64())
}
