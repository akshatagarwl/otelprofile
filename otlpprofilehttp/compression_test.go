package otlpprofilehttp

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"github.com/andybalholm/brotli"
	"github.com/golang/snappy"
	"github.com/klauspost/compress/zstd"
	"github.com/open-telemetry/sig-profiling/profcheck"
	"github.com/pierrec/lz4/v4"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	profilespb "go.opentelemetry.io/proto/otlp/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

var compressionCases = []struct {
	name        string
	compression Compression
	encoding    string
}{
	{"none", NoCompression, ""},
	{"gzip", GzipCompression, "gzip"},
	{"zstd", ZstdCompression, "zstd"},
	{"zlib", ZlibCompression, "zlib"},
	{"deflate", DeflateCompression, "deflate"},
	{"snappy", SnappyCompression, "snappy"},
	{"x-snappy-framed", SnappyFramedCompression, "x-snappy-framed"},
	{"lz4", LZ4Compression, "lz4"},
	{"br", BrotliCompression, "br"},
}

func decodeBody(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	var reader io.Reader = bytes.NewReader(body)
	switch encoding {
	case "":
		return body
	case "gzip":
		zr, err := gzip.NewReader(reader)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = zr.Close() }()
		reader = zr
	case "zlib", "deflate":
		zr, err := zlib.NewReader(reader)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = zr.Close() }()
		reader = zr
	case "zstd":
		zr, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		reader = zr
	case "snappy":
		decoded, err := snappy.Decode(nil, body)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	case "x-snappy-framed":
		reader = snappy.NewReader(reader)
	case "lz4":
		reader = lz4.NewReader(reader)
	case "br":
		reader = brotli.NewReader(reader)
	default:
		t.Fatalf("unexpected Content-Encoding %q", encoding)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func checkProfileRequest(t *testing.T, body []byte) {
	t.Helper()
	var req collpb.ExportProfilesServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.ResourceProfiles) == 0 {
		t.Fatal("request contains no profiles")
	}
	data := &profilespb.ProfilesData{ResourceProfiles: req.ResourceProfiles, Dictionary: req.Dictionary}
	if err := (profcheck.ConformanceChecker{CheckDictionaryDuplicates: true, CheckDictionaryOrphans: true, CheckSampleTimestampShape: true}).Check(data); err != nil {
		t.Fatal(err)
	}
}

func captureWire(c *captured, firstStatus int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		c.headers = append(c.headers, r.Header.Clone())
		c.bodies = append(c.bodies, body)
		n := len(c.bodies)
		c.mu.Unlock()
		if n == 1 {
			w.WriteHeader(firstStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func TestCompressionWireFormats(t *testing.T) {
	for _, tc := range compressionCases {
		t.Run(tc.name, func(t *testing.T) {
			var c captured
			srv := httptest.NewServer(captureWire(&c, http.StatusServiceUnavailable))
			defer srv.Close()
			e, err := New(context.Background(), WithEndpointURL(srv.URL), WithCompression(tc.compression),
				WithHeaders(map[string]string{"Content-Encoding": "incorrect"}),
				WithRetry(RetryConfig{Enabled: true, InitialInterval: time.Millisecond, MaxInterval: time.Millisecond, MaxElapsedTime: time.Second}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Shutdown(context.Background()) }()
			// Requests and Export otherwise generate different profile IDs.
			e.enc = transform.NewEncoder(transform.Options{ProfileID: func() []byte { return bytes.Repeat([]byte{1}, 16) }})
			recs := records(t)
			requests, err := e.Requests(res, scope, recs)
			if err != nil || len(requests) != 1 {
				t.Fatalf("requests %d, err %v", len(requests), err)
			}
			if err := e.Export(context.Background(), res, scope, recs); err != nil {
				t.Fatal(err)
			}
			if len(c.bodies) != 2 {
				t.Fatalf("want retry, got %d requests", len(c.bodies))
			}
			if !bytes.Equal(c.bodies[0], c.bodies[1]) {
				t.Fatal("retry changed the encoded body")
			}
			for i, body := range c.bodies {
				if got := c.headers[i].Get("Content-Encoding"); got != tc.encoding {
					t.Fatalf("Content-Encoding = %q, want %q", got, tc.encoding)
				}
				if got := c.headers[i].Get("Content-Type"); got != "application/x-protobuf" {
					t.Fatalf("Content-Type = %q", got)
				}
				decoded := decodeBody(t, tc.encoding, body)
				if !bytes.Equal(decoded, requests[0]) {
					t.Fatal("decompressed request differs from uncompressed protobuf")
				}
				checkProfileRequest(t, decoded)
			}
		})
	}
}

func TestCompressionRespectsUncompressedCap(t *testing.T) {
	for _, tc := range compressionCases {
		t.Run(tc.name, func(t *testing.T) {
			var c captured
			srv := httptest.NewServer(captureWire(&c, http.StatusOK))
			defer srv.Close()
			e, err := New(context.Background(), WithEndpointURL(srv.URL), WithCompression(tc.compression), WithMaxRequestBytes(4000))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Shutdown(context.Background()) }()
			// Requests and Export otherwise generate different profile IDs.
			e.enc = transform.NewEncoder(transform.Options{ProfileID: func() []byte { return bytes.Repeat([]byte{1}, 16) }})
			recs := records(t)
			expected, expectedErr := e.Requests(res, scope, recs)
			err = e.Export(context.Background(), res, scope, recs)
			if (err == nil) != (expectedErr == nil) {
				t.Fatalf("Export error %v differs from Requests error %v", err, expectedErr)
			}
			if len(c.bodies) < 2 || len(c.bodies) != len(expected) {
				t.Fatalf("got %d requests, want %d split requests", len(c.bodies), len(expected))
			}
			for i, body := range c.bodies {
				decoded := decodeBody(t, tc.encoding, body)
				if len(decoded) > 4000 || !bytes.Equal(decoded, expected[i]) {
					t.Fatalf("request %d violates the uncompressed cap or differs from Requests", i)
				}
				checkProfileRequest(t, decoded)
			}
		})
	}
}

func TestCompressionEnvironmentAndOptions(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION", "")
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "")
	if e, err := New(context.Background()); err != nil || e.cfg.compression != NoCompression {
		t.Fatalf("default must be none: exporter %v, error %v", e, err)
	}
	for _, tc := range compressionCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", " \t"+strings.ToUpper(tc.name)+" ")
			e, err := New(context.Background())
			if err != nil || e.cfg.compression != tc.compression {
				t.Fatalf("generic env %q: exporter %v, error %v", tc.name, e, err)
			}
			t.Setenv("OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION", "gzip")
			e, err = New(context.Background())
			if err != nil || e.cfg.compression != GzipCompression {
				t.Fatalf("signal env must win: exporter %v, error %v", e, err)
			}
			e, err = New(context.Background(), WithCompression(tc.compression))
			if err != nil || e.cfg.compression != tc.compression {
				t.Fatalf("option must win: exporter %v, error %v", e, err)
			}
		})
	}
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "identity")
	if e, err := New(context.Background()); err != nil || e.cfg.compression != NoCompression {
		t.Fatalf("identity must mean none: exporter %v, error %v", e, err)
	}
}

func TestInvalidCompression(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION", "")
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "not-a-codec")
	if _, err := New(context.Background()); err == nil || !strings.Contains(err.Error(), "compression") {
		t.Fatalf("want unsupported compression error, got %v", err)
	}
	if _, err := New(context.Background(), WithCompression(GzipCompression)); err != nil {
		t.Fatalf("explicit option must override invalid env: %v", err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION", "zstd")
	if _, err := New(context.Background()); err != nil {
		t.Fatalf("signal env must override invalid generic env: %v", err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")
	t.Setenv("OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION", "not-a-codec")
	if _, err := New(context.Background()); err == nil {
		t.Fatal("invalid signal env must not fall back to generic env")
	}
	if _, err := New(context.Background(), WithCompression(NoCompression)); err != nil {
		t.Fatalf("explicit none must override invalid signal env: %v", err)
	}
	for _, invalid := range []Compression{-1, 99} {
		if _, err := New(context.Background(), WithCompression(invalid)); err == nil {
			t.Fatalf("accepted invalid compression %d", invalid)
		}
	}
}

func TestRejectedCompressionDoesNotFallBack(t *testing.T) {
	for _, tc := range compressionCases {
		t.Run(tc.name, func(t *testing.T) {
			var c captured
			srv := httptest.NewServer(captureWire(&c, http.StatusUnsupportedMediaType))
			defer srv.Close()
			e, err := New(context.Background(), WithEndpointURL(srv.URL), WithCompression(tc.compression))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Shutdown(context.Background()) }()
			err = e.Export(context.Background(), res, scope, records(t))
			if err == nil || !strings.Contains(err.Error(), "415") {
				t.Fatalf("want receiver rejection, got %v", err)
			}
			if len(c.bodies) != 1 || c.headers[0].Get("Content-Encoding") != tc.encoding {
				t.Fatal("rejected request was retried or changed its encoding")
			}
		})
	}
}

func TestCollectorCompression(t *testing.T) {
	endpoint := os.Getenv("OTELPROFILE_COMPRESSION_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set OTELPROFILE_COMPRESSION_TEST_ENDPOINT to an OTLP/HTTP profiles receiver")
	}
	for _, tc := range compressionCases {
		if tc.compression == BrotliCompression {
			continue // The Collector does not include a Brotli HTTP decoder.
		}
		t.Run(tc.name, func(t *testing.T) {
			e, err := New(context.Background(), WithEndpointURL(endpoint), WithCompression(tc.compression), WithRetry(RetryConfig{}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.Shutdown(context.Background()) }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := e.Export(ctx, res, scope, records(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
