//go:build !nopyro

package main

import (
	"log"
	"log/slog"

	"github.com/akshatagarwl/otelprofile/profile"
	"github.com/grafana/pyroscope-go"
)

func init() { startPyroscope = startPyroscopeProfiler }

func startPyroscopeProfiler(svc string, ts []profile.Type) func() {
	var pts []pyroscope.ProfileType
	for _, t := range ts {
		switch t {
		case profile.TypeCPU:
			pts = append(pts, pyroscope.ProfileCPU)
		case profile.TypeHeap:
			pts = append(pts, pyroscope.ProfileAllocObjects, pyroscope.ProfileAllocSpace, pyroscope.ProfileInuseObjects, pyroscope.ProfileInuseSpace)
		case profile.TypeGoroutine:
			pts = append(pts, pyroscope.ProfileGoroutines)
		case profile.TypeMutex:
			pts = append(pts, pyroscope.ProfileMutexCount, pyroscope.ProfileMutexDuration)
		case profile.TypeBlock:
			pts = append(pts, pyroscope.ProfileBlockCount, pyroscope.ProfileBlockDuration)
		}
	}
	p, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: svc,
		ServerAddress:   *endpoint,
		Tags:            map[string]string{"deployment.environment.name": "demo", "region": "local"},
		ProfileTypes:    pts,
		UploadRate:      *interval,
	})
	if err != nil {
		log.Fatal(err)
	}
	slog.Info("pyroscope started", "service", svc, "server", *endpoint)
	return func() { _ = p.Stop() }
}
