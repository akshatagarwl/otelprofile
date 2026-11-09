# Compression

The exporter sends OTLP Profiles protobuf over HTTP. Compression changes
`Content-Encoding`, not the protobuf format or the profiles endpoint.

## Encodings

| Environment value | `otlpprofilehttp` constant | HTTP body | Collector 0.165.0 |
|---|---|---|---|
| `none` | `NoCompression` | Uncompressed, no `Content-Encoding` header | Verified |
| `gzip` | `GzipCompression` | Gzip | Verified |
| `zstd` | `ZstdCompression` | Zstandard frame | Verified |
| `zlib` | `ZlibCompression` | Zlib-wrapped DEFLATE | Verified |
| `deflate` | `DeflateCompression` | Zlib-wrapped DEFLATE | Verified |
| `snappy` | `SnappyCompression` | Snappy block | Verified |
| `x-snappy-framed` | `SnappyFramedCompression` | Snappy framed stream | Verified |
| `lz4` | `LZ4Compression` | LZ4 frame | Verified |
| `br` | `BrotliCompression` | Brotli | Not supported by its HTTP decoder |

The HTTP header matches the environment value except for `none`, which omits
it. `identity` is an alias for `none`. Values ignore case and surrounding
whitespace. The default remains `none`.

`deflate` includes a zlib wrapper as required by
[RFC 9110 section 8.4.1.2](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.4.1.2).
It does not send raw DEFLATE. Snappy block and framed Snappy are distinct
formats with distinct header values.

Only no compression and gzip are baseline OTLP choices. The other values
are extensions and require a matching receiver. The Collector's
[HTTP decoders](https://github.com/open-telemetry/opentelemetry-collector/blob/main/config/confighttp/compression.go)
cover every entry above except Brotli. Older receiver versions can differ.
A receiver rejection is returned as an export error, with no silent fallback
to a different encoding. Compression ratios and CPU cost have not been
benchmarked here.

## Configuration

For `otelprofile.Start`, the environment selects compression:

```sh
export OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION=zstd
```

`OTEL_EXPORTER_OTLP_PROFILES_COMPRESSION` overrides
`OTEL_EXPORTER_OTLP_COMPRESSION`. An explicit exporter option overrides both:

```go
exporter, err := otlpprofilehttp.New(ctx,
	otlpprofilehttp.WithCompression(otlpprofilehttp.ZstdCompression),
)
```

Unknown environment values and unknown `Compression` constants fail `New`.
A valid explicit option overrides an invalid environment value. Custom
headers cannot override the exporter's `Content-Encoding`.

The request-size cap applies to uncompressed protobuf. Each request is
compressed once, before the retry loop. Retries reuse the encoded bytes.

## Verification

`go test ./otlpprofilehttp -run Compression` checks HTTP headers, independent
decoding, byte-for-byte protobuf equality, `profcheck`, environment
precedence, request splitting, and retries for all nine choices.

A local Collector 0.165.0 with its `file` exporter persisted eight batches
of eleven profiles, one batch per supported encoding. The opt-in test uses
an existing local receiver:

```sh
OTELPROFILE_COMPRESSION_TEST_ENDPOINT=http://localhost:4318 \
	go test ./otlpprofilehttp -run TestCollectorCompression -v -count=1
```

Brotli has HTTP wire and conformance coverage in the unit tests, but no
end-to-end backend result in this change.

## Protocol limits

There is no gRPC or OTLP JSON exporter in this library. Apache Arrow OTAP
is a different protocol, not a compression setting. Its inspected
[service definitions](https://github.com/open-telemetry/otel-arrow/blob/main/proto/opentelemetry/proto/experimental/arrow/v1/arrow_service.proto)
contain traces, logs, and metrics, but no Profiles service or Profiles
payload type. This exporter does not send Arrow.
