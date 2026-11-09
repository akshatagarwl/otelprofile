## Environment
```
Linux runnervmwuk1x 7.0.0-1019-azure #20-Ubuntu SMP PREEMPT Mon Oct 19 22:41:16 UTC 2026 x86_64 GNU/Linux
4
PRETTY_NAME="Ubuntu 26.04.1 LTS"
-r--r--r-- 1 root root 7267528 Nov  9 12:53 /sys/kernel/btf/vmlinux
go version go1.27.3 linux/amd64
```

## Quality (eBPF 97 Hz)

```json
{
 "name": "qotel",
 "rusage_cpu_s": 35.701,
 "maxrss_mb": 38.3,
 "ebpf_cpu_s": 34.86,
 "ebpf_vs_rusage": 0.976,
 "ebpf_types": {
  "samples/count period=cpu": 3381
 },
 "ebpf_leaf_caller_kept": "0/2278",
 "ebpf_pct_cpu_linked": 0.0,
 "otel_cpu_s": 35.28,
 "otel_vs_rusage": 0.988,
 "otel_leaf_caller_kept": "2534/2534",
 "otel_inlined_funcs": 120,
 "otel_pct_cpu_linked": 72.2,
 "otel_link_main.queryDB": {
  "query-db": 1050
 },
 "otel_link_main.renderPage": {
  "GET /slow": 335
 },
 "otel_link_main.fastHandler": {
  "GET /fast": 1156
 },
 "otel_link_other": {
  "GET /fast": 4,
  "GET /slow": 2,
  "query-db": 1
 },
 "ebpf_hotspot_pct": {
  "other": 32.2,
  "main.fastHandler": 29.5,
  "main.queryDB": 27.8,
  "main.renderPage": 10.5
 },
 "otel_hotspot_pct": {
  "main.fastHandler": 32.8,
  "main.queryDB": 29.8,
  "other": 28.0,
  "main.renderPage": 9.5
 }
}
```
```json
{
 "name": "qnone",
 "rusage_cpu_s": 35.792,
 "maxrss_mb": 29.6,
 "ebpf_cpu_s": 35.31,
 "ebpf_vs_rusage": 0.987,
 "ebpf_types": {
  "samples/count period=cpu": 3425
 },
 "ebpf_leaf_caller_kept": "0/2270",
 "ebpf_pct_cpu_linked": 0.0,
 "ebpf_hotspot_pct": {
  "other": 33.3,
  "main.queryDB": 29.0,
  "main.fastHandler": 28.2,
  "main.renderPage": 9.5
 }
}
```
```json
{
 "name": "qotel",
 "otel_inlined_funcs": 120,
 "of_which_in_ebpf_stacks": 2
}
```

## Off-CPU probe

```
started on try 1
```
```json
{
 "name": "offnone",
 "rusage_cpu_s": 14.687,
 "maxrss_mb": 29.2,
 "ebpf_cpu_s": 14.62,
 "ebpf_vs_rusage": 0.995,
 "ebpf_types": {
  "off_cpu/nanoseconds period=": 32806,
  "samples/count period=cpu": 1418
 },
 "ebpf_leaf_caller_kept": "0/917",
 "ebpf_pct_cpu_linked": 0.0,
 "offcpu_s": 107.705,
 "offcpu_top": [
  [
   "finish_task_switch.isra.0 <- schedule <- futex_do_wait <- __futex_wait <- futex_wait",
   78.5
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- schedule_hrtimeout_range_clock <- schedule_hrtimeout_range <- ep_poll",
   11.1
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- do_nanosleep <- hrtimer_nanosleep <- __x64_sys_nanosleep",
   10.4
  ],
  [
   "finish_task_switch.isra.0 <- preempt_schedule <- preempt_schedule_thunk <- _raw_spin_unlock_irqrestore <- sched_move_task",
   0.0
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- irqentry_exit <- sysvec_hyperv_stimer0 <- asm_sysvec_hyperv_stimer0",
   0.0
  ]
 ],
 "ebpf_hotspot_pct": {
  "other": 34.7,
  "main.queryDB": 28.3,
  "main.fastHandler": 27.6,
  "main.renderPage": 9.4
 }
}
```

## CPU profiled / CPU used, two identical processes

| requests | eBPF rate | process a | process b |
|---|---|---|---|
| ticker | 20 Hz | 0.128 | 0.897 |
| ticker | 100 Hz | 0.594 | 1.778 |
| ticker | 97 Hz | 0.998 | 0.991 |
| poisson | 20 Hz | 0.984 | 1.409 |
| poisson | 100 Hz | 0.823 | 0.839 |
| poisson | 97 Hz | 0.901 | 0.928 |

## Cost inside the profiled process (CPU seconds per probe)

| round | no eBPF | eBPF 97 Hz | eBPF 20 Hz |
|---|---|---|---|
| 1 | 14.073 | 14.239 | 14.229 |
| 2 | 14.072 | 14.091 | 14.115 |
| 3 | 14.207 | 14.126 | 14.184 |
| mean | 14.12 | 14.15 | 14.18 |

`ovh-97-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 44434595, "run_cnt": 48694, "ns_per_run": 913}

`ovh-97-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 42037830, "run_cnt": 48698, "ns_per_run": 863}

`ovh-97-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 42021216, "run_cnt": 48696, "ns_per_run": 863}

`ovh-20-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 9106461, "run_cnt": 10038, "ns_per_run": 907}

`ovh-20-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 12192098, "run_cnt": 10040, "ns_per_run": 1214}

`ovh-20-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 8515463, "run_cnt": 10044, "ns_per_run": 848}

## OBI trace links

```json
{
 "name": "obinone",
 "rusage_cpu_s": 24.817,
 "maxrss_mb": 29.0,
 "ebpf_cpu_s": 24.04,
 "ebpf_vs_rusage": 0.969,
 "ebpf_types": {
  "samples/count period=cpu": 2332
 },
 "ebpf_leaf_caller_kept": "0/1286",
 "ebpf_pct_cpu_linked": 53.3,
 "obi_main.fastHandler": {
  "linked_samples": 527,
  "trace_found": 527,
  "span_found": 527,
  "linked_span": {
   "processing": 527
  }
 },
 "obi_main.queryDB": {
  "linked_samples": 499,
  "trace_found": 499,
  "span_found": 499,
  "linked_span": {
   "processing": 499
  }
 },
 "obi_main.renderPage": {
  "linked_samples": 170,
  "trace_found": 170,
  "span_found": 170,
  "linked_span": {
   "processing": 170
  }
 },
 "obi_other": {
  "linked_samples": 46,
  "trace_found": 46,
  "span_found": 46,
  "linked_span": {
   "processing": 46
  }
 },
 "ebpf_hotspot_pct": {
  "other": 44.6,
  "main.fastHandler": 24.4,
  "main.queryDB": 23.1,
  "main.renderPage": 7.9
 }
}
```
