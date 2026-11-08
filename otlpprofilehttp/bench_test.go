package otlpprofilehttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkExportCycle is one collection cycle's worth of export work: the
// five fixture profiles converted, encoded, gzipped and POSTed.
func BenchmarkExportCycle(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) }))
	defer srv.Close()
	e, _ := New(context.Background(), WithEndpointURL(srv.URL))
	recs := records(&testing.T{})
	b.ReportAllocs()
	for b.Loop() {
		if err := e.Export(context.Background(), res, scope, recs); err != nil {
			b.Fatal(err)
		}
	}
}
