package transform

import (
	"bytes"
	"io"

	"github.com/klauspost/compress/gzip"
)

// gunzipper decompresses into a buffer it keeps, with a reader it keeps.
// Unlike sync.Pool (emptied by every GC, which in a busy process happens
// several times per collection interval) this makes repeat decompression
// allocation-free.
type gunzipper struct {
	zr  *gzip.Reader
	buf bytes.Buffer
	src bytes.Reader
}

func (g *gunzipper) gunzip(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	g.src.Reset(data)
	if g.zr == nil {
		zr, err := gzip.NewReader(&g.src)
		if err != nil {
			return nil, err
		}
		g.zr = zr
	} else if err := g.zr.Reset(&g.src); err != nil {
		return nil, err
	}
	g.buf.Reset()
	if _, err := io.Copy(&g.buf, g.zr); err != nil {
		return nil, err
	}
	if g.buf.Cap() > 16<<20 {
		// Do not keep a huge buffer around after one huge profile.
		out := g.buf.Bytes()
		g.buf = bytes.Buffer{}
		return out, nil
	}
	return g.buf.Bytes(), nil
}
