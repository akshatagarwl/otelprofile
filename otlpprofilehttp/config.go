package otlpprofilehttp

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultURLPath is the OTLP/HTTP path for profiles while the signal is in
// development.
const DefaultURLPath = "/v1development/profiles"

// DefaultMaxRequestBytes keeps each uncompressed request under common
// receiver limits (sigiro and the collector default to 8 MiB).
const DefaultMaxRequestBytes = 8 << 20

// ProfilesDevelopmentVersion is the OTLP Profiles development revision this
// exporter writes. Revision 1 must not send the version header; revision 2+
// must send it as OTLP-Profiles-Development-Version.
const ProfilesDevelopmentVersion = 1

// Compression selects the request body encoding.
type Compression int

const (
	// GzipCompression gzips request bodies.
	GzipCompression Compression = iota
	// NoCompression sends bodies as-is (default, as in the OTLP exporter spec).
	NoCompression
)

// RetryConfig controls retries of retryable responses (429, 502, 503, 504)
// and transport errors. Retries never outlive the Export context.
type RetryConfig struct {
	Enabled         bool
	InitialInterval time.Duration
	MaxInterval     time.Duration
	MaxElapsedTime  time.Duration
}

// DefaultRetry gives up after 10 seconds so an unreachable endpoint is
// reported within one 15-second interval.
var DefaultRetry = RetryConfig{Enabled: true, InitialInterval: time.Second, MaxInterval: 5 * time.Second, MaxElapsedTime: 10 * time.Second}

type config struct {
	endpoint    string // host:port
	urlPath     string
	insecure    bool
	headers     map[string]string
	compression Compression
	tlsCfg      *tls.Config
	timeout     time.Duration
	retry       RetryConfig
	maxBytes    int
	client      *http.Client
	err         error
	endpointSet bool
	traceLabel  string
	spanLabel   string
}

// Option configures the exporter.
type Option func(*config)

// WithEndpoint sets host[:port] (no scheme or path). Default localhost:4318.
func WithEndpoint(hostport string) Option {
	return func(c *config) { c.endpoint = hostport; c.endpointSet = true }
}

// WithEndpointURL sets the full URL, e.g. http://localhost:4318/v1development/profiles.
// A URL without a path gets DefaultURLPath.
func WithEndpointURL(u string) Option {
	return func(c *config) {
		if err := c.applyURL(u, false); err != nil {
			c.err = err
		}
	}
}

// WithURLPath overrides the path (default /v1development/profiles).
func WithURLPath(p string) Option { return func(c *config) { c.urlPath = p } }

// WithInsecure uses plain HTTP.
func WithInsecure() Option { return func(c *config) { c.insecure = true } }

// WithHeaders adds request headers (e.g. Authorization).
func WithHeaders(h map[string]string) Option {
	return func(c *config) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		for k, v := range h {
			c.headers[k] = v
		}
	}
}

// WithCompression sets body compression (default none, per the OTLP spec).
func WithCompression(cmp Compression) Option { return func(c *config) { c.compression = cmp } }

// WithTLSClientConfig sets the TLS configuration.
func WithTLSClientConfig(t *tls.Config) Option { return func(c *config) { c.tlsCfg = t } }

// WithTimeout bounds each HTTP attempt (default 10s).
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithRetry configures retries (default DefaultRetry).
func WithRetry(r RetryConfig) Option { return func(c *config) { c.retry = r } }

// WithMaxRequestBytes caps one uncompressed request. Larger exports are
// split; a single profile larger than the cap is dropped with an error.
func WithMaxRequestBytes(n int) Option { return func(c *config) { c.maxBytes = n } }

// WithHTTPClient uses the given client (its Timeout is respected).
func WithHTTPClient(h *http.Client) Option { return func(c *config) { c.client = h } }

// WithTraceLabels changes the pprof label keys mapped to Sample.link
// (default otel.trace_id and otel.span_id).
func WithTraceLabels(traceID, spanID string) Option {
	return func(c *config) { c.traceLabel, c.spanLabel = traceID, spanID }
}

func (c *config) applyURL(raw string, fromEnvBase bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid OTLP endpoint URL %q", raw)
	}
	c.endpoint = u.Host
	c.endpointSet = true
	c.insecure = u.Scheme == "http"
	p := strings.TrimRight(u.Path, "/")
	switch {
	case fromEnvBase:
		c.urlPath = p + DefaultURLPath
	case p == "":
		c.urlPath = DefaultURLPath
	default:
		c.urlPath = u.Path
	}
	return nil
}

// Environment variables, following the OTLP exporter spec. The profiles
// signal has no spec'd signal-specific names yet; the _PROFILES_ forms below
// follow the pattern of the other signals.
func (c *config) fromEnv() {
	get := func(names ...string) (string, bool) {
		for _, n := range names {
			if v, ok := os.LookupEnv(n); ok && strings.TrimSpace(v) != "" {
				return v, true
			}
		}
		return "", false
	}
	if v, ok := os.LookupEnv("OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT"); ok && v != "" {
		if err := c.applyURL(v, false); err != nil {
			c.err = err
		}
	} else if v, ok := os.LookupEnv("OTEL_EXPORTER_OTLP_ENDPOINT"); ok && v != "" {
		if err := c.applyURL(v, true); err != nil {
			c.err = err
		}
	}
	if v, ok := get("OTEL_EXPORTER_OTLP_PROFILES_INSECURE", "OTEL_EXPORTER_OTLP_INSECURE"); ok {
		if b, err := strconv.ParseBool(v); err == nil && b {
			c.insecure = true
		}
	}
	if v, ok := get("OTEL_EXPORTER_OTLP_PROFILES_HEADERS", "OTEL_EXPORTER_OTLP_HEADERS"); ok {
		h, err := ParseHeaders(v)
		if err != nil {
			c.err = err
		}
		WithHeaders(h)(c)
	}
	if v, ok := get("OTEL_EXPORTER_OTLP_PROFILES_TIMEOUT", "OTEL_EXPORTER_OTLP_TIMEOUT"); ok {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			c.timeout = time.Duration(ms) * time.Millisecond
		}
	}
	if v, ok := get("OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION", "OTEL_EXPORTER_OTLP_COMPRESSION"); ok {
		switch strings.ToLower(v) {
		case "gzip":
			c.compression = GzipCompression
		case "none":
			c.compression = NoCompression
		}
	}
	if v, ok := get("OTEL_EXPORTER_OTLP_PROFILES_CERTIFICATE", "OTEL_EXPORTER_OTLP_CERTIFICATE"); ok {
		pem, err := os.ReadFile(v)
		if err != nil {
			c.err = fmt.Errorf("read OTLP certificate: %w", err)
		} else {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				c.err = fmt.Errorf("no certificates in %s", v)
			}
			c.ensureTLS().RootCAs = pool
		}
	}
	cert, okc := get("OTEL_EXPORTER_OTLP_PROFILES_CLIENT_CERTIFICATE", "OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE")
	key, okk := get("OTEL_EXPORTER_OTLP_PROFILES_CLIENT_KEY", "OTEL_EXPORTER_OTLP_CLIENT_KEY")
	if okc && okk {
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			c.err = fmt.Errorf("load OTLP client certificate: %w", err)
		} else {
			c.ensureTLS().Certificates = []tls.Certificate{pair}
		}
	}
}

func (c *config) ensureTLS() *tls.Config {
	if c.tlsCfg == nil {
		c.tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return c.tlsCfg
}

// ParseHeaders parses the OTEL_EXPORTER_OTLP_HEADERS format:
// comma-separated key=value pairs, values percent-decoded.
func ParseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return out, fmt.Errorf("invalid OTLP header %q", part)
		}
		dk, err1 := url.PathUnescape(strings.TrimSpace(k))
		dv, err2 := url.PathUnescape(strings.TrimSpace(v))
		if err1 != nil || err2 != nil {
			return out, fmt.Errorf("invalid OTLP header %q", part)
		}
		out[dk] = dv
	}
	return out, nil
}

func (c *config) url() string {
	scheme := "https"
	if c.insecure {
		scheme = "http"
	}
	return scheme + "://" + c.endpoint + c.urlPath
}
