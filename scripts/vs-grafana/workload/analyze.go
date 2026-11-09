package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/akshatagarwl/otelprofile-go/internal/otlpread"
	pprofprofile "github.com/google/pprof/profile"
	collpb "go.opentelemetry.io/proto/otlp/collector/profiles/v1development"
	"google.golang.org/protobuf/proto"
)

type qualityTotal struct {
	Rows      int              `json:"rows"`
	Total     int64            `json:"sum_values"`
	Linked    int64            `json:"linked_values"`
	Root      int64            `json:"root_values"`
	Child     int64            `json:"child_values"`
	Invalid   int64            `json:"unmatched_link_values"`
	Route     int64            `json:"route_values"`
	Leaf      int64            `json:"spinLCG_values"`
	Caller    int64            `json:"spinLCG_with_burn_values"`
	Functions map[string]int64 `json:"functions"`
}

type qualityResult struct {
	CPUSeconds float64                  `json:"process_cpu_s"`
	Requests   uint64                   `json:"requests"`
	Totals     map[string]*qualityTotal `json:"totals"`
}

func addRows(rows []otlpread.Row, spans map[string]spanRecord, totals map[string]*qualityTotal) {
	for _, row := range rows {
		if row.Value == 0 {
			continue
		}
		key := row.Type + "/" + row.Unit
		t := totals[key]
		if t == nil {
			t = &qualityTotal{Functions: map[string]int64{}}
			totals[key] = t
		}
		t.Rows++
		t.Total += row.Value
		if row.TraceID != "" {
			t.Linked += row.Value
			span, ok := spans[row.TraceID+":"+row.SpanID]
			if !ok {
				t.Invalid += row.Value
			} else if span.Name == "request" {
				t.Root += row.Value
			} else if span.Name == "compute" {
				t.Child += row.Value
			}
		}
		if row.Labels["route"] == "/work" {
			t.Route += row.Value
		}
		seen := map[string]bool{}
		for _, f := range row.Stack {
			seen[f] = true
		}
		if seen["main.spinLCG"] {
			t.Leaf += row.Value
			if seen["main.burn"] {
				t.Caller += row.Value
			}
		}
		for f := range seen {
			t.Functions[f] += row.Value
		}
	}
}

func analyze(root string) {
	prefixData, err := os.ReadFile(filepath.Join(root, "prefix"))
	if err != nil {
		log.Fatal(err)
	}
	prefix := strings.TrimSpace(string(prefixData))
	result := map[string]qualityResult{}
	spansByService := map[string]map[string]spanRecord{}
	for _, spec := range []string{"otel-default", "grafana-default", "grafana-all"} {
		name := "quality-" + spec
		data, err := os.ReadFile(filepath.Join(root, name+".json"))
		if err != nil {
			log.Fatal(err)
		}
		var run runRecord
		if err := json.Unmarshal(data, &run); err != nil {
			log.Fatal(err)
		}
		spans := map[string]spanRecord{}
		for _, span := range run.Spans {
			spans[span.Trace+":"+span.Span] = span
		}
		spansByService[prefix+"-"+spec] = spans
		q := qualityResult{CPUSeconds: run.CPU, Requests: run.Requests, Totals: map[string]*qualityTotal{}}
		files, err := filepath.Glob(filepath.Join(root, name, "*.pprof"))
		if err != nil {
			log.Fatal(err)
		}
		if len(files) == 0 {
			log.Fatal("no profiles for " + spec)
		}
		for _, file := range files {
			b, err := os.ReadFile(file)
			if err != nil {
				log.Fatal(err)
			}
			p, err := pprofprofile.ParseData(b)
			if err != nil {
				log.Fatal(err)
			}
			tid, sid := "trace_id", "span_id"
			if spec == "otel-default" {
				tid, sid = "otel.trace_id", "otel.span_id"
			}
			addRows(otlpread.PprofRows(p, tid, sid), spans, q.Totals)
		}
		result[spec+"-pprof"] = q
	}
	otlp := qualityResult{Totals: map[string]*qualityTotal{}}
	files, err := filepath.Glob(filepath.Join(root, "uploads", "*.otlp"))
	if err != nil {
		log.Fatal(err)
	}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			log.Fatal(err)
		}
		var req collpb.ExportProfilesServiceRequest
		if err := proto.Unmarshal(b, &req); err != nil {
			log.Fatal(err)
		}
		rows, err := otlpread.Rows(&req)
		if err != nil {
			log.Fatal(err)
		}
		for _, row := range rows {
			if row.Service != prefix+"-otel-default" {
				continue
			}
			addRows([]otlpread.Row{row}, spansByService[row.Service], otlp.Totals)
		}
	}
	result["otel-default-otlp"] = otlp
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(b))
}
