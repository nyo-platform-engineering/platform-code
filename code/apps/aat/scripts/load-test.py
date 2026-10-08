"""Run sustained k6 M1 P2.2 traffic and independently verify container survival.
Implementation assisted by Codex; see README. Never writes secrets into results.
"""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import platform
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--env-file', type=Path, default=ROOT / '.env')
parser.add_argument('--project', default='aat-part1')
parser.add_argument('--vus', type=int, default=50)
parser.add_argument('--seconds', type=int, default=65)
parser.add_argument('--limit', type=int, default=100)
parser.add_argument('--output-dir', type=Path, default=ROOT / 'docs/evidence/load-normal')
args = parser.parse_args()
if args.vus < 50 or args.seconds < 60 or not 1 <= args.limit <= 1000:
    parser.error('Require >=50 VUs, >=60 seconds, and limit in 1..1000.')
args.env_file = args.env_file.resolve()
args.output_dir = args.output_dir.resolve()
args.output_dir.mkdir(parents=True, exist_ok=True)
values = {}
for line in args.env_file.read_text().splitlines():
    if line.strip() and not line.lstrip().startswith('#') and '=' in line:
        key, value = line.split('=', 1)
        values[key.strip()] = value.strip().strip('\"\'')
compose = ['docker', 'compose', '--env-file', str(args.env_file), '-p', args.project,
           '-f', 'compose.yaml', '-f', 'compose.client.yaml']
services = ['bmkg', 'pvmbg', 'aggregator', 'postgres', 'nats', 'auth', 'client',
            'dashboard-updater', 'field-notifier']
run_id = 'load-' + uuid.uuid4().hex
url = 'http://127.0.0.1:' + values.get('CLIENT_PORT', '8080')
auth_url = 'http://127.0.0.1:' + values.get('AUTH_PORT', '8084')
aggregator_url = 'http://127.0.0.1:' + values.get('AGGREGATOR_PORT', '8083')


def command(argv):
    return subprocess.check_output(argv, cwd=ROOT, text=True, stderr=subprocess.DEVNULL)


def snapshot():
    ids = command(compose + ['ps', '-q', *services]).split()
    if not ids:
        raise RuntimeError('No running containers; start the full Compose stack first.')
    raw = json.loads(command(['docker', 'inspect', *ids]))
    result = {}
    for item in raw:
        name = item['Config']['Labels']['com.docker.compose.service']
        result[name] = {'id': item['Id'], 'running': item['State']['Running'],
                        'restart_count': item['RestartCount'], 'oom_killed': item['State']['OOMKilled'],
                        'health': item['State'].get('Health', {}).get('Status', 'not_configured')}
    # ps -q omits stopped services: missing entries must count as failures too.
    return result


def tcp_connections(container_id):
    # Compose fixes the client listener at 8080 (hex 1F90); count inbound established sockets.
    program = 'awk \'$2 ~ /:1F90$/ && $4 == "01" {n++} END{print n+0}\' /proc/net/tcp /proc/net/tcp6'
    return int(command(['docker', 'exec', container_id, 'sh', '-c', program]).strip())


def get(path, token=None):
    headers = {'X-Correlation-ID': run_id}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(path, headers=headers)
    with urllib.request.urlopen(req, timeout=5) as response:
        return json.load(response)


def polling():
    return get(aggregator_url + '/internal/source-status', values['AGGREGATOR_TOKEN'])['sources']


version = command(['k6', 'version']).strip()
engine = json.loads(command(['docker', 'info', '--format', '{{json .}}']))
baseline = snapshot()
if any(s not in baseline or not baseline[s]['running'] for s in services):
    raise SystemExit('All nine required services must be running.')
for endpoint in (url, auth_url, aggregator_url):
    if not get(endpoint + '/health').get('ok'):
        raise SystemExit('Service health preflight failed.')
poll_before = polling()
if not all(s['healthy'] for s in poll_before):
    raise SystemExit('Wait for both source polls to become healthy before the normal-load test.')
config_keys = ('BMKG_DELAY_MS', 'PVMBG_DELAY_MIN_MS', 'PVMBG_DELAY_MAX_MS',
               'EVENT_INTERVAL_SECONDS', 'DB_MAX_CONNS', 'POLL_INTERVAL_SECONDS',
               'POLL_TIMEOUT_MS', 'ACCESS_TTL_SECONDS', 'AUTH_MAX_CONCURRENT',
               'CLIENT_MAX_CONCURRENT', 'UPSTREAM_TIMEOUT_MS', 'STALE_AFTER_SECONDS')
config = {key: values.get(key) for key in config_keys}
summary_path = args.output_dir / 'k6-summary.json'
# Do not reuse a previous summary if the new run fails before producing one.
if summary_path.exists():
    raise SystemExit('Output already exists; choose a fresh --output-dir to preserve previous evidence.')
environment = os.environ.copy()
environment.update({key: values[key] for key in ('PUBLIC_CLIENT_SECRET', 'RESPONDER_CLIENT_SECRET', 'ANALYST_CLIENT_SECRET')})
environment.update({'LOAD_CLIENT_URL': url, 'LOAD_AUTH_URL': auth_url,
                    'LOAD_VUS': str(args.vus), 'LOAD_SECONDS': str(args.seconds),
                    'LOAD_LIMIT': str(args.limit), 'LOAD_RUN_ID': run_id,
                    'LOAD_SUMMARY_PATH': str(summary_path)})
argv = ['k6', 'run', '--quiet', '--no-usage-report', str(ROOT / 'scripts/load/m1.js')]
started_at = dt.datetime.now(dt.timezone.utc).isoformat()
start = time.monotonic()
observations = []
print(f'{run_id}: {args.vus} VUs, {args.seconds}s sustained, page limit {args.limit}', flush=True)
with (args.output_dir / 'k6-console.txt').open('w') as console:
    process = subprocess.Popen(argv, cwd=ROOT, env=environment, stdout=console, stderr=subprocess.STDOUT)
    while process.poll() is None:
        elapsed = time.monotonic() - start
        try:
            state = snapshot()
            connections = tcp_connections(baseline['client']['id'])
            health = bool(get(url + '/health').get('ok'))
            observations.append({'elapsed_seconds': round(elapsed, 3), 'containers': state, 'client_health': health, 'inbound_tcp_connections': connections})
        except (subprocess.SubprocessError, urllib.error.URLError, ValueError) as exc:
            observations.append({'elapsed_seconds': round(elapsed, 3), 'monitor_error': type(exc).__name__})
        print(f'  {elapsed:.0f}s: client health={observations[-1].get("client_health", False)}', flush=True)
        # Short waits allow progress reports and interruptions throughout the run.
        for _ in range(10):
            if process.poll() is not None:
                break
            time.sleep(.5)
    code = process.wait()
finish = snapshot()
poll_after = polling()
crash_free = all(
    all(s in state and state[s]['running'] and not state[s]['oom_killed'] and
        state[s]['id'] == baseline[s]['id'] and
        state[s]['restart_count'] == baseline[s]['restart_count'] for s in services)
    for state in [finish] + [o.get('containers', {}) for o in observations])
health_ok = all(o.get('client_health', False) for o in observations)
raw = json.loads(summary_path.read_text()) if summary_path.exists() else {}
metrics = raw.get('metrics', {})

def count(name):
    return metrics.get(name, {}).get('values', {}).get('count', 0)


def trend(name):
    vals = metrics.get(name, {}).get('values', {})
    return {label: vals.get(key) for label, key in [('p50', 'p(50)'), ('p95', 'p(95)'), ('p99', 'p(99)')]}


requests = count('hazard_requests')
accepted = count('hazard_successes')
rejected = count('hazard_429')
failed = count('hazard_failures')
noncontrolled = requests - rejected
error_rate = failed / noncontrolled if noncontrolled else 1.0
windows = [{'start_second': i * 5, 'requests': count(f'hazard_requests{{window:{i}}}'),
            'successes': count(f'hazard_successes{{window:{i}}}'),
            'controlled_429': count(f'hazard_429{{window:{i}}}'),
            'failures': count(f'hazard_failures{{window:{i}}}')}
           for i in range((args.seconds + 4) // 5)]
sustained = all(w['requests'] >= 50 and w['successes'] > 0 for w in windows)
progress = all(any(t['source'] == s['source'] and t['healthy'] and t['last_success_at'] > s['last_success_at']
                   for t in poll_after) for s in poll_before)
result = {
    'run_id': run_id, 'started_at_utc': started_at,
    'finished_at_utc': dt.datetime.now(dt.timezone.utc).isoformat(),
    'tool': version, 'host': {'os': platform.system(), 'architecture': platform.machine()},
    'docker': {'version': engine['ServerVersion'], 'cpus': engine['NCPU'], 'memory_bytes': engine['MemTotal']},
    'configuration': config, 'workload': {'vus': args.vus, 'sustained_seconds': args.seconds,
        'page_limit': args.limit, 'sources': ['BMKG', 'PVMBG'], 'identities': ['public', 'responder', 'analyst'],
        'model': 'constant-vus, back-to-back HTTP requests with keep-alive, no think time',
        'actual_50_parallel_tcp_connections_observed': max((o.get('inbound_tcp_connections', 0) for o in observations), default=0) >= args.vus,
        'error_denominator': 'all hazard responses except valid controlled 429'},
    'requests': requests, 'successful_requests': accepted, 'controlled_429': rejected,
    'unexpected_failures': failed, 'error_rate_excluding_429': error_rate,
    'unexpected_error_rate_all_requests': failed / requests if requests else 1.0,
    'throughput_total_rps': requests / args.seconds,
    'throughput_successful_rps': accepted / args.seconds,
    'throughput_429_rps': rejected / args.seconds,
    'latency_all_responses_ms': trend('hazard_latency_ms'),
    'latency_successful_responses_ms': trend('hazard_success_latency_ms'),
    'http_status_counts': {str(s): count(f'hazard_requests{{status:{s}}}') for s in (0, 200, 400, 401, 403, 429, 500, 503)},
    'auth_refresh_successes': count('auth_refresh_successes'),
    'auth_refresh_429': count('auth_refresh_429'),
    'traffic_windows': windows, 'container_monitor': observations,
    'containers_before': baseline, 'containers_after': finish,
    'polling_before': poll_before, 'polling_after': poll_after,
    'criteria': {'k6_thresholds_pass': code == 0, 'sustained_traffic': sustained,
        'parallel_tcp_connections_observed': max((o.get('inbound_tcp_connections', 0) for o in observations), default=0) >= args.vus,
        'no_container_crash_or_restart': crash_free, 'client_health_during_load': health_ok,
        'polling_continued': progress, 'unexpected_error_rate_below_1_percent': error_rate < .01},
}
result['passed'] = all(result['criteria'].values())
(args.output_dir / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps({k: result[k] for k in ('passed', 'requests', 'successful_requests', 'controlled_429',
    'unexpected_failures', 'throughput_total_rps', 'throughput_successful_rps',
    'latency_all_responses_ms', 'latency_successful_responses_ms', 'criteria')}, indent=2))
raise SystemExit(0 if result['passed'] else 1)
