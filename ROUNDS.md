# Rounds

Each round: run the full scorecard, fix the worst failing rows, re-run,
record what changed. Evidence files are in `evidence/` with the round prefix.

## Round 1 — 2026-11-07 23:20 → 2026-11-08 00:46 IST (≈1 h 25 min, including the initial build)

Built: transform, profiler, OTLP/HTTP exporter, trace labels, one-call
`Start`, demo app; sigiro proto fix, labels column, Pyroscope bridge naming,
Profiles page, trace→profile links, local dashboard serving.

First full scorecard run. Failures found:

| Row | Failure | Fix (in this round) |
|---|---|---|
| 8 | `profcheck -check-dupes` failed on real requests: an empty stack was added as a duplicate of the zero `Stack{}` (also `Link{}` must be the literal zero, not 16/8 zero bytes). | Pre-map every table's zero value; empty link entry. Unit test added. |
| 8 | sigiro rejected/misread current OTLP profiles: its vendored proto was the v1.8 layout (`Sample.values` was field 2, now 4). | sigiro: vendor the v1.10 layout (the one its gRPC path already used). |
| 10 | Overhead soak (32 min, cpu+heap, 15 s): allocations +357 MB vs no profiler (pyroscope-go +146 MB); peak RSS 42.4 MB vs 40.0 MB. CPU was fine (210.5 s vs pyroscope 219.5 s). | Streaming encoder (pprof wire → OTLP wire, reused tables): one export cycle 600 KB/4,000 allocs → 22 KB/190. Default compression none (OTLP default; the stateful gzip writer retained ~0.9 MB). Re-measured in round 2. |
| 7 | Same-process A/B: goroutines top-stack share differed by 3.6 points. | Not an accuracy bug: the two snapshots are taken 3 ms apart and each sees the other profiler's in-flight upload goroutines (`net/http.(*persistConn).roundTrip` 0 % vs 2.5 %). Added a ground-truth test (100 parked goroutines and `runtime.NumGoroutine`); goroutine tolerance set to ±5 points with this rationale. |
| 2 | On Linux, an unreachable endpoint was only reported after the 30 s retry budget. | Retries give up after 10 s; `Stop` waits at most 5 s. |
| 6 | Top-functions table clipped its Total column; diff legend said green where d3-flame-graph draws blue. | Fixed table layout and wording. |
| 6 | sigiro's `/v1/query` refused `json_keys`, so labels could not be listed. | sigiro: allow `json_keys`/`json_type`. |

Rows passing at the end of round 1: 1, 2, 3, 4, 5, 6, 8, 9. Row 7 passes
with the documented goroutine tolerance; row 10 fails until the round 2
soak confirms the encoder.

## Round 2 — 2026-11-08 00:46 → 01:10 IST (24 min)

Full re-run with the streaming encoder: `scripts/scorecard.sh r2` (all
automated checks PASS after one test fix), A/B (`evidence/r2-ab-*.txt`, all
PASS, CPU −3.1 %), soak 2 (32 min) and an amplified run (1 s interval, 8 min).

| Measure (vs no profiler) | otelprofile | pyroscope-go |
|---|---|---|
| Soak 2 CPU s (none 213.2) | 211.3 | 216.3 |
| Soak 2 allocations (none 52,815 MB) | +242 MB | +131 MB |
| Soak 2 peak RSS (none 35.4 MB) | 46.8 MB | 39.7 MB |
| Amplified CPU s (none 53.6) | 55.3 | 55.7 |
| Amplified allocations | +1,018 MB | +585 MB |
| Amplified, otelprofile with 2-interval CPU windows | +544 MB | — |

Findings, from sigiro's own alloc/inuse profiles of the demo processes:

- Most remaining allocation is the runtime's `pprof.StartCPUProfile`
  (~2 MB per restart). pyroscope-go restarts its CPU profile only every
  other tick (its ticker resets to the elapsed time, so every second tick is
  skipped: 65 CPU windows in 32 min against 128 for us), so at the same
  configured interval it does half the CPU-profiler work.
- RSS got worse than round 1: the encoder's intern table kept growing up to
  128K entries (maps were 1.7 MB of live heap).
- sync.Pool for the gzip reader did nothing: the pool is emptied by every GC
  and this process GCs several times a second.

Fixes: the encoder owns its gzip reader and buffer; the profiler reuses one
profile buffer; the intern table is cleared past 4,096 entries (an encoder
now keeps ~120 KB, `TestEncoderRetainedMemoryIsBounded`); CPU profiles span
two intervals by default (`CPUWindow`, 30 s at the default 15 s), which is
pyroscope-go's real cadence; CPU windows are scheduled on tick times. A
ground-truth CPU test was rewritten to compare with process CPU, since it
failed under load (wall-clock spin).

Rows flipped: none to PASS yet; row 10 still FAIL (allocations, RSS) until
round 3's soak.

## Round 3 — 2026-11-08 01:10 → 01:42 IST (32 min)

Ran A/B (`evidence/r3-ab-*.txt`: all PASS; CPU totals −9 % between the two
processes, but each profile recovers the same share of its own process's
rusage CPU: 0.906 otelprofile vs 0.901 pyroscope-go,
`evidence/r3-ab-cpu-vs-rusage.txt`), soak 3 and an amplified run with the
round-2 fixes.

| Measure (vs no profiler) | otelprofile | pyroscope-go |
|---|---|---|
| Soak 3 CPU s (none 211.0) | 207.1 | 218.2 |
| Soak 3 allocations (none 52,902 MB) | +151 MB | +147 MB |
| Soak 3 peak RSS / Go runtime memory (none 35.0 / 26.8 MB) | 42.7 / 34.8 MB | 41.0 / 35.0 MB |
| Amplified (1 s) CPU s (none 52.7) | 50.8 | 52.0 |
| Amplified allocations | +541 MB | +594 MB |
| Amplified peak RSS | 45.8 MB | 43.7 MB |

Allocations are now level, CPU lower; peak RSS still 1.7–2.1 MB higher.
sigiro's inuse profiles of both processes show the same retained profiler
memory, so the gap is not live heap held by the encoder. Two more causes
found and fixed:

- The production path still used the protobuf runtime (`proto.Marshal` of
  the resource, `proto.Unmarshal` of every response) and linked
  google/pprof (whose package init alone maps ~4 MB). The encoder, resource
  and scope encoding and partial-success parsing now use only protowire;
  the google/pprof converter moved to `internal/transform/oracle` (test
  reference only). Importing otelprofile now adds 0 MB of mapped memory at
  init; importing pyroscope-go adds 4.6 MB.
- Every soak process ran the same demo binary with both profilers linked.
  The demo now has build tags so each variant links only its own profiler,
  as a real program would.

Also: sigiro `mise run quick` (rustfmt + check) found formatting, fixed;
the cloud e2e OTLP fixture now writes `Sample.values` as field 4.

Rows flipped: row 10 not yet (RSS); confirmation in round 4.

## Round 4 — 2026-11-08 01:42 → 02:30 IST (48 min; the laptop lid was closed 01:59–02:16, which paused every process alike)

Full run of the round-3 code: `scripts/scorecard.sh r4` (all PASS), A/B
(`scripts/ab-report.sh`: all PASS; CPU recovered 0.878 vs 0.871 of rusage),
amplified run and soak 4 with build-tag variants (each links only its own
profiler) and RSS sampled every 10 s.

| Measure (vs no profiler) | otelprofile | pyroscope-go |
|---|---|---|
| Soak 4 CPU s (none 208.5) | 211.2 | 213.6 |
| Soak 4 allocations (none 52,926 MB) | +128 MB | +158 MB |
| Soak 4 peak RSS (none 37.9 MB) | 41.2 MB | 43.4 MB |
| Soak 4 mean / p95 RSS, 192 samples (none 33.1 / 37.7) | 38.4 / 40.6 MB | 38.4 / 41.8 MB |
| Soak 4 Go runtime memory at exit | 35.0 MB | 42.8 MB |
| Amplified (1 s) CPU s (none 53.2) | 53.7 | 55.1 |
| Amplified allocations | +522 MB | +577 MB |
| Amplified peak RSS (none 34.2) | 41.8 MB | 44.5 MB |

Row 10 flips to PASS: CPU, allocations and peak RSS are at or below
pyroscope-go, mean RSS equal.

Also checked: migrating an existing sigiro data directory written by the
released binary (`ALTER TABLE … ADD COLUMN labels`) keeps old rows and
stores new labelled, linked rows (`evidence/r4-migration.json`); the
released sigiro answers our OTLP with 400 (`evidence/r4-old-sigiro.json`),
which is why the sigiro proto change is needed.

Changes made during the round (so round 5 is the first unchanged run):
sigiro `mise run full` failed on a clippy `needless_update` in the new
segments test (fixed; `mise run full` now passes: rustfmt, check, clippy
-D warnings, 24 native-contract tests); the diff screenshot now uses a
settled 5-minute window on a long-running service (the newest minutes were
still being ingested); sub-unit diff changes show as 0; the demo logs type
names; `scripts/ab-report.sh` added.

## Round 5 — 2026-11-08 02:33 → 03:13 IST (40 min)

First run with the round-4 code frozen (otelprofile 6bd7ca1, sigiro
164fab1a). `scripts/scorecard.sh r5`: all PASS. Quickstart from README word
for word in a fresh directory: 54 s to the flame graph
(`evidence/r5-quickstart.png`). Same-process A/B: all PASS. Two failures:

- CPU A/B between two separate processes: top-20 share 3.09 points (limit
  3). The difference is one stack (darwin attributes 27 % vs 30 % of CPU to
  the HTTP server's `write` syscall), which differs between processes, not
  between profilers: each profile recovers the same share of its own
  process's rusage CPU (0.870 vs 0.866). Fix: the CPU A/B now runs **one**
  process that alternates pyroscope-go and otelprofile in 2-minute turns
  (`-profiler=alternate`); the separate processes stay for the rusage
  check. Also fixed: the last turn was lost at exit.
- Soak 5 RSS: mean 43.3 vs 40.4 MB, peak 49.9 vs 47.3 MB (soak 4 had mean
  RSS equal). CPU (201.7 vs 205.2 s) and allocations (+121 vs +140 MB)
  still favour otelprofile. Exact measurement (`bin/memcheck`,
  MemProfileRate=1, `evidence/r5-memcheck.txt`): otelprofile keeps 3.72 MB
  of non-workload heap alive, pyroscope-go 2.59 MB; the difference is the
  encoder's reusable state (~1.1 MB: decompression buffer, dictionary maps,
  name table, output buffers). An experiment that builds a fresh encoder
  per export brings live heap to 2.70 MB but allocates 1.75 MB more per
  cycle (+105 MB per minute at 1 s; about +220 MB over a 32-minute soak at
  15 s, against pyroscope-go's total overhead of +140 MB). RSS and
  allocations cannot both be at or below pyroscope-go with a converting
  exporter; this is now an open decision for the user (REPORT). Trimmed
  what was free: names only are interned (encoder 430 → 255 KB on real
  profiles in isolation).

## Round 6 — 2026-11-08 03:13 → 03:47 IST (34 min)

Frozen at 15da1a4 / sigiro 164fab1a. Quickstart 55 s. A/B all PASS: the
alternating-process CPU comparison gives −1.1 % total and 1.35 points
(`evidence/r6-ab-cpu.txt`), cpu/rusage 0.877 vs 0.870. Scorecard: one FAIL,
golangci-lint on the new `memcheck` tool (unchecked `Fprintf`), fixed;
everything else PASS. Soak 6: CPU 211.9 vs 217.6 s, allocations +124 vs
+140 MB, RSS mean 45.2 vs 38.7 MB, peak 49.0 vs 40.0 MB. RSS is flat after a
step in the first minutes, not a leak: live objects and heap goal are the
same as pyroscope-go's, the difference is `released` heap (13–19 MB vs
10–12 MB), pages Go has returned with MADV_FREE-style advice, which macOS
keeps counting in RSS until it reclaims them. Added a Linux soak (VmRSS)
to measure RSS where released pages leave RSS at once.

## Round 7 — 2026-11-08 03:47 → 04:22 IST (35 min)

Frozen at 4f06fc4 / sigiro 164fab1a.

- Quickstart from README: 55 s (`evidence/r7-quickstart.png`).
- Scorecard: vet+lint, race tests, fuzz, profcheck, all 11 types from both
  paths, resource attributes + links, dashboard (flame graph, label filter,
  diff, Pyroscope data, trace → profile): PASS.
- **Docker hung at 03:48** (OrbStack status "Running", `docker ps` and
  `docker version` never return; the Linux soak container stopped after
  one sample). Per the rules I did not restart OrbStack (it also runs
  another session's containers). Linux tests and the collector check were
  SKIPPED this round; both passed in rounds 2, 4, 5 and 6.
- A/B: same-process types all PASS. CPU (alternating process): totals
  −2.4 %, cpu/rusage 0.860 vs 0.858, but top-20 share 6.1 points (FAIL).
  The whole difference is one stack, darwin's attribution of kernel time
  to the HTTP server's `write` syscall (30.5 % vs 36.6 %); the application
  stacks agree (`main.queryDB` 32.25 % vs 32.44 %). That stack's share has
  flipped sign between runs (26.9 vs 19.6 in a test run) and moves most
  under load (the scorecard and the hung Docker VM were running). PASS in
  rounds 2, 3, 4, 6. A Linux CPU A/B would settle it but needs Docker.
- Soak 7: CPU 231.2 vs 239.1 s, allocations +128 vs +140 MB, peak RSS 49.1
  vs 50.0 MB, mean RSS 42.8 vs 41.2 MB (median 41.2 vs 41.0).

Status: two consecutive clean rounds were not reached. Remaining blockers
are outside what code changes here can settle: Docker, a user decision on
RSS versus allocations, and darwin-only CPU attribution noise in one
kernel stack (needs the Linux A/B, i.e. Docker).

## Round 8 — 2026-11-08 13:27 → 14:15 IST (48 min)

Product code frozen at ece2704 (sigiro 164fab1a). Docker back (OrbStack
restarted by the user). User decisions applied this round: RSS accepted
(keep the reused encoder state); CPU rule = linux full rule AND darwin
without the HTTP response write-syscall path.

- Quickstart from README with default ports (4318 free now), no edits: 57 s.
- Scorecard: all PASS, including linux/arm64 container tests and the
  collector (otelcol-contrib 0.165.0, links present, no errors).
- A/B, same process, non-CPU: all PASS on both OSes after one harness fix:
  goroutines now leave out the profilers' own and in-flight upload
  goroutines on both sides (raw darwin 5.06 pt: pyroscope-go's five idle
  upload workers and our snapshot's dial/roundTrip goroutines; filtered
  darwin 0.55 pt, linux 0.79 pt). The ±5-point goroutine exception is gone.
- **CPU A/B, linux/arm64 container (judged, full rule):** total −0.6 %,
  top-20 max 0.53 pt — PASS. Profiled ÷ rusage CPU 0.994 vs 0.994.
- **CPU A/B, darwin (judged, write path left out):** total −0.3 %, top-20
  max 0.79 pt — PASS. Information, write path included: 1.88 pt. Profiled ÷
  rusage CPU 0.868 vs 0.860. (Re-evaluated under the same rule: round 6
  0.74 pt, round 7 2.19 pt.)
- Soak, 32 min, cpu+heap, 15 s (values vs no profiler):

| | otelprofile | pyroscope-go |
|---|---|---|
| darwin CPU s (none 212.7) | 206.8 | 211.6 |
| darwin allocations | +128 MB | +139 MB |
| darwin mean / peak RSS (none 32.6 / 33.7 MB) | 39.9 / 41.4 MB | 39.3 / 41.6 MB |
| linux CPU s (none 191.2) | 192.1 | 194.9 |
| linux allocations | +146 MB | +159 MB |
| linux mean / peak VmRSS (none 16.9 / 26.3 MB) | 23.3 / 32.4 MB | 22.7 / 30.0 MB |

  Linux, where released pages leave RSS at once, shows the real cost of the
  accepted encoder state: +0.6 MB mean RSS.

Harness changed during the round (goroutine filter, CPU rule), so rounds 9
and 10 are the clean pair, frozen at 1c7c843.

## Round 9 — 2026-11-08 14:15 → 14:49 IST (34 min) — clean

Frozen at d305c16 (= 1c7c843 + docs), sigiro 164fab1a. Nothing changed
during the round.

- Quickstart from README, default ports, no edits: 57 s
  (`evidence/r9-quickstart.png`).
- Scorecard: all PASS, including linux/arm64 container tests and the
  collector (`evidence/r9-summary.txt`).
- A/B, same process, non-CPU: all PASS on darwin (max 0.53 pt) and linux
  (max 0.98 pt).
- **CPU A/B, linux/arm64 container (judged, full rule):** total +0.4 %,
  top-20 max 1.20 pt — PASS. Profiled ÷ rusage CPU 0.995 vs 0.994.
- **CPU A/B, darwin (judged, write path left out):** total −0.6 %, top-20
  max 1.18 pt — PASS. Information, write path included: 1.18 pt. Profiled ÷
  rusage CPU 0.878 vs 0.863.
- Soak, darwin, 32 min (vs no profiler): CPU 215.8 vs 216.7 s (none
  215.8); allocations +125 vs +142 MB; mean / peak RSS 42.9 / 49.5 vs
  40.8 / 47.2 MB (accepted by the user). PASS.
- Soak, linux container: Docker hung at 14:39:38, 24 minutes in (VmRSS
  stops there; `docker ps` does not return). Partial, 145 samples: mean
  VmRSS 23.5 vs 22.9 MB (none 16.8), the same +0.6 MB as round 8. Exit
  stats (CPU, allocations) were not written. This soak is the extra
  darwin-vs-linux RSS comparison; row 10 is judged on the 32-minute darwin
  soak.

Round 10 (the second clean round) needs Docker for the linux tests, the
collector and the linux CPU A/B; blocked until Docker is back. Both hangs
(03:48 and 14:39) happened while my linux soak container ran next to the
scorecard's test and collector containers; next time run the containers
one after another.

**Round 9 is invalid.** The Mac went into lid-closed Clamshell Sleep at
14:38:45 (pmset log; OrbStack vmgr `msg=sleep` at the same instant) and woke
at 14:59:58, inside the round window. That, not a Docker fault, is what
stopped the linux soak (the same cause as the 03:47:42 "hang" in round 7).
From now on every round is checked against `pmset -g log` and
`~/.orbstack/log/vmgr.log`; a round with a sleep inside its window is rerun.
Round 8 (13:27–14:15) had no sleep. Rounds 10 and 11 are the clean pair.

## Round 10 — 2026-11-08 15:03 → 15:58 IST (55 min) — clean

Code and harness frozen at 299ea89 (HEAD differed only in ROUNDS.md);
sigiro 164fab1a. Containers ran one after another (A/B, then scorecard,
then soak). **Sleep check: no pmset Sleep/Wake and no vmgr sleep/wake
between 15:03:32 and 15:58:30.**

- Quickstart from README, default ports, no edits: 54 s.
- Scorecard: all PASS (linux/arm64 tests, collector included).
- A/B, same process, non-CPU: all PASS (darwin max 1.26 pt, linux max
  2.69 pt).
- **CPU A/B, linux/arm64 (judged, full rule):** total −0.3 %, top-20 max
  1.04 pt — PASS. Profiled ÷ rusage CPU 0.993 vs 0.994.
- **CPU A/B, darwin (judged, write path left out):** total −0.6 %, top-20
  max 0.82 pt — PASS. Information, write path included: 1.17 pt. Profiled ÷
  rusage CPU 0.857 vs 0.849.
- Soak, 32 min, cpu+heap, 15 s (vs no profiler):

| | otelprofile | pyroscope-go |
|---|---|---|
| darwin CPU s (none 237.5) | 217.1 | 222.6 |
| darwin allocations | +127 MB | +138 MB |
| darwin mean / peak RSS (none 32.6 / 33.2) | 44.6 / 50.6 MB | 41.9 / 49.1 MB |
| linux CPU s (none 203.1) | 203.3 | 207.1 |
| linux allocations | +146 MB | +159 MB |
| linux mean / peak VmRSS (none 17.0 / 26.8) | 23.0 / 32.5 MB | 22.3 / 31.3 MB |

  RSS: accepted by the user (reused encoder state).

## Round 11 — 2026-11-08 15:59 → 16:19 IST (20 min) — not clean

Frozen at 299ea89 (HEAD 042eb85, docs only). No sleep in the window.
Quickstart 54 s; scorecard all PASS.

- **CPU A/B, linux/arm64 (judged, full rule):** total +0.7 %, top-20 max
  0.44 pt — PASS. Profiled ÷ rusage CPU 0.994 vs 0.994.
- **CPU A/B, darwin (judged, write path left out):** total +0.1 %, top-20
  max 1.01 pt — PASS. Information, write path included: 1.01 pt.
- Same-process non-CPU: darwin all PASS; **linux `inuse_objects` FAIL**
  (−8.2 %, 4.05 pt; it was 2.69 pt in round 10). The whole gap is
  `main.allocSmall`, the demo's 128-byte objects. At Go's default heap
  sampling rate (one sample per 512 KiB) each sampled 128-byte object
  stands for ~4,096 objects and the average snapshot held ~3,700 objects,
  i.e. less than one sample; both profilers read the same runtime heap
  profile a few ms apart, so a GC landing between the two reads moves a
  whole sample. That measures runtime sampling, not the profilers.
- Fix (harness): the A/B's both-profilers process now runs with
  `-memprofilerate=4096` (`scripts/ab.sh`, `scripts/ab-linux.sh`), so in-use
  counts rest on enough samples to compare. Soaks keep the default rate.
  Soak skipped (the round was already not clean). Rounds 12 and 13 are the
  clean pair.

## Round 12 — 2026-11-08 16:19 → 17:13 IST (54 min) — clean

Frozen at 42d3554, sigiro 164fab1a. Containers one after another. **Sleep
check: none between 16:19:33 and 17:13** (pmset, vmgr).

- Quickstart from README, default ports: 53 s. Scorecard: all PASS.
- Same-process non-CPU (heap sampled every 4 KiB): all PASS, darwin max
  1.42 pt (goroutines), linux max 0.48 pt.
- **CPU A/B, linux/arm64 (judged, full rule):** total +0.6 %, top-20 max
  0.39 pt — PASS. Profiled ÷ rusage CPU 0.994 vs 0.994.
- **CPU A/B, darwin (judged, write path left out):** total −0.2 %, top-20
  max 0.73 pt — PASS. Information, write path included: 0.98 pt. Profiled ÷
  rusage CPU 0.874 vs 0.856.
- Soak, 32 min (vs no profiler):

| | otelprofile | pyroscope-go |
|---|---|---|
| darwin CPU s (none 252.9) | 229.9 | 238.0 |
| darwin allocations | +128 MB | +143 MB |
| darwin mean / peak RSS (none 34.6 / 35.4) | 47.0 / 50.9 MB | 42.4 / 46.1 MB |
| linux CPU s (none 242.0) | 247.3 | 251.5 |
| linux allocations | +170 MB | +177 MB |
| linux mean / peak VmRSS (none 17.1 / 26.3) | 23.6 / 31.9 MB | 22.8 / 30.9 MB |

## Round 13 — 2026-11-08 17:15 → 18:09 IST (54 min) — clean

Frozen at 42d3554 (HEAD 0da74bd: ROUNDS.md and evidence only), sigiro
164fab1a. **Sleep check: none between 17:15:53 and 18:09** (pmset, vmgr).

- Quickstart from README, default ports: 55 s. Scorecard: all PASS.
- Same-process non-CPU: all PASS, darwin max 1.01 pt, linux max 0.83 pt.
- **CPU A/B, linux/arm64 (judged, full rule):** total +0.2 %, top-20 max
  0.63 pt — PASS. Profiled ÷ rusage CPU 0.994 vs 0.995.
- **CPU A/B, darwin (judged, write path left out):** total −0.1 %, top-20
  max 0.46 pt — PASS. Information, write path included: 1.69 pt. Profiled ÷
  rusage CPU 0.877 vs 0.862.
- Soak, 32 min (vs no profiler):

| | otelprofile | pyroscope-go |
|---|---|---|
| darwin CPU s (none 231.9) | 220.2 | 222.9 |
| darwin allocations | +134 MB | +140 MB |
| darwin mean / peak RSS (none 35.1 / 38.1) | 42.1 / 45.4 MB | 43.8 / 49.0 MB |
| linux CPU s (none 194.6) | 195.2 | 199.3 |
| linux allocations | +143 MB | +153 MB |
| linux mean / peak VmRSS (none 17.2 / 27.2) | 23.5 / 31.7 MB | 22.9 / 30.8 MB |

**Rounds 12 and 13 are two consecutive clean rounds with nothing changed
between them.** Release steps follow.
