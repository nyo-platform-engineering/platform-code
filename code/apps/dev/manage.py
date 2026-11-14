#!/usr/bin/env python3
"""Small local process supervisor; requires only Python's standard library."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

APPS = Path(__file__).resolve().parents[1]
STATE = APPS / 'dev' / '.runtime'
FRONTEND = APPS / 'telemetry-ui/frontend'
COMPOSE = ['docker', 'compose', '-f', str(APPS / 'compose.yaml')]
STOP = False


def run(args, cwd=APPS, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, **kwargs)


def request(url, method='GET'):
    try:
        with urllib.request.urlopen(urllib.request.Request(url, method=method), timeout=6) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def wait_for(url, timeout=60, process=None, logfile=None):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline and not STOP:
        if process is not None and process.poll() is not None:
            raise RuntimeError(f'Process exited with code {process.returncode} before {url} was ready. See {logfile}')
        try:
            if request(url)[0] == 200:
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(.5)
    raise RuntimeError(f'Not ready after {timeout}s: {url}. Check {logfile or STATE} and docker compose logs.')


def resolve_pnpm():
    """Validate launchers in project context; avoid stale Corepack cache shims."""
    expected = json.loads((FRONTEND / 'package.json').read_text())['packageManager'].split('@', 1)[1]
    candidates = [Path(folder) / 'pnpm' for folder in os.get_exec_path()]
    if os.environ.get('PNPM_HOME'):
        candidates.append(Path(os.environ['PNPM_HOME']) / 'pnpm')
    candidates.extend([Path.home() / 'Library/pnpm/pnpm', Path.home() / '.local/share/pnpm/pnpm'])
    errors, seen = [], set()
    for candidate in candidates:
        candidate = candidate.absolute()
        if candidate in seen or not candidate.is_file() or not os.access(candidate, os.X_OK):
            continue
        seen.add(candidate)
        try:
            result = subprocess.run([str(candidate), '--version'], cwd=FRONTEND,
                                    capture_output=True, text=True, timeout=30)
            if result.returncode == 0 and result.stdout.strip() == expected:
                print(f'pnpm {expected}: {candidate}', flush=True)
                return str(candidate)
            detail = (result.stderr or result.stdout).strip()[-1500:]
            errors.append(f'{candidate}: {detail or "unexpected version"}')
        except (OSError, subprocess.TimeoutExpired) as error:
            errors.append(f'{candidate}: {error}')
    raise RuntimeError(f'No working pnpm {expected} launcher. Install the pinned version; see dev/README.md.\n' + '\n'.join(errors))


def prerequisites():
    for tool in ['docker', 'go']:
        if not shutil.which(tool):
            raise RuntimeError(f'Missing {tool}; see dev/README.md')
    pnpm = resolve_pnpm()
    run(COMPOSE + ['config', '--quiet'])
    run(['docker', 'info', '--format', '{{.ServerVersion}}'], stdout=subprocess.DEVNULL)
    return pnpm


def status():
    print(f'Workspace: {APPS}', flush=True)
    print('\nNative apps (running on your host):', flush=True)
    print(f'{"APP":<18} {"LISTENER PID":<15} {"HEALTH":<14} URL', flush=True)
    apps = [
        ('Go demo', 8081, '/demo', '/readyz', 'go-demo.log', 'go-demo'),
        ('Telemetry API', 8080, '/api/v1/meta', '/readyz', 'telemetry-api.log', 'telemetry-ui/backend'),
        ('Vite frontend', 5173, '/', '/', 'frontend.log', 'telemetry-ui/frontend'),
    ]
    for name, port, path, health, _, _ in apps:
        address = f'http://127.0.0.1:{port}'
        pid = 'unknown'
        if shutil.which('lsof'):
            listeners = subprocess.run(
                ['lsof', '-nP', f'-iTCP:{port}', '-sTCP:LISTEN', '-t'],
                capture_output=True, text=True, timeout=5,
            )
            pid = ','.join(dict.fromkeys(listeners.stdout.split())) or '—'
        try:
            health_status = f'HTTP {request(address + health)[0]}'
        except OSError:
            health_status = 'unavailable'
        print(f'{name:<18} {pid:<15} {health_status:<14} {address}{path}', flush=True)
    print('Listener PIDs identify the port owners; health checks do not establish process ownership.')
    print('\nSource and log locations:')
    for name, _, _, _, logfile, source in apps:
        print(f'  {name}: {APPS / source}\n    Log: {STATE / logfile}')
    print('\nDocker dependencies (inside the Docker runtime):', flush=True)
    if not shutil.which('docker'):
        print('Docker CLI is unavailable.')
        return
    context = subprocess.run(['docker', 'context', 'show'], capture_output=True, text=True, timeout=5)
    print(f'  Context: {context.stdout.strip() or "unknown"}')
    print('  Compose project: platform-apps-dev')
    print('  Collector OTLP: http://127.0.0.1:4318')
    print('  Collector health: http://127.0.0.1:13133/')
    print('  ClickHouse: http://127.0.0.1:8123 (HTTP), 127.0.0.1:9000 (native)', flush=True)
    result = subprocess.run(COMPOSE + ['ps', '--all'], cwd=APPS, timeout=15)
    if result.returncode:
        print('Docker status unavailable; the native app status above is still shown.')
    print('\nContainer logs: docker compose logs --tail=100 otel-collector clickhouse', flush=True)


def setup():
    pnpm = prerequisites()
    run(COMPOSE + ['pull'])
    run(COMPOSE + ['run', '--rm', '--no-deps', 'otel-collector', 'validate', '--config=/etc/otelcol-contrib/local.yaml'])
    for folder in ['go-demo', 'telemetry-ui/backend']:
        run(['go', 'mod', 'download'], APPS / folder)
    run([pnpm, 'install', '--frozen-lockfile'], FRONTEND)


def stop_process(process):
    if process.poll() is None:
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()


def fingerprint(folder):
    return tuple((str(path), path.stat().st_mtime_ns) for path in sorted(folder.rglob('*'))
                 if path.is_file() and (path.suffix == '.go' or path.name in ('go.mod', 'go.sum')))


def serve():
    global STOP
    pnpm = prerequisites()
    STATE.mkdir(exist_ok=True)
    lock = (STATE / 'lock').open('w')
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        raise RuntimeError('A task dev supervisor is already running.')
    for port in (8080, 8081, 5173):
        with socket.socket() as sock:
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                sock.bind(('127.0.0.1', port))
            except OSError:
                raise RuntimeError(f'Port {port} is occupied; no existing process was stopped.')
    def stop_handler(_signum, _frame):
        global STOP
        STOP = True
    signal.signal(signal.SIGTERM, stop_handler)
    signal.signal(signal.SIGINT, stop_handler)
    existing = set(run(COMPOSE + ['ps', '--services', '--status', 'running'], capture_output=True, text=True).stdout.split())
    owned = [name for name in ('otel-collector', 'clickhouse') if name not in existing]
    processes, logs, apps = [], [], []
    (STATE / 'supervisor.pid').write_text(str(os.getpid()))
    try:
        run(COMPOSE + ['up', '-d'])
        wait_for('http://127.0.0.1:13133/')
        for name, folder, port in [('go-demo', 'go-demo', '8081'), ('telemetry-api', 'telemetry-ui/backend', '8080')]:
            source = APPS / folder
            binary = STATE / name
            log = (STATE / f'{name}.log').open('a'); logs.append(log)
            run(['go', 'build', '-o', str(binary), '.'], source)
            env = dict(os.environ, PORT=port, LISTEN_HOST='127.0.0.1')
            # Query traffic is not exported by default; demo emits both signals.
            for key in list(env):
                if key.startswith('OTEL_'):
                    env.pop(key)
            if name == 'go-demo':
                env.update(OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:4318', OTEL_EXPORTER_OTLP_PROTOCOL='http/protobuf', OTEL_SERVICE_NAME='go-demo', OTEL_RESOURCE_ATTRIBUTES='deployment.environment.name=local,tenant.id=local')
            else:
                env.update(AUTH_MODE='local', CLICKHOUSE_ADDR='127.0.0.1:9000', CLICKHOUSE_USER='app', CLICKHOUSE_PASSWORD='local-clickhouse-app')
            process = subprocess.Popen([str(binary)], cwd=source, env=env, stdout=log, stderr=log, start_new_session=True)
            processes.append(process)
            apps.append(dict(source=source, binary=binary, env=env, log=log, process=process, fingerprint=fingerprint(source)))
            wait_for(f'http://127.0.0.1:{port}/readyz', process=process, logfile=log.name)
        log = (STATE / 'frontend.log').open('a'); logs.append(log)
        processes.append(subprocess.Popen([pnpm, 'dev', '--host', '127.0.0.1', '--strictPort'], cwd=FRONTEND, stdout=log, stderr=log, start_new_session=True))
        wait_for('http://127.0.0.1:5173/', process=processes[-1], logfile=log.name)
        print(f'Go demo: http://127.0.0.1:8081/demo\nTelemetry UI: http://127.0.0.1:5173\nLogs: {STATE}\nWatching Go sources; Ctrl-C stops owned processes and dependencies.', flush=True)
        while not STOP:
            for app in apps:
                current = fingerprint(app['source'])
                if current != app['fingerprint']:
                    app['fingerprint'] = current
                    print(f"Rebuilding {app['binary'].name}…", flush=True)
                    built = subprocess.run(['go', 'build', '-o', str(app['binary']) + '.next', '.'], cwd=app['source'], stdout=app['log'], stderr=app['log'])
                    if built.returncode:
                        print(f"Build failed; previous process kept running. See {app['binary'].name}.log", flush=True)
                        continue
                    stop_process(app['process'])
                    os.replace(str(app['binary']) + '.next', app['binary'])
                    app['process'] = subprocess.Popen([str(app['binary'])], cwd=app['source'], env=app['env'], stdout=app['log'], stderr=app['log'], start_new_session=True)
                    processes.append(app['process'])
                if app['process'].poll() is not None:
                    raise RuntimeError(f"{app['binary'].name} exited; see its log.")
            if processes[2].poll() is not None:
                raise RuntimeError('Vite exited; see frontend.log')
            time.sleep(.75)
    finally:
        for process in reversed(processes):
            stop_process(process)
        for log in logs:
            log.close()
        # Sequential stop lets the Collector drain before stopping ClickHouse.
        for service in owned:
            subprocess.run(COMPOSE + ['stop', service], cwd=APPS)
        (STATE / 'supervisor.pid').unlink(missing_ok=True)
        lock.close()


def stop():
    pidfile = STATE / 'supervisor.pid'
    if not pidfile.exists():
        print('No local app supervisor running. Dependencies can be stopped with docker compose down.')
        return
    pid = int(pidfile.read_text())
    command = subprocess.run(['ps', '-p', str(pid), '-o', 'command='], capture_output=True, text=True).stdout
    if str(Path(__file__).resolve()) not in command or ' run' not in command:
        raise RuntimeError('Stale PID file; refusing to signal an unrelated process.')
    os.kill(pid, signal.SIGTERM)
    for _ in range(100):
        if not pidfile.exists():
            return
        time.sleep(.5)
    raise RuntimeError('Shutdown still in progress; check supervisor output.')


def smoke():
    for action, expected in [('success', 200), ('slow', 200), ('error', 500)]:
        started = time.monotonic()
        code, raw = request(f'http://127.0.0.1:8081/demo/{action}', 'POST')
        assert code == expected, (action, code, raw)
        trace = json.loads(raw)['trace_id']
        assert len(trace) == 32 and trace != '0' * 32, trace
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            code, raw = request(f'http://127.0.0.1:8080/api/v1/traces/{trace}')
            spans = json.loads(raw).get('data', [])
            log_code, log_raw = request(f'http://127.0.0.1:8080/api/v1/logs?traceId={trace}')
            records = json.loads(log_raw).get('data', [])
            if code == log_code == 200 and len(spans) >= 2 and len(records) >= 2:
                ids = {span['spanId'] for span in spans}
                assert all(row['traceId'] == trace for row in spans + records)
                assert any(span['parentSpanId'] in ids for span in spans)
                assert all(row['spanId'] in ids for row in records)
                if action == 'error':
                    assert any(span['status'] == 'Error' for span in spans)
                    assert any(row['severity'] == 'error' for row in records)
                if action == 'slow':
                    assert max(span['durationMs'] for span in spans) >= 450
                print(f'{action}: {trace} — {len(spans)} spans, {len(records)} logs, visible in {time.monotonic()-started:.2f}s')
                break
            time.sleep(.3)
        else:
            raise RuntimeError(f'{action}: no correlated data within 15 seconds. API: {raw!r}; logs: {log_raw!r}')
    for path in ['traces/red', 'logs/volume', 'services']:
        code, raw = request('http://127.0.0.1:8080/api/v1/' + path)
        assert code == 200 and json.loads(raw)['data'], (path, raw)
    print('End-to-end telemetry smoke check passed.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['setup', 'run', 'stop', 'status', 'smoke', 'reset'])
    args = parser.parse_args()
    try:
        if args.command == 'setup': setup()
        elif args.command == 'run': serve()
        elif args.command == 'stop': stop()
        elif args.command == 'smoke': smoke()
        elif args.command == 'reset':
            stop()
            run(COMPOSE + ['down', '--volumes'])
        else: status()
    except (RuntimeError, subprocess.CalledProcessError, AssertionError) as error:
        print(f'Error: {error}', file=sys.stderr)
        sys.exit(1)
