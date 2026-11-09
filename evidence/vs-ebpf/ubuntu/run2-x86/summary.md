## Environment
```
Linux runnervmwuk1x 7.0.0-1019-azure #20-Ubuntu SMP PREEMPT Mon Oct 19 22:41:16 UTC 2026 x86_64 GNU/Linux
4
PRETTY_NAME="Ubuntu 26.04.1 LTS"
-r--r--r-- 1 root root 7267528 Nov  9 13:16 /sys/kernel/btf/vmlinux
go version go1.27.3 linux/amd64
```

## Quality (eBPF 97 Hz)

```json
{
 "name": "qotel",
 "rusage_cpu_s": 42.859,
 "maxrss_mb": 39.4,
 "ebpf_cpu_s": 40.18,
 "ebpf_vs_rusage": 0.937,
 "ebpf_types": {
  "samples/count period=cpu": 3897
 },
 "ebpf_leaf_caller_kept": "0/2487",
 "ebpf_pct_cpu_linked": 0.0,
 "otel_cpu_s": 42.3,
 "otel_vs_rusage": 0.987,
 "otel_leaf_caller_kept": "2936/2936",
 "otel_inlined_funcs": 124,
 "otel_pct_cpu_linked": 69.9,
 "otel_link_main.fastHandler": {
  "GET /fast": 1340
 },
 "otel_link_main.renderPage": {
  "GET /slow": 389
 },
 "otel_link_main.queryDB": {
  "query-db": 1215
 },
 "otel_link_other": {
  "GET /fast": 7,
  "GET /slow": 3,
  "query-db": 2
 },
 "ebpf_hotspot_pct": {
  "other": 35.5,
  "main.queryDB": 29.0,
  "main.fastHandler": 26.7,
  "main.renderPage": 8.8
 },
 "otel_hotspot_pct": {
  "main.fastHandler": 31.7,
  "other": 30.4,
  "main.queryDB": 28.7,
  "main.renderPage": 9.2
 }
}
```
```json
{
 "name": "qnone",
 "rusage_cpu_s": 42.516,
 "maxrss_mb": 29.7,
 "ebpf_cpu_s": 39.67,
 "ebpf_vs_rusage": 0.933,
 "ebpf_types": {
  "samples/count period=cpu": 3848
 },
 "ebpf_leaf_caller_kept": "0/2522",
 "ebpf_pct_cpu_linked": 0.0,
 "ebpf_hotspot_pct": {
  "other": 33.8,
  "main.fastHandler": 28.3,
  "main.queryDB": 26.2,
  "main.renderPage": 11.8
 }
}
```
```json
{
 "name": "qotel",
 "otel_inlined_funcs": 124,
 "of_which_in_ebpf_stacks": 0
}
```

## Off-CPU probe

```
started on try 1
```
```json
{
 "name": "offnone",
 "rusage_cpu_s": 17.805,
 "maxrss_mb": 29.1,
 "ebpf_cpu_s": 16.97,
 "ebpf_vs_rusage": 0.953,
 "ebpf_types": {
  "off_cpu/nanoseconds period=": 32651,
  "samples/count period=cpu": 1646
 },
 "ebpf_leaf_caller_kept": "0/1045",
 "ebpf_pct_cpu_linked": 0.0,
 "offcpu_s": 76.71,
 "offcpu_top": [
  [
   "finish_task_switch.isra.0 <- schedule <- futex_do_wait <- __futex_wait <- futex_wait",
   69.6
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- schedule_hrtimeout_range_clock <- schedule_hrtimeout_range <- ep_poll",
   15.3
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- do_nanosleep <- hrtimer_nanosleep <- __x64_sys_nanosleep",
   15.1
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- irqentry_exit <- sysvec_hyperv_stimer0 <- asm_sysvec_hyperv_stimer0",
   0.0
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- do_sched_yield <- __x64_sys_sched_yield <- x64_sys_call",
   0.0
  ]
 ],
 "ebpf_hotspot_pct": {
  "other": 35.9,
  "main.fastHandler": 28.9,
  "main.queryDB": 24.4,
  "main.renderPage": 10.9
 }
}
```

## CPU profiled / CPU used, two identical processes

| requests | eBPF rate | process a | process b |
|---|---|---|---|
| ticker | 20 Hz | 2.036 | 0.221 |
| ticker | 100 Hz | 0.527 | 1.669 |
| ticker | 97 Hz | 0.939 | 0.978 |
| poisson | 20 Hz | 0.746 | 0.813 |
| poisson | 100 Hz | 0.899 | 0.814 |
| poisson | 97 Hz | 0.942 | 0.954 |

## Cost inside the profiled process (CPU seconds per probe)

| round | no eBPF | eBPF 97 Hz | eBPF 20 Hz |
|---|---|---|---|
| 1 | 16.978 | 16.92 | 16.934 |
| 2 | 16.803 | 16.875 | 16.758 |
| 3 | 16.875 | 16.81 | 16.86 |
| mean | 16.89 | 16.87 | 16.85 |

`ovh-97-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 58177868, "run_cnt": 48703, "ns_per_run": 1195}

`ovh-97-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 52502451, "run_cnt": 48697, "ns_per_run": 1078}

`ovh-97-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 55332159, "run_cnt": 48700, "ns_per_run": 1136}

`ovh-20-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 9084925, "run_cnt": 10044, "ns_per_run": 905}

`ovh-20-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 7805181, "run_cnt": 10043, "ns_per_run": 777}

`ovh-20-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 46495575, "run_cnt": 10039, "ns_per_run": 4631}

## OBI trace links

```json
{
 "name": "obinone",
 "rusage_cpu_s": 29.437,
 "maxrss_mb": 29.2,
 "ebpf_cpu_s": 29.29,
 "ebpf_vs_rusage": 0.995,
 "ebpf_types": {
  "samples/count period=cpu": 2841
 },
 "ebpf_leaf_caller_kept": "0/1486",
 "ebpf_pct_cpu_linked": 52.4,
 "obi_main.fastHandler": {
  "linked_samples": 592,
  "trace_found": 592,
  "span_found": 592,
  "linked_span": {
   "processing": 592
  }
 },
 "obi_main.queryDB": {
  "linked_samples": 583,
  "trace_found": 583,
  "span_found": 583,
  "linked_span": {
   "processing": 583
  }
 },
 "obi_main.renderPage": {
  "linked_samples": 240,
  "trace_found": 240,
  "span_found": 240,
  "linked_span": {
   "processing": 240
  }
 },
 "obi_other": {
  "linked_samples": 74,
  "trace_found": 74,
  "span_found": 74,
  "linked_span": {
   "processing": 74
  }
 },
 "ebpf_hotspot_pct": {
  "other": 47.4,
  "main.fastHandler": 22.0,
  "main.queryDB": 21.6,
  "main.renderPage": 9.0
 }
}
```
