#!/usr/bin/env python3
import json
import pathlib
import statistics
import sys

root = pathlib.Path(sys.argv[1])
records = [json.loads(p.read_text()) for p in sorted(root.glob('cost-*.json'))]
summary = {}
for mode in ('none', 'otel', 'grafana'):
    runs = [r for r in records if r['mode'] == mode]
    if len(runs) < 5:
        raise SystemExit(f'need five runs for {mode}')
    result = {'n': len(runs)}
    for key in ('cpu_s', 'peak_rss_bytes', 'allocated_bytes', 'requests', 'duration_s'):
        values = [r[key] for r in runs]
        result[key] = {'median': statistics.median(values), 'min': min(values), 'max': max(values)}
    summary[mode] = result
requests = {r['requests'] for r in records}
if len(requests) != 1:
    raise SystemExit(f'work differs across runs: {requests}')
uploads = [json.loads(s) for s in (root / 'uploads/uploads.jsonl').read_text().splitlines()]
errors = [u for u in uploads if u.get('status') != 200]
summary['uploads'] = {'total': len(uploads), 'errors': errors}
if errors:
    raise SystemExit(f'upload failures: {errors}')
summary['verdict'] = 'shared-host measurements; no significance or universal overhead claim'
print(json.dumps(summary, indent=2))
