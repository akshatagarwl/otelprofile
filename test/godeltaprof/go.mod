module github.com/akshatagarwl/otelprofile-go/test/godeltaprof

go 1.26.0

replace github.com/akshatagarwl/otelprofile-go => ../..

require (
	github.com/akshatagarwl/otelprofile-go v0.0.0-00010101000000-000000000000
	github.com/google/pprof v0.0.0-20261105003125-4bf881d7fb0b
	github.com/grafana/pyroscope-go/godeltaprof v0.1.12
	go.opentelemetry.io/otel/sdk v1.47.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
