#!/usr/bin/env python3
import json
import pathlib
import sys

out = pathlib.Path(sys.argv[1])
quality = json.loads((out / 'quality.json').read_text())
backend = json.loads((out / 'backend.json').read_text())
prefix = (out / 'prefix').read_text().strip()
assert quality['otel-default-pprof']['totals'] == quality['otel-default-otlp']['totals'], 'pprof/OTLP rows differ'
requests = set()
for spec in ('otel-default', 'grafana-default', 'grafana-all'):
    raw = quality[spec + '-pprof']
    requests.add(raw['requests'])
    cpu = raw['totals']['cpu/nanoseconds']
    assert cpu['sum_values'] > 0 and cpu['linked_values'] > 0
    assert cpu['spinLCG_values'] > 0
    assert cpu['spinLCG_values'] == cpu['spinLCG_with_burn_values'], 'planted caller missing'
    assert cpu['unmatched_link_values'] == 0, 'link does not resolve to an emitted span'
    heap = raw['totals']['inuse_space/bytes']
    assert heap['sum_values'] > 0 and heap['linked_values'] == 0
    stored = backend['queries'][prefix + '-' + spec]
    assert stored['all']['cpu_ns'] == cpu['sum_values'], 'stored CPU differs from raw profile'
    assert stored['route_label']['cpu_ns'] == cpu['route_values'], 'dynamic route label differs'
    assert stored['heap_snapshot_sum']['bytes'] == heap['sum_values'], 'stored heap differs from raw profiles'
    if spec == 'grafana-default':
        assert cpu['child_values'] == 0 and stored['first50_children']['cpu_ns'] == 0
    else:
        assert cpu['child_values'] > 0 and stored['first50_children']['cpu_ns'] > 0
assert len(requests) == 1 and next(iter(requests)) > 0, 'quality probes did different work'
uploads = [json.loads(line) for line in (out / 'uploads/uploads.jsonl').read_text().splitlines()]
assert uploads and all(u.get('status') == 200 for u in uploads), 'failed profile upload'
print(json.dumps({'verdict': 'verified', 'profile_uploads': len(uploads),
                  'quality_requests_per_configuration': next(iter(requests)),
                  'scope': 'observed stacks, stored values, dynamic label, resolvable IDs, and backend span filtering; not temporal attribution or overhead significance'}, indent=2))
