"""Verify M1 polling, degradation, isolation and fan-out on a running Compose stack.
No sustained load test. Implementation assisted by Codex; see README.
Run on a dedicated demo project: this script toggles PVMBG and restarts services.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import datetime as dt
import json
import math
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--env-file', type=Path, default=ROOT / '.env')
parser.add_argument('--project', default='aat-part1')
parser.add_argument('--output', type=Path)
args = parser.parse_args()
args.env_file = args.env_file.resolve()
env = {}
for line in args.env_file.read_text().splitlines():
    if line.strip() and not line.lstrip().startswith('#') and '=' in line:
        key, value = line.split('=', 1)
        env[key.strip()] = value.strip().strip('\"\'')
if any(env.get(key) != '3000' for key in ('PVMBG_DELAY_MIN_MS', 'PVMBG_DELAY_MAX_MS')):
    parser.error('Set PVMBG_DELAY_MIN_MS=PVMBG_DELAY_MAX_MS=3000 and recreate PVMBG before this demo.')
urls = {name: f'http://127.0.0.1:{env.get(key, default)}' for name, key, default in (
    ('client', 'CLIENT_PORT', '8080'), ('auth', 'AUTH_PORT', '8084'),
    ('aggregator', 'AGGREGATOR_PORT', '8083'), ('pvmbg', 'PVMBG_PORT', '8082'))}
correlation = 'system-demo-' + uuid.uuid4().hex
compose = ['docker', 'compose', '--env-file', str(args.env_file), '-p', args.project,
           '-f', 'compose.yaml', '-f', 'compose.client.yaml', '-f', 'compose.consumer.yaml']
proof = {'correlation_id': correlation, 'load_testing': 'excluded', 'checks': {}}
pair = None


def command(*arguments):
    result = subprocess.run(compose + list(arguments), cwd=ROOT, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise RuntimeError(f'Compose {arguments[0]} failed: {result.stderr[-2000:]}')
    return result.stdout


def call(service, path, body=None, expected=200):
    global pair
    headers = {'X-Correlation-ID': correlation}
    if service in ('pvmbg', 'aggregator'):
        headers['Authorization'] = 'Bearer ' + env['PVMBG_TOKEN' if service == 'pvmbg' else 'AGGREGATOR_TOKEN']
    if service == 'client':
        headers['Authorization'] = 'Bearer ' + pair['access_token']
    data = None if body is None else json.dumps(body).encode()
    if data is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(urls[service] + path, data=data, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            status, raw, received = response.status, response.read(), response.headers.get('X-Correlation-ID')
    except urllib.error.HTTPError as error:
        status, raw, received = error.code, error.read(), error.headers.get('X-Correlation-ID')
    if service == 'client' and status == 401 and expected == 200:
        pair = call('auth', '/auth/refresh', {'refresh_token': pair['refresh_token']})
        return call(service, path, body, expected)
    if status != expected:
        raise RuntimeError(f'{service} {path}: HTTP {status}, expected {expected}')
    assert received == correlation, 'correlation ID not preserved'
    return json.loads(raw)


def wait_for(description, check, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = check()
        except urllib.error.URLError:
            result = None
        if result:
            return result
        time.sleep(0.5)
    raise RuntimeError('Timed out: ' + description)


def statuses():
    return {s['source']: s for s in call('aggregator', '/internal/source-status')['sources']}


def hazards(source):
    return call('aggregator', '/internal/hazards?source=' + source + '&limit=1000')['items']


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def entries(service, since):
    result = []
    for line in command('logs', '--no-log-prefix', '--since', since, service).splitlines():
        try:
            result.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return result


def container_ids():
    return {s: command('ps', '-a', '-q', s).strip() for s in (
        'bmkg', 'pvmbg', 'aggregator', 'postgres', 'nats', 'client', 'auth',
        'dashboard-updater', 'field-notifier')}


def matching(rows, predicate):
    return rows if predicate(rows) else None


def passed(name, evidence=True):
    proof['checks'][name] = evidence
    print('PASS ' + name, flush=True)


initial = container_ids()
assert all(initial.values()), 'Start base and client Compose overlays first.'
start = timestamp()
pair = call('auth', '/auth/token', {'client_id': 'analyst', 'client_secret': env['ANALYST_CLIENT_SECRET']})
try:
    call('pvmbg', '/admin/outage', {'enabled': False})
    call('pvmbg', '/admin/schema-version', {'enabled': False})
    wait_for('initial successful polling', lambda: all(s['healthy'] for s in statuses().values()))
    before = {s: len(hazards(s)) for s in ('BMKG', 'PVMBG')}
    wait_for('new events ingested automatically', lambda: all(len(hazards(s)) > before[s] for s in before))
    passed('automatic_polling_without_manual_ingest', before)
    wait_for('old volcanic records', lambda: any('confidence_level' not in h['attributes'] for h in hazards('PVMBG')))
    call('pvmbg', '/admin/schema-version', {'enabled': True})
    volcanic = wait_for('additive schema via polling', lambda: matching(hazards('PVMBG'), lambda rows: any('confidence_level' in h['attributes'] for h in rows)))
    assert any('confidence_level' not in h['attributes'] for h in volcanic)
    assert container_ids() == initial, 'schema change restarted a service'
    passed('schema_evolution_and_old_new_coexistence_without_restart')

    # Small paired latency experiment for P2.1; not the P2.2 sustained load test.
    def timed(source):
        started = time.perf_counter()
        page = call('client', '/hazards?source=' + source + '&limit=1')
        assert page['items']
        return (time.perf_counter() - started) * 1000
    # Refresh before sampling; no shared token rotation during paired requests.
    pair = call('auth', '/auth/refresh', {'refresh_token': pair['refresh_token']})
    samples = []
    with ThreadPoolExecutor(max_workers=2) as pool:
        for _ in range(20):
            fast = pool.submit(timed, 'BMKG')
            slow = pool.submit(timed, 'PVMBG')
            samples.append(fast.result())
            slow.result()
    p95 = sorted(samples)[math.ceil(len(samples) * .95) - 1]
    assert p95 < 300, f'BMKG p95={p95:.2f} ms, must be below 300'
    passed('bmkg_only_p95_with_3s_pvmbg', {'samples': len(samples), 'p95_ms': round(p95, 3), 'bmkg_ms': samples})

    saved = statuses()
    saved_ids = {h['hazard_id'] for h in hazards('PVMBG')}
    call('pvmbg', '/admin/outage', {'enabled': True})
    call('pvmbg', '/volcanic-reports', expected=503)
    wait_for('PVMBG failed poll', lambda: not statuses()['PVMBG']['healthy'])
    wait_for('independent BMKG progress', lambda: statuses()['BMKG']['last_success_at'] > saved['BMKG']['last_success_at'])
    state = statuses()
    assert state['BMKG']['healthy']
    failed_cursor = state['PVMBG']['last_success_at']
    for source in ('BMKG', 'PVMBG'):
        page = call('client', '/hazards?source=' + source + '&limit=1000')
        assert page['items']
        assert page['sources'][0]['stale'] == (source == 'PVMBG')
        if source == 'PVMBG':
            assert saved_ids <= {h['hazard_id'] for h in page['items']}
    time.sleep(3)
    assert statuses()['PVMBG']['last_success_at'] == failed_cursor, 'failed poll advanced cursor'
    call('pvmbg', '/admin/outage', {'enabled': False})
    wait_for('automatic recovery', lambda: statuses()['PVMBG']['healthy'])
    assert container_ids() == initial
    passed('outage_stale_data_independent_bmkg_and_recovery')

    wait_for('initial fan-out', lambda: all(any(e.get('hazard_id') for e in entries(s, start))
        for s in ('dashboard-updater', 'field-notifier')))
    command('stop', 'dashboard-updater')
    stopped = timestamp()
    published = wait_for('producer and notifier continue during consumer outage', lambda: (
        rows if (rows := entries('aggregator', stopped)) and
        any(e.get('msg') == 'hazard published' for e in rows) and
        any(e.get('hazard_id') for e in entries('field-notifier', stopped)) else None))
    missed = {e['event_key'][:36] for e in published if e.get('msg') == 'hazard published'}
    assert not any(e.get('hazard_id') for e in entries('dashboard-updater', stopped))
    command('start', 'dashboard-updater')
    replay = wait_for('stopped consumer catches up', lambda: matching(entries('dashboard-updater', stopped), lambda rows: missed <= {e.get('hazard_id') for e in rows}))
    passed('two_independent_consumers_and_durable_catchup', {'missed_hazards': sorted(missed)})
    command('up', '-d', '--build', '--no-deps', 'test-consumer')
    added = wait_for('new third subscriber', lambda: matching(entries('test-consumer', start), lambda rows: any(e.get('hazard_id') for e in rows)))
    assert any(e.get('hazard_id') for e in added)
    assert container_ids()['aggregator'] == initial['aggregator']
    passed('third_subscriber_without_producer_change')

    # Rebuild just PVMBG while verifying unrelated BMKG requests remain available.
    command('stop', 'pvmbg')
    assert call('client', '/hazards?source=BMKG&limit=1')['items']
    with ThreadPoolExecutor(max_workers=1) as pool:
        rebuilding = pool.submit(command, 'up', '-d', '--build', '--no-deps', 'pvmbg')
        reads = 0
        while not rebuilding.done():
            assert call('client', '/hazards?source=BMKG&limit=1')['items']
            reads += 1
            time.sleep(.2)
        rebuilding.result()
    wait_for('polling after rebuild', lambda: statuses()['PVMBG']['healthy'])
    current = container_ids()
    assert all(current[s] == initial[s] for s in initial if s != 'pvmbg')
    passed('independent_service_rebuild', {'bmkg_reads_during_rebuild': reads})

    # Match one producer correlation ID to the source HTTP request and both consumers.
    publications = [e for e in entries('aggregator', start) if e.get('msg') == 'hazard published']
    match = None
    for publication in publications:
        cid = publication.get('correlation_id')
        if cid and all(any(e.get('correlation_id') == cid for e in entries(s, start))
                       for s in ('bmkg', 'dashboard-updater', 'field-notifier')):
            match = cid
            break
    assert match, 'no source-to-consumer correlation trace'
    passed('correlation_id_source_to_broker_to_consumers', match)
finally:
    command('start', 'pvmbg', 'dashboard-updater')
    wait_for('PVMBG health after cleanup', lambda: call('pvmbg', '/health')['ok'])
    call('pvmbg', '/admin/outage', {'enabled': False})
    call('pvmbg', '/admin/schema-version', {'enabled': False})

proof['verified_at'] = timestamp()
if args.output:
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(proof, indent=2) + '\n')
print(json.dumps(proof, indent=2))
