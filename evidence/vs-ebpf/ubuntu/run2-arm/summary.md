## Environment
```
Linux runnervmkb2gk 7.0.0-1017-azure #17~24.04.1-Ubuntu SMP PREEMPT Thu Sep 24 19:06:39 UTC 2026 aarch64 aarch64 aarch64 GNU/Linux
4
PRETTY_NAME="Ubuntu 24.04.5 LTS"
-r--r--r-- 1 root root 8430525 Nov  9 13:17 /sys/kernel/btf/vmlinux
go version go1.27.3 linux/arm64
```

## Quality (eBPF 97 Hz)

```json
{
 "name": "qotel",
 "rusage_cpu_s": 32.115,
 "maxrss_mb": 38.3,
 "ebpf_cpu_s": 31.98,
 "ebpf_vs_rusage": 0.996,
 "ebpf_types": {
  "samples/count period=cpu": 3102
 },
 "ebpf_leaf_caller_kept": "0/1956",
 "ebpf_pct_cpu_linked": 0.0,
 "otel_cpu_s": 31.76,
 "otel_vs_rusage": 0.989,
 "otel_leaf_caller_kept": "2080/2080",
 "otel_inlined_funcs": 141,
 "otel_pct_cpu_linked": 66.3,
 "otel_link_main.queryDB": {
  "query-db": 857
 },
 "otel_link_main.fastHandler": {
  "GET /fast": 960
 },
 "otel_link_main.renderPage": {
  "GET /slow": 272
 },
 "otel_link_other": {
  "GET /fast": 14,
  "GET /slow": 2,
  "query-db": 1
 },
 "ebpf_hotspot_pct": {
  "other": 36.9,
  "main.fastHandler": 27.3,
  "main.queryDB": 26.1,
  "main.renderPage": 9.6
 },
 "otel_hotspot_pct": {
  "other": 34.2,
  "main.fastHandler": 30.2,
  "main.queryDB": 27.0,
  "main.renderPage": 8.6
 }
}
```
```json
{
 "name": "qnone",
 "rusage_cpu_s": 31.73,
 "maxrss_mb": 28.8,
 "ebpf_cpu_s": 29.63,
 "ebpf_vs_rusage": 0.934,
 "ebpf_types": {
  "samples/count period=cpu": 2874
 },
 "ebpf_leaf_caller_kept": "0/1747",
 "ebpf_pct_cpu_linked": 0.0,
 "ebpf_hotspot_pct": {
  "other": 39.2,
  "main.fastHandler": 28.6,
  "main.queryDB": 23.0,
  "main.renderPage": 9.2
 }
}
```
```json
{
 "name": "qotel",
 "otel_inlined_funcs": 141,
 "of_which_in_ebpf_stacks": 1
}
```

## Off-CPU probe

```
started on try 1
```
```json
{
 "name": "offnone",
 "rusage_cpu_s": 12.203,
 "maxrss_mb": 29.1,
 "ebpf_cpu_s": 12.95,
 "ebpf_vs_rusage": 1.061,
 "ebpf_types": {
  "off_cpu/nanoseconds period=": 33445,
  "samples/count period=cpu": 1256
 },
 "ebpf_leaf_caller_kept": "0/752",
 "ebpf_pct_cpu_linked": 0.0,
 "offcpu_s": 101.651,
 "offcpu_top": [
  [
   "finish_task_switch.isra.0 <- schedule <- futex_do_wait <- __futex_wait <- futex_wait",
   76.7
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- schedule_hrtimeout_range_clock <- schedule_hrtimeout_range <- ep_poll",
   11.7
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- do_nanosleep <- hrtimer_nanosleep <- __arm64_sys_nanosleep",
   11.6
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- do_sched_yield <- __arm64_sys_sched_yield <- invoke_syscall",
   0.0
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- exit_to_user_mode_loop <- el0_interrupt <- __el0_irq_handler_common",
   0.0
  ]
 ],
 "ebpf_hotspot_pct": {
  "other": 40.1,
  "main.fastHandler": 26.5,
  "main.queryDB": 23.9,
  "main.renderPage": 9.5
 }
}
```

## CPU profiled / CPU used, two identical processes

| requests | eBPF rate | process a | process b |
|---|---|---|---|
| ticker | 20 Hz | 2.567 | 0.158 |
| ticker | 100 Hz | 0.592 | 2.0 |
| ticker | 97 Hz | 0.965 | 0.965 |
| poisson | 20 Hz | 0.808 | 2.02 |
| poisson | 100 Hz | 0.839 | 0.838 |
| poisson | 97 Hz | 0.948 | 0.918 |

## Cost inside the profiled process (CPU seconds per probe)

| round | no eBPF | eBPF 97 Hz | eBPF 20 Hz |
|---|---|---|---|
| 1 | 11.561 | 11.746 | 11.659 |
| 2 | 11.644 | 11.693 | 11.613 |
| 3 | 11.555 | 11.693 | 11.691 |
| mean | 11.59 | 11.71 | 11.65 |

`ovh-97-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 53017440, "run_cnt": 48678, "ns_per_run": 1089}

`ovh-97-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 51974544, "run_cnt": 48673, "ns_per_run": 1068}

`ovh-97-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 48492808, "run_cnt": 48646, "ns_per_run": 997}

`ovh-20-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 16010328, "run_cnt": 10036, "ns_per_run": 1595}

`ovh-20-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 12417176, "run_cnt": 10040, "ns_per_run": 1237}

`ovh-20-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 13044384, "run_cnt": 10034, "ns_per_run": 1300}

## OBI trace links

```json
{
 "name": "obinone",
 "rusage_cpu_s": 20.596,
 "maxrss_mb": 28.6,
 "ebpf_cpu_s": 20.08,
 "ebpf_vs_rusage": 0.975,
 "ebpf_types": {
  "samples/count period=cpu": 1948
 },
 "ebpf_leaf_caller_kept": "0/1125",
 "ebpf_pct_cpu_linked": 55.1,
 "obi_main.fastHandler": {
  "linked_samples": 453,
  "trace_found": 453,
  "span_found": 453,
  "linked_span": {
   "processing": 453
  }
 },
 "obi_main.renderPage": {
  "linked_samples": 169,
  "trace_found": 169,
  "span_found": 169,
  "linked_span": {
   "processing": 169
  }
 },
 "obi_other": {
  "linked_samples": 22,
  "trace_found": 22,
  "span_found": 22,
  "linked_span": {
   "processing": 22
  }
 },
 "obi_main.queryDB": {
  "linked_samples": 429,
  "trace_found": 429,
  "span_found": 429,
  "linked_span": {
   "processing": 429
  }
 },
 "ebpf_hotspot_pct": {
  "other": 42.2,
  "main.fastHandler": 24.8,
  "main.queryDB": 23.6,
  "main.renderPage": 9.3
 }
}
```
