package transform_test

import (
	"bytes"
	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"github.com/akshatagarwl/otelprofile-go/internal/transform/oracle"
	"testing"
	"time"

	"github.com/google/pprof/profile"
)

// FuzzTransform feeds arbitrary bytes. Whatever google/pprof accepts must
// convert without panicking, and the output must pass profcheck.
func FuzzTransform(f *testing.F) {
	for _, name := range fixtureNames {
		f.Add(loadFixture(f, name))
	}
	var buf bytes.Buffer
	_ = synthetic().Write(&buf)
	f.Add(buf.Bytes())
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := profile.ParseData(data)
		if err != nil {
			return
		}
		b := oracle.NewBuilder(transform.Options{})
		if _, err := b.Add(testResource(), testScope(), oracle.Input{Name: "fuzz", Start: time.Unix(1, 0), Parsed: p}); err != nil {
			return
		}
		if b.Samples() == 0 {
			return
		}
		checkConformance(t, b.Request())
	})
}
