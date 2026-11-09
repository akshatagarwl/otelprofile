#!/usr/bin/env python3
"""Markdown summary of a ci.sh output directory.

    ci-summary.py <outdir>
"""
import glob
import json
import os
import statistics
import sys

root = sys.argv[1]


def report(d):
    p = os.path.join(root, d, "report.jsonl")
    if not os.path.exists(p):
        return []
    return [json.loads(l) for l in open(p) if l.strip()]


def first(d, key="name"):
    return {r[key]: r for r in report(d) if "rusage_cpu_s" in r}


print("## Environment\n```")
print(open(os.path.join(root, "env.txt")).read().strip())
print("```\n")
if os.path.exists(os.path.join(root, "failures.txt")):
    print("Failures:\n```\n" + open(os.path.join(root, "failures.txt")).read() + "```\n")

print("## Quality (eBPF 97 Hz)\n")
for r in report("quality"):
    print("```json\n" + json.dumps(r, indent=1) + "\n```")
print("\n## Off-CPU probe\n")
starts = os.path.join(root, "offcpu", "starts.txt")
if os.path.exists(starts):
    print("```\n" + open(starts).read().strip() + "\n```")
for r in report("offcpu"):
    print("```json\n" + json.dumps(r, indent=1) + "\n```")

print("\n## CPU profiled / CPU used, two identical processes\n")
print("| requests | eBPF rate | process a | process b |")
print("|---|---|---|---|")
for arr in ("ticker", "poisson"):
    for hz in (20, 100, 97):
        rs = list(first(f"rate-{arr}-{hz}").values())
        vals = " | ".join(str(r.get("ebpf_vs_rusage")) for r in rs)
        print(f"| {arr} | {hz} Hz | {vals} |")

print("\n## Cost inside the profiled process (CPU seconds per probe)\n")
print("| round | no eBPF | eBPF 97 Hz | eBPF 20 Hz |")
print("|---|---|---|---|")
cols = {"off": [], "97": [], "20": []}
for r in (1, 2, 3):
    row = []
    for c in ("off", "97", "20"):
        v = [x["rusage_cpu_s"] for x in first(f"ovh-{c}-{r}").values()]
        row.append(str(v[0]) if v else "-")
        cols[c] += v
    print(f"| {r} | " + " | ".join(row) + " |")
if all(cols.values()):
    print("| mean | " + " | ".join(f"{statistics.mean(cols[c]):.2f}" for c in ("off", "97", "20")) + " |")
for c in ("97", "20"):
    for f in sorted(glob.glob(os.path.join(root, f"ovh-{c}-*", "bpf-stats.json"))):
        print(f"\n`{os.path.relpath(f, root)}`: {open(f).read().strip()}")

print("\n## OBI trace links\n")
for r in report("obi"):
    print("```json\n" + json.dumps(r, indent=1) + "\n```")
