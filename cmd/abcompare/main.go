// Command abcompare compares two services' profiles stored in sigiro, one
// sent by pyroscope-go through sigiro's Pyroscope bridge and one by
// otelprofile over OTLP, over the same time range. It prints per-type totals
// and the top-N stacks by share, and exits non-zero if any row is outside the
// tolerance.
//
//	abcompare -sigiro http://localhost:19999 -a demo-pyro -b demo-otel -from <unix> -to <unix>
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

var (
	sigiro   = flag.String("sigiro", "http://localhost:19999", "sigiro HTTP API")
	svcA     = flag.String("a", "demo-pyro", "baseline service (pyroscope-go)")
	svcB     = flag.String("b", "demo-otel", "candidate service (otelprofile)")
	from     = flag.Int64("from", 0, "range start, unix seconds")
	to       = flag.Int64("to", 0, "range end, unix seconds")
	topN     = flag.Int("top", 20, "stacks compared per type")
	totTol   = flag.Float64("total-tol", 0.10, "allowed relative difference of per-type totals")
	shareTol = flag.Float64("share-tol", 0.03, "allowed absolute difference of a top stack's share (0.03 = 3 points)")
	types    = flag.String("types", "", "comma list of profile types (default: all present in both)")
	snapshot = map[string]bool{"inuse_objects": true, "inuse_space": true, "goroutines": true}
)

func query(sql string) ([]map[string]any, error) {
	resp, err := http.Post(*sigiro+"/v1/query", "text/plain", bytes.NewBufferString(sql))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", resp.Status, b)
	}
	var rows []map[string]any
	return rows, json.Unmarshal(b, &rows)
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		_, _ = fmt.Sscan(x, &f)
		return f
	}
	return 0
}

func where(svc, typ string) string {
	return fmt.Sprintf("service_name = '%s' AND profile_type = '%s' AND timestamp >= make_timestamp(%d) AND timestamp < make_timestamp(%d)",
		svc, typ, *from*1_000_000, *to*1_000_000)
}

type side struct {
	stackSum float64 // all stacks, including any left out of the comparison
	total    float64
	snaps    float64
	excluded float64
	stacks   map[string]float64
}

var excludeWrite = flag.Bool("exclude-http-write-syscall", false, "darwin rule: leave the HTTP server's response-write syscall path out of the CPU comparison")

// httpResponseWrite reports a CPU stack in which the HTTP server writes a
// response and the time lands in the write syscall. darwin attributes a
// varying share of kernel time to it (it moves several points between turns
// of the same process), so the darwin CPU A/B leaves it out on both sides.
func httpResponseWrite(st string) bool {
	return strings.Contains(st, "net/http.(*conn).serve") && strings.Contains(st, "syscall.write")
}

var exclude = flag.Bool("exclude-profiler-goroutines", true, "drop the profilers' own and in-flight upload goroutines from the goroutine comparison")

// profilerGoroutine reports a goroutine that belongs to a profiler or to an
// upload in flight. With both profilers in one process each snapshot sees the
// other's (pyroscope-go keeps five upload workers; an upload has transport
// and dial goroutines), so they are left out of the comparison on both sides.
func profilerGoroutine(st string) bool {
	for _, f := range []string{"github.com/grafana/pyroscope-go", "github.com/akshatagarwl/otelprofile-go",
		"net/http.(*persistConn).roundTrip", "net/http.(*Transport).getConn", "net.(*sysDialer)", "net/http.(*Transport).dialConn"} {
		if strings.Contains(st, f) {
			return true
		}
	}
	return false
}

func load(svc, typ string) (side, error) {
	s := side{stacks: map[string]float64{}}
	rows, err := query("SELECT sum(value) AS t, count(DISTINCT timestamp) AS n FROM sigiro_profiles WHERE " + where(svc, typ))
	if err != nil {
		return s, err
	}
	s.total, s.snaps = num(rows[0]["t"]), num(rows[0]["n"])
	rows, err = query("SELECT array_to_string(stack_trace, ';') AS st, sum(value) AS v FROM sigiro_profiles WHERE " + where(svc, typ) + " GROUP BY 1")
	if err != nil {
		return s, err
	}
	for _, r := range rows {
		st := fmt.Sprint(r["st"])
		s.stackSum += num(r["v"])
		if (typ == "cpu" || typ == "samples") && *excludeWrite && httpResponseWrite(st) {
			// Out of the top-20 comparison only: totals stay complete and the
			// other paths keep their share of the full profile.
			continue
		}
		if typ == "goroutines" && *exclude && profilerGoroutine(st) {
			s.excluded += num(r["v"])
			continue
		}
		s.stacks[st] += num(r["v"])
	}
	if s.excluded > 0 {
		s.total -= s.excluded
	}
	if snapshot[typ] && s.snaps > 0 {
		s.total /= s.snaps
		for k := range s.stacks {
			s.stacks[k] /= s.snaps
		}
	}
	return s, nil
}

// normalize drops frames that differ only because of which profiler is
// running (the profilers' own goroutines and SDK wrappers), so stacks of the
// same application code compare equal.
func normalize(st string) string {
	frames := strings.Split(st, ";")
	out := frames[:0]
	for _, f := range frames {
		if strings.HasPrefix(f, "github.com/akshatagarwl/otelprofile-go") || strings.HasPrefix(f, "github.com/grafana/pyroscope-go") {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, ";")
}

func main() {
	flag.Parse()
	if *to == 0 {
		*to = time.Now().Unix()
	}
	if *from == 0 {
		*from = *to - 600
	}
	var ts []string
	if *types != "" {
		ts = strings.Split(*types, ",")
	} else {
		rows, err := query(fmt.Sprintf("SELECT profile_type AS t FROM sigiro_profiles WHERE service_name IN ('%s','%s') AND timestamp >= make_timestamp(%d) AND timestamp < make_timestamp(%d) GROUP BY 1 HAVING count(DISTINCT service_name) = 2 ORDER BY 1", *svcA, *svcB, *from*1_000_000, *to*1_000_000))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		for _, r := range rows {
			ts = append(ts, fmt.Sprint(r["t"]))
		}
	}
	fmt.Printf("range %s .. %s (%s), A=%s (pyroscope-go via bridge), B=%s (otelprofile OTLP)\n", time.Unix(*from, 0).Format(time.RFC3339), time.Unix(*to, 0).Format(time.RFC3339), time.Duration(*to-*from)*time.Second, *svcA, *svcB)
	fmt.Printf("tolerance: totals ±%.0f%%, top-%d stack shares ±%.1f points\n", *totTol*100, *topN, *shareTol*100)
	if *exclude {
		fmt.Printf("goroutines: the profilers' own and in-flight upload goroutines are left out on both sides\n")
	}
	if *excludeWrite {
		fmt.Printf("cpu: totals are complete; the HTTP response write-syscall path is left out of the top-20 share comparison on both sides (darwin rule)\n")
	}
	fmt.Println()
	fail := false
	fmt.Printf("%-16s %16s %16s %8s %10s %8s  %s\n", "type", "A total", "B total", "diff", "top-N max", "overlap", "result")
	for _, typ := range ts {
		a, err := load(*svcA, typ)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		b, err := load(*svcB, typ)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		na, nb := map[string]float64{}, map[string]float64{}
		for k, v := range a.stacks {
			na[normalize(k)] += v
		}
		for k, v := range b.stacks {
			nb[normalize(k)] += v
		}
		sa, sb := sum(na), sum(nb)
		if *excludeWrite && (typ == "cpu" || typ == "samples") {
			sa, sb = a.stackSum, b.stackSum
			if snapshot[typ] && a.snaps > 0 {
				sa /= a.snaps
			}
			if snapshot[typ] && b.snaps > 0 {
				sb /= b.snaps
			}
		}
		// Top-N stacks of the union by the larger share.
		keys := map[string]bool{}
		for _, m := range []map[string]float64{na, nb} {
			ks := sortedKeys(m)
			for i := 0; i < len(ks) && i < *topN; i++ {
				keys[ks[i]] = true
			}
		}
		type d struct {
			k      string
			pa, pb float64
		}
		var diffs []d
		maxd := 0.0
		for k := range keys {
			pa, pb := share(na[k], sa), share(nb[k], sb)
			diffs = append(diffs, d{k, pa, pb})
			maxd = math.Max(maxd, math.Abs(pa-pb))
		}
		ta, tb := topSet(na, *topN), topSet(nb, *topN)
		overlap := 0
		for k := range ta {
			if tb[k] {
				overlap++
			}
		}
		rel := 0.0
		if a.total > 0 {
			rel = (b.total - a.total) / a.total
		}
		ok := math.Abs(rel) <= *totTol && maxd <= *shareTol
		if !ok {
			fail = true
		}
		res := "PASS"
		if !ok {
			res = "FAIL"
		}
		fmt.Printf("%-16s %16.0f %16.0f %+7.1f%% %9.2fpt %5d/%-2d  %s\n", typ, a.total, b.total, rel*100, maxd*100, overlap, min(len(ta), *topN), res)
		if os.Getenv("AB_VERBOSE") != "" {
			sort.Slice(diffs, func(i, j int) bool { return math.Max(diffs[i].pa, diffs[i].pb) > math.Max(diffs[j].pa, diffs[j].pb) })
			for _, x := range diffs[:min(len(diffs), 8)] {
				fmt.Printf("    %6.2f%% %6.2f%%  %s\n", x.pa*100, x.pb*100, tail(x.k))
			}
		}
	}
	if fail {
		os.Exit(1)
	}
}

func sum(m map[string]float64) float64 {
	t := 0.0
	for _, v := range m {
		t += v
	}
	return t
}
func share(v, t float64) float64 {
	if t == 0 {
		return 0
	}
	return v / t
}
func sortedKeys(m map[string]float64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return m[ks[i]] > m[ks[j]] })
	return ks
}
func topSet(m map[string]float64, n int) map[string]bool {
	out := map[string]bool{}
	for i, k := range sortedKeys(m) {
		if i >= n {
			break
		}
		out[k] = true
	}
	return out
}
func tail(st string) string {
	f := strings.Split(st, ";")
	if len(f) > 3 {
		f = f[len(f)-3:]
	}
	return strings.Join(f, ";")
}
