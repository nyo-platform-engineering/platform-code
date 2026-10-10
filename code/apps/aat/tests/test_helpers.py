"""Shared HTTP, configuration, Docker and evidence helpers for integration tests."""
import argparse
from datetime import datetime, timezone
import json
import os
import sys
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
SUMMARY = {'hazard_id', 'source', 'hazard_type', 'severity', 'area_name', 'occurred_at', 'ingested_at'}
RAW = {'source_ref_id', 'latitude', 'longitude', 'attributes'}
SECRET_KEYS = ('BMKG_API_KEY', 'PVMBG_TOKEN', 'AGGREGATOR_TOKEN', 'POSTGRES_PASSWORD',
               'AUTH_INTERNAL_TOKEN', 'PUBLIC_CLIENT_SECRET', 'RESPONDER_CLIENT_SECRET',
               'ANALYST_CLIENT_SECRET')
SERVICES = ('bmkg', 'pvmbg', 'aggregator', 'postgres', 'nats', 'auth', 'client',
            'dashboard-updater', 'field-notifier')


def dotenv(path):
    values = {}
    for line in path.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            values[key.strip()] = value.strip().strip('\"\'')
    return values


class IntegrationTest:
    def __init__(self, args):
        self.args = args
        self.env = dotenv(args.env_file)
        for key in SECRET_KEYS:
            require(self.env.get(key), 'Missing credential variable: ' + key)
        self.urls = {name: 'http://127.0.0.1:' + self.env.get(key, port)
                     for name, key, port in (('bmkg', 'BMKG_PORT', '8081'),
                         ('pvmbg', 'PVMBG_PORT', '8082'), ('aggregator', 'AGGREGATOR_PORT', '8083'),
                         ('auth', 'AUTH_PORT', '8084'), ('client', 'CLIENT_PORT', '8080'))}
        self.compose = ['docker', 'compose', '-p', args.project, '--env-file', str(args.env_file),
                        '-f', 'compose.yaml', '-f', 'compose.client.yaml']
        self.correlation = f'p{args.problem}-' + uuid.uuid4().hex
        self.proof = {'problem': args.problem, 'correlation_id': self.correlation, 'checks': {}, 'http_results': []}
        self.redacted_values = set(self.env[key] for key in SECRET_KEYS)
        self.shown_summaries = set()
        self.session = None

    def cmd(self, argv):
        try:
            return subprocess.check_output(argv, cwd=ROOT, text=True, stderr=subprocess.PIPE)
        except FileNotFoundError:
            raise RuntimeError(f'{argv[0]} is not installed or is not on PATH') from None
        except subprocess.CalledProcessError as exc:
            detail = exc.stderr or 'No diagnostic output'
            # Redact credentials from command errors before printing them.
            for key in SECRET_KEYS:
                value = self.env.get(key)
                if value:
                    detail = detail.replace(value, '[REDACTED]')
            detail = detail.strip()[-2000:]
            command = 'docker compose' if argv[:2] == ['docker', 'compose'] else argv[0]
            raise RuntimeError(f'{command} failed (exit {exc.returncode}): {detail}') from None
        except OSError as exc:
            raise RuntimeError(f'{argv[0]} could not run: {type(exc).__name__}') from None

    def clean(self, data):
        if isinstance(data, str):
            for value in sorted(self.redacted_values, key=len, reverse=True):
                if value:
                    data = data.replace(value, '[REDACTED]')
            return data
        if isinstance(data, dict):
            return {self.clean(key): self.clean(value) for key, value in data.items()}
        if isinstance(data, list):
            return [self.clean(value) for value in data]
        return data

    def show(self, label, data):
        print('[INFO] ' + label + '\n' + json.dumps(self.clean(data), indent=2, ensure_ascii=False), flush=True)

    def call(self, service, path, body=None, token=None, headers=None, expected=(200,), evidence=None):
        headers = dict(headers or {})
        headers['X-Correlation-ID'] = self.correlation
        if token:
            headers['Authorization'] = 'Bearer ' + token
        data = None if body is None else json.dumps(body).encode()
        if data is not None:
            headers['Content-Type'] = 'application/json'
        req = urllib.request.Request(self.urls[service] + path, data=data, headers=headers)
        started_at = datetime.now(timezone.utc).isoformat()
        started = time.perf_counter()
        received_correlation = None
        try:
            with urllib.request.urlopen(req, timeout=self.args.request_timeout) as response:
                status, raw = response.status, response.read()
                received_correlation = response.headers.get('X-Correlation-ID')
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
            received_correlation = error.headers.get('X-Correlation-ID')
        except (urllib.error.URLError, TimeoutError):
            raise RuntimeError(f'{service} {path}: network failure or client timeout') from None
        elapsed = (time.perf_counter() - started) * 1000
        record = None
        if evidence:
            record = {'label': evidence, 'started_at_utc': started_at, 'service': service,
                      'method': req.get_method(), 'path': path, 'status': status,
                      'expected_statuses': list(expected), 'duration_ms': round(elapsed, 3),
                      'request_correlation_id': self.correlation,
                      'response_correlation_id': received_correlation}
            self.proof['http_results'].append(record)
            print(f'[HTTP] {evidence}: {req.get_method()} {service}{path} -> HTTP {status} ({elapsed:.2f} ms)', flush=True)
        require(status in expected, f'{service} {path}: HTTP {status}, expected {expected}')
        try:
            payload = json.loads(raw)
        except ValueError:
            raise RuntimeError(f'{service} {path}: invalid JSON') from None
        if isinstance(payload, dict):
            for key in ('access_token', 'refresh_token'):
                if isinstance(payload.get(key), str):
                    self.redacted_values.add(payload[key])
            if record is not None:
                summary = {}
                if isinstance(payload.get('items'), list):
                    summary['item_count'] = len(payload['items'])
                    summary['received_fields'] = sorted({key for item in payload['items'] if isinstance(item, dict) for key in item})
                    if payload['items'] and isinstance(payload['items'][0], dict):
                        summary['example_item'] = {key: value for key, value in payload['items'][0].items() if key in SUMMARY}
                if 'sources' in payload:
                    summary['sources'] = payload['sources']
                for key in ('expires_in', 'refresh_expires_in', 'token_type', 'outage'):
                    if key in payload:
                        summary[key] = payload[key]
                if summary:
                    record['response_summary'] = summary
                    if evidence not in self.shown_summaries:
                        self.show(evidence + ' response', summary)
                        self.shown_summaries.add(evidence)
        return payload

    def login(self, identity):
        pair = self.call('auth', '/auth/token', {'client_id': identity,
                         'client_secret': self.env[identity.upper() + '_CLIENT_SECRET']}, evidence='Login ' + identity)
        require(pair.get('access_token') and pair.get('refresh_token') and pair.get('expires_in', 0) > 0,
                'Invalid token response')
        return pair

    def refresh(self, pair, evidence=None):
        return self.call('auth', '/auth/refresh', {'refresh_token': pair['refresh_token']}, evidence=evidence)

    def statuses(self):
        page = self.call('aggregator', '/internal/source-status', token=self.env['AGGREGATOR_TOKEN'])
        return {s['source']: s for s in page['sources']}

    def wait(self, description, check):
        deadline = time.monotonic() + self.args.timeout
        while time.monotonic() < deadline:
            result = check()
            if result:
                return result
            time.sleep(.5)
        raise RuntimeError('Timed out: ' + description)

    def snapshot(self):
        ids = self.cmd(self.compose + ['ps', '-a', '-q', *SERVICES]).split()
        require(ids, 'No containers; start base + client overlay first')
        state = {}
        for item in json.loads(self.cmd(['docker', 'inspect', *ids])):
            name = item['Config']['Labels']['com.docker.compose.service']
            state[name] = {'id': item['Id'], 'running': item['State']['Running'],
                           'restart_count': item['RestartCount'], 'started_at': item['State']['StartedAt'],
                           'oom_killed': item['State']['OOMKilled']}
        require(all(s in state and state[s]['running'] and not state[s]['oom_killed'] for s in SERVICES),
                'All nine services must be running without OOM')
        return state

    def passed(self, name, evidence=True):
        self.proof['checks'][name] = evidence
        print('[PASS] ' + name, flush=True)

    def preflight(self):
        baseline = self.snapshot()
        for service in self.urls:
            require(self.call(service, '/health').get('ok'), service + ' health failed')
        self.wait('healthy source polling', lambda: all(self.statuses().get(s, {}).get('healthy') for s in ('BMKG', 'PVMBG')))
        self.passed('running_stack_and_healthy_sources')
        return baseline



def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run_test(problem, test_class, description):
    parser = argparse.ArgumentParser(description=description)
    parser.add_argument('--env-file', type=Path, default=ROOT / os.environ.get('ENV_FILE', '.env'))
    parser.add_argument('--project', default=os.environ.get('COMPOSE_PROJECT_NAME') or os.environ.get('PROJECT', 'aat-part1'))
    parser.add_argument('--output-dir', type=Path)
    parser.add_argument('--timeout', type=float, default=os.environ.get('TIMEOUT_SECS', '90'))
    parser.add_argument('--request-timeout', type=float, default=os.environ.get('CURL_TIMEOUT_SECS', '10'))
    if problem == '2':
        parser.add_argument('--samples', type=int, default=os.environ.get('LATENCY_SAMPLES', '20'))
        parser.add_argument('--vus', type=int, default=os.environ.get('LOAD_VUS', '50'))
        parser.add_argument('--seconds', type=int, default=os.environ.get('LOAD_SECONDS', '65'))
    args = parser.parse_args()
    args.problem = problem
    args.python = sys.executable
    args.env_file = args.env_file.resolve()
    if args.timeout <= 0 or args.request_timeout <= 3:
        parser.error('Require positive timeout and request timeout >3s')
    if problem == '2' and (args.samples < 20 or args.vus < 50 or args.seconds < 60):
        parser.error('Require samples >=20, VUs >=50 and seconds >=60')
    args.output_dir = (args.output_dir or ROOT / 'docs/evidence' / f'problem-{problem}-{uuid.uuid4().hex[:12]}').resolve()
    if args.output_dir.exists():
        parser.error('Output directory already exists; choose a fresh --output-dir')
    args.output_dir.mkdir(parents=True, exist_ok=False)
    test = None
    error = None
    try:
        test = test_class(args)
        print('[INFO] correlation_id=' + test.correlation, flush=True)
        test.run()
    except (OSError, RuntimeError, ValueError, KeyError, KeyboardInterrupt) as exc:
        # Only print our own diagnostic messages; other exceptions may contain secrets.
        error = str(exc) if isinstance(exc, RuntimeError) else type(exc).__name__
        print('[FAIL] ' + error, flush=True)
    proof = test.proof if test else {'problem': problem, 'checks': {}}
    proof.update({'passed': error is None, 'failure': error})
    if test:
        proof = test.clean(proof)
    (args.output_dir / 'result.json').write_text(json.dumps(proof, indent=2) + '\n')
    print(f"[INFO] evidence: {args.output_dir}")
    print(f"Problem {problem} summary: {len(proof['checks'])} passed, {int(error is not None)} failed")
    raise SystemExit(0 if error is None else 1)
