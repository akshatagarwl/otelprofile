// Package otlpprofilehttp exports profiles as OTLP/HTTP protobuf to
// /v1development/profiles. It converts the pprof records produced by the
// profile package into OTLP Profiles with a shared dictionary, maps the
// otel.trace_id/otel.span_id pprof labels to Sample.link, and retries
// retryable failures within the Export context.
package otlpprofilehttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/akshatagarwl/otelprofile/internal/transform"
	"github.com/akshatagarwl/otelprofile/profile"
	"github.com/klauspost/compress/gzip"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	"google.golang.org/protobuf/encoding/protowire"
)

// Exporter implements profile.Exporter over OTLP/HTTP.
type Exporter struct {
	cfg    config
	url    string
	client *http.Client

	mu       sync.Mutex
	shutdown bool

	encMu    sync.Mutex // guards enc and the caches
	enc      *transform.Encoder
	resKey   *resource.Resource
	resBytes []byte
	scopeKey instrumentation.Scope
	scope    transform.EncScope
}

var _ profile.Exporter = (*Exporter)(nil)

// New creates an exporter. Options override environment variables
// (OTEL_EXPORTER_OTLP_[PROFILES_]ENDPOINT, _HEADERS, _TIMEOUT, _COMPRESSION,
// _CERTIFICATE, _CLIENT_CERTIFICATE, _CLIENT_KEY, _INSECURE).
func New(_ context.Context, opts ...Option) (*Exporter, error) {
	c := config{
		// The OTLP exporter spec's default compression is none. gzip is one
		// option or OTEL_EXPORTER_OTLP_COMPRESSION=gzip away.
		compression: NoCompression,
		endpoint:    "localhost:4318",
		urlPath:     DefaultURLPath,
		insecure:    true, // spec default endpoint is http://localhost:4318
		timeout:     10 * time.Second,
		retry:       DefaultRetry,
		maxBytes:    DefaultMaxRequestBytes,
	}
	c.fromEnv()
	for _, o := range opts {
		o(&c)
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.maxBytes <= 0 {
		c.maxBytes = DefaultMaxRequestBytes
	}
	client := c.client
	if client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if c.tlsCfg != nil {
			tr.TLSClientConfig = c.tlsCfg.Clone()
		}
		client = &http.Client{Transport: tr}
	}
	return &Exporter{
		cfg: c, url: c.url(), client: client,
		enc: transform.NewEncoder(transform.Options{TraceIDLabel: c.traceLabel, SpanIDLabel: c.spanLabel}),
	}, nil
}

// URL is the endpoint requests are sent to.
func (e *Exporter) URL() string { return e.url }

// ErrShutdown is returned by Export after Shutdown.
var ErrShutdown = errors.New("otlpprofilehttp: exporter is shut down")

// Export converts recs and sends them in as few requests as the size cap
// allows. Records with no samples are skipped.
func (e *Exporter) Export(ctx context.Context, res *resource.Resource, scope instrumentation.Scope, recs []profile.Record) error {
	e.mu.Lock()
	down := e.shutdown
	e.mu.Unlock()
	if down {
		return ErrShutdown
	}
	e.encMu.Lock()
	defer e.encMu.Unlock()
	var errs []error
	err := e.encode(res, scope, recs, func(body []byte) {
		if err := e.send(ctx, body); err != nil {
			errs = append(errs, err)
		}
	})
	if err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Requests converts recs into encoded ExportProfilesServiceRequest bodies,
// each at most the configured size. It is exported for tests and tools.
func (e *Exporter) Requests(res *resource.Resource, scope instrumentation.Scope, recs []profile.Record) ([][]byte, error) {
	e.encMu.Lock()
	defer e.encMu.Unlock()
	var out [][]byte
	err := e.encode(res, scope, recs, func(body []byte) { out = append(out, bytes.Clone(body)) })
	return out, err
}

// encode converts recs with the streaming encoder and calls emit with each
// request body (valid only during the call), splitting oversized batches.
func (e *Exporter) encode(res *resource.Resource, scope instrumentation.Scope, recs []profile.Record, emit func([]byte)) error {
	if e.resKey != res || e.resBytes == nil {
		e.resKey, e.resBytes = res, transform.EncodeResource(res)
	}
	if e.scope.Name == "" || e.scopeKey.Name != scope.Name || e.scopeKey.Version != scope.Version || !e.scopeKey.Attributes.Equals(&scope.Attributes) {
		e.scopeKey = scope
		e.scope = transform.EncScope{Name: scope.Name, Version: scope.Version, Attributes: transform.AppendKeyValues(nil, 3, scope.Attributes.ToSlice())}
	}
	ins := make([]transform.Input, len(recs))
	for i, r := range recs {
		ins[i] = transform.Input{Name: r.Type.String(), Start: r.Start, Duration: r.Duration, Pprof: r.Pprof}
	}
	var errs []error
	var build func(ins []transform.Input)
	build = func(ins []transform.Input) {
		body, n, err := e.enc.Encode(e.resBytes, e.scope, ins)
		if err != nil {
			errs = append(errs, err)
		}
		if n == 0 {
			return
		}
		if len(body) <= e.cfg.maxBytes {
			emit(body)
			return
		}
		if len(ins) == 1 {
			errs = append(errs, fmt.Errorf("dropped %s profile: %d bytes exceeds the %d-byte request cap", ins[0].Name, len(body), e.cfg.maxBytes))
			return
		}
		build(ins[:len(ins)/2])
		build(ins[len(ins)/2:])
	}
	build(ins)
	return errors.Join(errs...)
}

// encode gzips body. Stateless compression keeps no deflate state between
// requests (a stateful writer retains about a megabyte), at a small cost in
// ratio; profiles compress well either way.
func (e *Exporter) compress(body []byte) ([]byte, error) {
	if e.cfg.compression != GzipCompression {
		return body, nil
	}
	var buf bytes.Buffer
	buf.Grow(len(body) / 3)
	zw, err := gzip.NewWriterLevel(&buf, gzip.StatelessCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type retryableError struct {
	err   error
	after time.Duration
}

func (r *retryableError) Error() string { return r.err.Error() }

func (e *Exporter) send(ctx context.Context, raw []byte) error {
	body, err := e.compress(raw)
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}
	start := time.Now()
	interval := e.cfg.retry.InitialInterval
	for attempt := 0; ; attempt++ {
		err := e.attempt(ctx, body)
		var re *retryableError
		if err == nil || !errors.As(err, &re) || !e.cfg.retry.Enabled {
			if re != nil {
				return re.err
			}
			return err
		}
		wait := re.after
		if wait <= 0 {
			// Full jitter around the current interval.
			wait = interval/2 + time.Duration(rand.Int64N(int64(interval)+1))/2
			interval = min(interval*2, e.cfg.retry.MaxInterval)
		}
		if e.cfg.retry.MaxElapsedTime > 0 && time.Since(start)+wait > e.cfg.retry.MaxElapsedTime {
			return fmt.Errorf("giving up after %d attempts: %w", attempt+1, re.err)
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait {
			return fmt.Errorf("giving up after %d attempts (deadline): %w", attempt+1, re.err)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w (last error: %v)", ctx.Err(), re.err)
		case <-t.C:
		}
	}
}

func (e *Exporter) attempt(ctx context.Context, body []byte) error {
	actx := ctx
	if e.cfg.timeout > 0 {
		var cancel context.CancelFunc
		actx, cancel = context.WithTimeout(ctx, e.cfg.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(actx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("User-Agent", "OTel-Go-Profile-Exporter/"+profile.Version)
	if e.cfg.compression == GzipCompression {
		req.Header.Set("Content-Encoding", "gzip")
	}
	if ProfilesDevelopmentVersion >= 2 {
		req.Header.Set("OTLP-Profiles-Development-Version", strconv.Itoa(ProfilesDevelopmentVersion))
	}
	for k, v := range e.cfg.headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return &retryableError{err: fmt.Errorf("POST %s: %w", e.url, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return partialSuccess(respBody)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusBadGateway,
		resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode == http.StatusGatewayTimeout:
		return &retryableError{err: statusError(resp, respBody), after: retryAfter(resp.Header.Get("Retry-After"))}
	default:
		return statusError(resp, respBody)
	}
}

// partialSuccess reads ExportProfilesServiceResponse{partial_success=1:
// {rejected_profiles=1, error_message=2}} from a 2xx body. Bodies that are
// not protobuf (some receivers answer with text) count as success.
func partialSuccess(body []byte) error {
	var rejected int64
	var msg string
	for b := body; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil
		}
		b = b[n:]
		if num != 1 || typ != protowire.BytesType {
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return nil
			}
			b = b[m:]
			continue
		}
		ps, m := protowire.ConsumeBytes(b)
		if m < 0 {
			return nil
		}
		b = b[m:]
		for len(ps) > 0 {
			pn, pt, k := protowire.ConsumeTag(ps)
			if k < 0 {
				return nil
			}
			ps = ps[k:]
			switch {
			case pn == 1 && pt == protowire.VarintType:
				v, k := protowire.ConsumeVarint(ps)
				if k < 0 {
					return nil
				}
				rejected, ps = int64(v), ps[k:]
			case pn == 2 && pt == protowire.BytesType:
				v, k := protowire.ConsumeBytes(ps)
				if k < 0 {
					return nil
				}
				msg, ps = string(v), ps[k:]
			default:
				k := protowire.ConsumeFieldValue(pn, pt, ps)
				if k < 0 {
					return nil
				}
				ps = ps[k:]
			}
		}
	}
	if rejected > 0 || msg != "" {
		return fmt.Errorf("partial success: %d profiles rejected: %s", rejected, msg)
	}
	return nil
}

func statusError(resp *http.Response, body []byte) error {
	msg := string(bytes.TrimSpace(body))
	if len(msg) > 512 {
		msg = msg[:512] + "…"
	}
	return fmt.Errorf("POST %s: %s: %s", resp.Request.URL, resp.Status, msg)
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// ForceFlush is a no-op: Export is synchronous.
func (e *Exporter) ForceFlush(context.Context) error { return nil }

// Shutdown makes later Export calls fail and closes idle connections.
func (e *Exporter) Shutdown(context.Context) error {
	e.mu.Lock()
	e.shutdown = true
	e.mu.Unlock()
	e.client.CloseIdleConnections()
	return nil
}
