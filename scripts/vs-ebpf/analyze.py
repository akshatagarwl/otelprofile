#!/usr/bin/env python3
"""Summarise the raw OTLP profiles the hub collector wrote (file exporter, JSON
lines): for the demo process, what each profiler sent.

    analyze.py profiles.jsonl [demo-executable-name] > summary.json
"""
import collections
import json
import sys

path = sys.argv[1]
exe = sys.argv[2] if len(sys.argv) > 2 else "demo-otel"


def val(v, st):
    if "stringValueStrindex" in v:
        return st[v["stringValueStrindex"]]
    if "stringValue" in v:
        return v["stringValue"]
    if "intValue" in v:
        return int(v["intValue"])
    if "boolValue" in v:
        return v["boolValue"]
    if "doubleValue" in v:
        return v["doubleValue"]
    return json.dumps(v)


def attrs(idx, d, st):
    out = {}
    for i in idx or []:
        a = d["attributeTable"][i]
        out[st[a.get("keyStrindex", 0)]] = val(a.get("value", {}), st)
    return out


def side_of(scope):
    return "ebpf" if "ebpf-profiler" in scope else "otelprofile"


# per side, per sample type
S = collections.defaultdict(lambda: {
    "samples": 0, "value": 0, "stacks": 0, "frames": 0, "inline_frames": 0,
    "unknown_frames": 0, "kernel_frames": 0, "labelled": 0, "linked": 0,
    "label_keys": collections.Counter(), "self": collections.Counter(),
    "spin_parent": collections.Counter(), "max_depth": 0, "resource_keys": set(),
    "unit": "", "first": None, "last": None,
})

for line in open(path):
    d = json.loads(line)
    dic = d["dictionary"]
    st = dic["stringTable"]
    for rp in d["resourceProfiles"]:
        res = {}
        for a in rp["resource"].get("attributes", []):
            k = st[a["keyStrindex"]] if "keyStrindex" in a else a.get("key")
            res[k] = val(a.get("value", {}), st)
        for sp in rp["scopeProfiles"]:
            side = side_of(sp.get("scope", {}).get("name", ""))
            for p in sp["profiles"]:
                stype = st[p["sampleType"].get("typeStrindex", 0)]
                unit = st[p["sampleType"].get("unitStrindex", 0)]
                period = int(p.get("period", 0) or 0)
                for s in p.get("samples", []):
                    sa = attrs(s.get("attributeIndices"), dic, st)
                    name = res.get("service.name") or ""
                    pexe = str(sa.get("process.executable.name", res.get("process.executable.name", "")))
                    if side == "ebpf":
                        if pexe != exe:
                            continue
                    elif name != "demo":
                        continue
                    key = (side, stype)
                    agg = S[key]
                    agg["unit"] = unit
                    agg["resource_keys"].update(res.keys())
                    n = len(s.get("timestampsUnixNano", [])) if "values" not in s else 1
                    v = sum(int(x) for x in s.get("values", [])) if "values" in s else n * period
                    agg["samples"] += n if "values" not in s else int(s["values"][0])
                    agg["value"] += v
                    agg["stacks"] += 1
                    for k in sa:
                        agg["label_keys"][k] += v
                    if "endpoint" in sa or "process.context.label.endpoint" in sa:
                        agg["labelled"] += v
                    if s.get("linkIndex") or "process.context.label.otel.span_id" in sa:
                        agg["linked"] += v
                    stack = dic["stackTable"][s.get("stackIndex", 0)]
                    names = []
                    for li in stack.get("locationIndices", []):
                        loc = dic["locationTable"][li]
                        lines = loc.get("lines", [])
                        la = attrs(loc.get("attributeIndices"), dic, st)
                        ftype = la.get("profile.frame.type", "")
                        if ftype == "kernel":
                            agg["kernel_frames"] += 1
                        if not lines:
                            agg["unknown_frames"] += 1
                            names.append("[unknown]")
                            agg["frames"] += 1
                            continue
                        for j, ln in enumerate(lines):
                            fn = dic["functionTable"][ln.get("functionIndex", 0)]
                            nm = st[fn.get("nameStrindex", 0)]
                            if not nm:
                                agg["unknown_frames"] += 1
                                nm = "[unknown]"
                            if j < len(lines) - 1:
                                agg["inline_frames"] += 1
                            names.append(nm)
                            agg["frames"] += 1
                    agg["max_depth"] = max(agg["max_depth"], len(names))
                    if names:
                        agg["self"][names[0]] += v
                        if names[0] == "main.spinLCG":
                            parent = next((x for x in names[1:] if x in ("main.queryDB", "main.renderPage", "main.fastHandler")), "other")
                            agg["spin_parent"][parent] += v

out = {}
for (side, stype), a in sorted(S.items()):
    tot = a["value"] or 1
    out[f"{side}:{stype}"] = {
        "unit": a["unit"], "samples": a["samples"], "value": a["value"], "distinct_stacks": a["stacks"],
        "frames_per_stack": round(a["frames"] / max(a["stacks"], 1), 1), "max_depth": a["max_depth"],
        "inline_frames": a["inline_frames"], "unknown_frames": a["unknown_frames"], "kernel_frames": a["kernel_frames"],
        "pct_with_endpoint_label": round(100 * a["labelled"] / tot, 1),
        "pct_linked_to_span": round(100 * a["linked"] / tot, 1),
        "sample_attribute_keys": sorted(a["label_keys"]),
        "resource_keys": sorted(a["resource_keys"]),
        "top_self_pct": [(k, round(100 * v / tot, 1)) for k, v in a["self"].most_common(8)],
        "spinLCG_by_caller_pct": {k: round(100 * v / max(sum(a["spin_parent"].values()), 1), 1) for k, v in a["spin_parent"].most_common()},
    }
json.dump(out, sys.stdout, indent=1)
print()
