## Environment
```
Linux runnervmkb2gk 7.0.0-1017-azure #17~24.04.1-Ubuntu SMP PREEMPT Thu Sep 24 19:06:39 UTC 2026 aarch64 aarch64 aarch64 GNU/Linux
4
PRETTY_NAME="Ubuntu 24.04.5 LTS"
-r--r--r-- 1 root root 8430525 Nov  9 12:53 /sys/kernel/btf/vmlinux
go version go1.27.3 linux/arm64
```

## Quality (eBPF 97 Hz)

```json
{
 "name": "qotel",
 "rusage_cpu_s": 32.477,
 "maxrss_mb": 38.4,
 "ebpf_cpu_s": 31.15,
 "ebpf_vs_rusage": 0.959,
 "ebpf_types": {
  "samples/count period=cpu": 3022
 },
 "ebpf_leaf_caller_kept": "0/1841",
 "ebpf_pct_cpu_linked": 0.0,
 "otel_cpu_s": 32.11,
 "otel_vs_rusage": 0.989,
 "otel_leaf_caller_kept": "2103/2103",
 "otel_inlined_funcs": 127,
 "otel_pct_cpu_linked": 65.9,
 "otel_link_main.fastHandler": {
  "GET /fast": 995
 },
 "otel_link_main.queryDB": {
  "query-db": 851
 },
 "otel_link_main.renderPage": {
  "GET /slow": 265
 },
 "otel_link_other": {
  "GET /fast": 6
 },
 "ebpf_hotspot_pct": {
  "other": 39.0,
  "main.fastHandler": 26.7,
  "main.queryDB": 24.4,
  "main.renderPage": 9.8
 },
 "otel_hotspot_pct": {
  "other": 34.3,
  "main.fastHandler": 31.0,
  "main.queryDB": 26.5,
  "main.renderPage": 8.3
 }
}
```
```json
{
 "name": "qnone",
 "rusage_cpu_s": 32.195,
 "maxrss_mb": 28.7,
 "ebpf_cpu_s": 30.91,
 "ebpf_vs_rusage": 0.96,
 "ebpf_types": {
  "samples/count period=cpu": 2998
 },
 "ebpf_leaf_caller_kept": "0/1694",
 "ebpf_pct_cpu_linked": 0.0,
 "ebpf_hotspot_pct": {
  "other": 43.1,
  "main.queryDB": 24.9,
  "main.fastHandler": 24.8,
  "main.renderPage": 7.1
 }
}
```
```json
{
 "name": "qotel",
 "otel_inlined_funcs": 127,
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
 "rusage_cpu_s": 12.302,
 "maxrss_mb": 28.6,
 "ebpf_cpu_s": 11.86,
 "ebpf_vs_rusage": 0.964,
 "ebpf_types": {
  "off_cpu/nanoseconds period=": 33239,
  "samples/count period=cpu": 1150
 },
 "ebpf_leaf_caller_kept": "0/696",
 "ebpf_pct_cpu_linked": 0.0,
 "offcpu_s": 75.395,
 "offcpu_top": [
  [
   "finish_task_switch.isra.0 <- schedule <- futex_do_wait <- __futex_wait <- futex_wait",
   69.6
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- schedule_hrtimeout_range_clock <- schedule_hrtimeout_range <- ep_poll",
   15.6
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- do_nanosleep <- hrtimer_nanosleep <- __arm64_sys_nanosleep",
   14.8
  ],
  [
   "finish_task_switch.isra.0 <- preempt_schedule <- _raw_spin_unlock_irqrestore <- sched_move_task <- sched_autogroup_exit_task",
   0.0
  ],
  [
   "finish_task_switch.isra.0 <- schedule <- do_sched_yield <- __arm64_sys_sched_yield <- invoke_syscall",
   0.0
  ]
 ],
 "ebpf_hotspot_pct": {
  "other": 39.5,
  "main.fastHandler": 28.5,
  "main.queryDB": 22.8,
  "main.renderPage": 9.2
 }
}
```

## CPU profiled / CPU used, two identical processes

| requests | eBPF rate | process a | process b |
|---|---|---|---|
| ticker | 20 Hz | 0.114 | 0.207 |
| ticker | 100 Hz | 0.137 | 0.606 |
| ticker | 97 Hz | 0.957 | 0.981 |
| poisson | 20 Hz | 0.785 | 0.774 |
| poisson | 100 Hz | 1.004 | 0.786 |
| poisson | 97 Hz | 0.95 | 0.969 |

## Cost inside the profiled process (CPU seconds per probe)

| round | no eBPF | eBPF 97 Hz | eBPF 20 Hz |
|---|---|---|---|
| 1 | 11.694 | 11.77 | 11.762 |
| 2 | 11.689 | 11.762 | 11.637 |
| 3 | 11.546 | 11.823 | 11.755 |
| mean | 11.64 | 11.79 | 11.72 |

`ovh-97-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 51542136, "run_cnt": 48636, "ns_per_run": 1060}

`ovh-97-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 51339680, "run_cnt": 48640, "ns_per_run": 1056}

`ovh-97-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 51618072, "run_cnt": 48623, "ns_per_run": 1062}

`ovh-20-1/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 26994040, "run_cnt": 10032, "ns_per_run": 2691}

`ovh-20-2/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 13678344, "run_cnt": 10024, "ns_per_run": 1365}

`ovh-20-3/bpf-stats.json`: {"program": "native_tracer_entry", "run_time_ns": 13842840, "run_cnt": 10032, "ns_per_run": 1380}

## OBI trace links

```json
{
 "name": "obinone",
 "rusage_cpu_s": 20.86,
 "maxrss_mb": 28.2,
 "ebpf_cpu_s": 20.55,
 "ebpf_vs_rusage": 0.985,
 "ebpf_types": {
  "samples/count period=cpu": 1993
 },
 "ebpf_leaf_caller_kept": "0/1017",
 "ebpf_pct_cpu_linked": 53.3,
 "obi_main.renderPage": {
  "linked_samples": 124,
  "trace_found": 124,
  "span_found": 124,
  "linked_span": {
   "processing": 124
  }
 },
 "obi_main.fastHandler": {
  "linked_samples": 451,
  "trace_found": 451,
  "span_found": 451,
  "linked_span": {
   "processing": 451
  }
 },
 "obi_main.queryDB": {
  "linked_samples": 411,
  "trace_found": 411,
  "span_found": 411,
  "linked_span": {
   "processing": 411
  }
 },
 "obi_other": {
  "linked_samples": 76,
  "trace_found": 76,
  "span_found": 76,
  "linked_span": {
   "processing": 76
  }
 },
 "ebpf_hotspot_pct": {
  "other": 48.4,
  "main.fastHandler": 23.5,
  "main.queryDB": 21.5,
  "main.renderPage": 6.5
 }
}
```
