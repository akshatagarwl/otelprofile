#!/usr/bin/env python3
import argparse
import json
import pathlib
import time
import urllib.request
import urllib.error

parser = argparse.ArgumentParser()
parser.add_argument('out', type=pathlib.Path)
parser.add_argument('--backend', default='http://127.0.0.1:14040')
a = parser.parse_args()
prefix = (a.out / 'prefix').read_text().strip()
window = {'start': int((time.time() - 86400) * 1000), 'end': int(time.time() * 1000)}

def api(method, body):
    request = urllib.request.Request(a.backend + '/querier.v1.QuerierService/' + method,
                                    json.dumps(body).encode(), {'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)

series = api('Series', window)['labelsSet']
services = {}
for labels in series:
    values = {p['name']: p['value'] for p in labels['labels']}
    service = values.get('service_name', '')
    if service.startswith(prefix + '-'):
        services.setdefault(service, set()).add(values['__profile_type__'])
result = {'profile_types': {s: sorted(p) for s, p in services.items()}, 'queries': {}}
for spec in ('otel-default', 'grafana-default', 'grafana-all'):
    service = prefix + '-' + spec
    run = json.loads((a.out / ('quality-' + spec + '.json')).read_text())
    root_ids = [s['span'] for s in run['spans'] if s['name'] == 'request'][:50]
    child_ids = [s['span'] for s in run['spans'] if s['name'] == 'compute'][:50]
    trace_ids = [s['trace'] for s in run['spans'] if s['name'] == 'request'][:50]
    cpu_types = [p for p in services[service] if ':cpu:nanoseconds:' in p]
    preferred_cpu = 'process_cpu:cpu:nanoseconds:cpu:nanoseconds'
    if preferred_cpu not in cpu_types:
        raise RuntimeError(f'CPU types for {service}: {cpu_types}')
    common = {**window, 'profileTypeID': preferred_cpu, 'labelSelector': '{service_name="' + service + '"}'}
    variants = {'all': {}, 'first50_roots': {'spanSelector': root_ids},
                'first50_children': {'spanSelector': child_ids}, 'first50_traces': {'traceIdSelector': trace_ids},
                'route_label': {'labelSelector': '{service_name="' + service + '",route="/work"}'}}
    result['queries'][service] = {}
    for name, filters in variants.items():
        try:
            data = api('SelectMergeStacktraces', {**common, **filters})
            flame = data.get('flamegraph', {})
            result['queries'][service][name] = {'cpu_ns': int(flame.get('total', '0')), 'functions': flame.get('names', [])}
        except urllib.error.HTTPError as error:
            result['queries'][service][name] = {'status': error.code, 'error': error.read().decode()}
            if name == 'all':
                raise
    if result['queries'][service]['all']['cpu_ns'] <= 0:
        raise RuntimeError(f'no stored CPU for {service}')
    heap_types = [p for p in services[service] if ':inuse_space:bytes:' in p]
    if len(heap_types) != 1:
        raise RuntimeError(f'heap types for {service}: {heap_types}')
    data = api('SelectMergeStacktraces', {**common, 'profileTypeID': heap_types[0]})
    flame = data.get('flamegraph', {})
    result['queries'][service]['heap_snapshot_sum'] = {'bytes': int(flame.get('total', '0')), 'functions': flame.get('names', [])}
    if 'main.allocate' not in flame.get('names', []):
        raise RuntimeError(f'planted allocator missing in {service}')
print(json.dumps(result, indent=2))
