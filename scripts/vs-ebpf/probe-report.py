#!/usr/bin/env python3
"""Per-process summary of one probe.sh run.

    probe-report.py <outdir> <name> ...

For each demo process <name> it compares, against the process's own rusage
CPU time:
  - the CPU the eBPF profiler attributed to the executable <name>
  - the CPU otelprofile reported for service <name> (if it ran)
and checks stack quality on samples whose leaf is main.spinLCG:
  - leaf_caller_kept: share whose next frame is main.burn (its real caller)
It also reports, for the eBPF side:
  - every profile type it sent (e.g. CPU samples, off-CPU)
  - trace links on CPU samples, whether linked trace IDs exist in the traces
    the hub received (OBI test), and the name of the linked span, split by
    the function on the stack (queryDB runs inside the query-db span)
  - the top off-CPU stacks, if the off-CPU probe ran
"""
import collections
import json
import os
import sys

out, names = sys.argv[1], sys.argv[2:]


def sval(v, st):
    if "stringValueStrindex" in v:
        return st[v["stringValueStrindex"]]
    if "stringValue" in v:
        return v["stringValue"]
    if "intValue" in v:
        return str(v["intValue"])
    return ""


cpu = {}
for line in open(os.path.join(out, "rusage.txt")):
    n, js = line.split(" ", 1)
    cpu[n] = json.loads(js)["cpu_s"]

R = {n: {"rusage_cpu_s": cpu.get(n), "ebpf_types": collections.Counter(), "ebpf_cpu_s": 0.0, "otel_cpu_s": 0.0,
         "leaf": {"ebpf": [0, 0], "otel": [0, 0]}, "linked": 0, "cpu_samples": 0,
         "trace_ids": collections.defaultdict(collections.Counter),
         "otel_trace_ids": collections.defaultdict(collections.Counter), "otel_linked": 0, "otel_samples": 0, "offcpu": collections.Counter(),
         "otel_inlined": set(), "ebpf_funcs": set()} for n in names}

for line in open(os.path.join(out, "profiles.jsonl")):
    d = json.loads(line)
    dic = d["dictionary"]
    st = dic["stringTable"]
    for rp in d["resourceProfiles"]:
        res = {st[a["keyStrindex"]]: a["value"] for a in rp["resource"].get("attributes", [])}
        for sp in rp["scopeProfiles"]:
            is_ebpf = "ebpf" in sp.get("scope", {}).get("name", "")
            for p in sp["profiles"]:
                stype = st[p["sampleType"].get("typeStrindex", 0)]
                unit = st[p["sampleType"].get("unitStrindex", 0)]
                ptype = st[p.get("periodType", {}).get("typeStrindex", 0)]
                period = int(p.get("period", 0) or 0)
                for s in p.get("samples", []):
                    sa = {st[dic["attributeTable"][i]["keyStrindex"]]: dic["attributeTable"][i]["value"] for i in s.get("attributeIndices", [])}
                    attrs = {**res, **sa}
                    who = sval(attrs.get("process.executable.name", {}), st) if is_ebpf else sval(attrs.get("service.name", {}), st)
                    if who not in R:
                        continue
                    r = R[who]
                    frames, inlined = [], []
                    for li in dic["stackTable"][s.get("stackIndex", 0)].get("locationIndices", []):
                        lines = dic["locationTable"][li].get("lines", [])
                        if not lines:
                            frames.append("[unknown]")
                        for j, ln in enumerate(lines):
                            nm = st[dic["functionTable"][ln.get("functionIndex", 0)].get("nameStrindex", 0)] or "[unknown]"
                            frames.append(nm)
                            if j < len(lines) - 1:
                                inlined.append(nm)
                    if is_ebpf:
                        n = len(s.get("timestampsUnixNano", [])) or (int(s["values"][0]) if s.get("values") else 1)
                        r["ebpf_types"][f"{stype}/{unit} period={ptype}"] += n
                        if stype == "samples" and ptype == "cpu":
                            r["ebpf_cpu_s"] += n * period / 1e9
                            r["cpu_samples"] += n
                            r["ebpf_funcs"].update(frames)
                            if s.get("linkIndex"):
                                r["linked"] += n
                                lk = dic["linkTable"][s["linkIndex"]]
                                h = next((x for x in ("main.queryDB", "main.renderPage", "main.fastHandler") if x in frames), "other")
                                r["trace_ids"][h][(lk.get("traceId", ""), lk.get("spanId", ""))] += n
                            side = "ebpf"
                        else:
                            if frames:
                                r["offcpu"][" <- ".join(frames[:5])] += int(s["values"][0]) if s.get("values") else n
                            continue
                    else:
                        if stype != "samples":
                            continue
                        n = int(s["values"][0])
                        r["otel_cpu_s"] += n / 100
                        r["otel_samples"] += n
                        if s.get("linkIndex"):
                            r["otel_linked"] += n
                            lk = dic["linkTable"][s["linkIndex"]]
                            h = next((x for x in ("main.queryDB", "main.renderPage", "main.fastHandler") if x in frames), "other")
                            r["otel_trace_ids"][h][(lk.get("traceId", ""), lk.get("spanId", ""))] += n
                        r["otel_inlined"].update(inlined)
                        side = "otel"
                    if frames and frames[0] == "main.spinLCG":
                        r["leaf"][side][1] += n
                        if len(frames) > 1 and frames[1] == "main.burn":
                            r["leaf"][side][0] += n

# OBI traces the hub received: trace id -> span names
spans = collections.defaultdict(set)
span_name = {}
tpath = os.path.join(out, "traces.jsonl")
if os.path.exists(tpath):
    for line in open(tpath):
        for rs in json.loads(line).get("resourceSpans", []):
            for ss in rs.get("scopeSpans", []):
                for sp in ss.get("spans", []):
                    spans[sp["traceId"]].add(sp.get("name", ""))
                    span_name[sp.get("spanId", "")] = sp.get("name", "")

for n in names:
    r = R[n]
    o = {"name": n, "rusage_cpu_s": r["rusage_cpu_s"]}
    if r["cpu_samples"]:
        o["ebpf_cpu_s"] = round(r["ebpf_cpu_s"], 2)
        o["ebpf_vs_rusage"] = round(r["ebpf_cpu_s"] / r["rusage_cpu_s"], 3) if r["rusage_cpu_s"] else None
        o["ebpf_types"] = dict(r["ebpf_types"])
        e = r["leaf"]["ebpf"]
        o["ebpf_leaf_caller_kept"] = f"{e[0]}/{e[1]}"
        o["ebpf_pct_cpu_linked"] = round(100 * r["linked"] / r["cpu_samples"], 1)
        if r["linked"] and spans:
            for h, ids in r["trace_ids"].items():
                known = sum(c for (t, _), c in ids.items() if t in spans)
                by_span = collections.Counter()
                for (_, sid), c in ids.items():
                    by_span[span_name.get(sid, "<span not in traces>")] += c
                o[f"obi_{h}"] = {"linked_samples": sum(ids.values()), "trace_found": known, "linked_span": dict(by_span.most_common(4))}
        if r["offcpu"]:
            tot = sum(r["offcpu"].values())
            o["offcpu_top"] = [(k, round(100 * v / tot, 1)) for k, v in r["offcpu"].most_common(5)]
    if r["otel_cpu_s"]:
        o["otel_cpu_s"] = round(r["otel_cpu_s"], 2)
        o["otel_vs_rusage"] = round(r["otel_cpu_s"] / r["rusage_cpu_s"], 3) if r["rusage_cpu_s"] else None
        t = r["leaf"]["otel"]
        o["otel_leaf_caller_kept"] = f"{t[0]}/{t[1]}"
        o["otel_inlined_funcs"] = len(r["otel_inlined"])
        o["otel_pct_cpu_linked"] = round(100 * r["otel_linked"] / max(r["otel_samples"], 1), 1)
        if r["otel_linked"] and spans:
            for h, ids in r["otel_trace_ids"].items():
                by_span = collections.Counter()
                for (_, sid), c in ids.items():
                    by_span[span_name.get(sid, "<span not in traces>")] += c
                o[f"otel_link_{h}"] = dict(by_span.most_common(3))
    print(json.dumps(o))
# Inlined functions otelprofile showed that the eBPF stacks of the same
# process also contain.
for n in names:
    if R[n]["otel_inlined"] and R[n]["ebpf_funcs"]:
        print(json.dumps({"name": n, "otel_inlined_funcs": len(R[n]["otel_inlined"]), "of_which_in_ebpf_stacks": len(R[n]["otel_inlined"] & R[n]["ebpf_funcs"])}))
