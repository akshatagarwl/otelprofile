package transform_test

import (
	"github.com/akshatagarwl/otelprofile-go/internal/transform"
	"runtime"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// TestEncoderRetainedMemoryIsBounded encodes every fixture many times with
// distinct timestamps and checks how much heap the Encoder keeps.
func TestEncoderRetainedMemoryIsBounded(t *testing.T) {
	res, _ := proto.Marshal(testResource())
	var ins []transform.Input
	for _, name := range fixtureNames {
		ins = append(ins, transform.Input{Name: name, Pprof: loadFixture(t, name)})
	}
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	before := heap()
	e := transform.NewEncoder(transform.Options{})
	for i := 0; i < 200; i++ {
		for j := range ins {
			ins[j].Start = time.Unix(int64(i), 0)
		}
		if _, _, err := e.Encode(res, transform.EncScope{Name: "x"}, ins); err != nil {
			t.Fatal(err)
		}
	}
	kept := int64(heap()) - int64(before)
	t.Logf("encoder retains %d KB", kept/1024)
	if kept > 1<<20 {
		t.Fatalf("encoder retains %d KB", kept/1024)
	}
	runtime.KeepAlive(e)
}
