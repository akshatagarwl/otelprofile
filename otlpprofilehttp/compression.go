package otlpprofilehttp

import (
	"bytes"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

type compressionCodec struct {
	name     string
	encoding string
	encode   func([]byte) ([]byte, error)
}

var compressionCodecs = map[Compression]compressionCodec{
	NoCompression: {"none", "", func(body []byte) ([]byte, error) { return body, nil }},
	GzipCompression: {"gzip", "gzip", compressWith(func(w io.Writer) (io.WriteCloser, error) {
		// Preserve stateless gzip so idle exporters retain no deflate state.
		return gzip.NewWriterLevel(w, gzip.StatelessCompression)
	})},
	ZstdCompression: {"zstd", "zstd", func(body []byte) ([]byte, error) {
		encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
		if err != nil {
			return nil, err
		}
		encoded := encoder.EncodeAll(body, nil)
		if err := encoder.Close(); err != nil {
			return nil, err
		}
		return encoded, nil
	}},
	ZlibCompression: {"zlib", "zlib", compressWith(func(w io.Writer) (io.WriteCloser, error) {
		return zlib.NewWriter(w), nil
	})},
	DeflateCompression: {"deflate", "deflate", compressWith(func(w io.Writer) (io.WriteCloser, error) {
		// HTTP deflate includes a zlib wrapper (RFC 9110 section 8.4.1.2).
		return zlib.NewWriter(w), nil
	})},
	SnappyCompression: {"snappy", "snappy", func(body []byte) ([]byte, error) {
		return snappy.Encode(nil, body), nil
	}},
	SnappyFramedCompression: {"x-snappy-framed", "x-snappy-framed", compressWith(func(w io.Writer) (io.WriteCloser, error) {
		return snappy.NewBufferedWriter(w), nil
	})},
	LZ4Compression: {"lz4", "lz4", compressWith(func(w io.Writer) (io.WriteCloser, error) {
		return lz4.NewWriter(w), nil
	})},
	BrotliCompression: {"br", "br", compressWith(func(w io.Writer) (io.WriteCloser, error) {
		return brotli.NewWriter(w), nil
	})},
}

func parseCompression(name string) Compression {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "identity" {
		return NoCompression
	}
	for compression, codec := range compressionCodecs {
		if codec.name == name {
			return compression
		}
	}
	return Compression(-1)
}

func compressWith(newWriter func(io.Writer) (io.WriteCloser, error)) func([]byte) ([]byte, error) {
	return func(body []byte) ([]byte, error) {
		var buf bytes.Buffer
		buf.Grow(len(body) / 3)
		writer, err := newWriter(&buf)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(body); err != nil {
			_ = writer.Close()
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
}
