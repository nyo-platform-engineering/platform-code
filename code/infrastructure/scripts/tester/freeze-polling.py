#!/usr/bin/env python3
"""Temporarily disconnect the two mock upstreams; retain a resumable journal."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state/polling-freeze.json'
NS = 'sistem-informasi-kebencanaan-bnpb'
APPS = ('platform', 'dev-apps', 'aat-aggregator-dev')
URLS = {'BMKG_URL': 'http://bmkg.polling-frozen.invalid:8081',
        'PVMBG_URL': 'http://pvmbg.polling-frozen.invalid:8082'}


def kube(*args, json_output=True):
    result = subprocess.run(['kubectl', '--kubeconfig', str(ROOT / '.state/kubeconfig'),
                             '--request-timeout=10s', *args], capture_output=True, text=True,
                            timeout=140)
    if result.returncode:
        raise RuntimeError('kubectl failed: ' + result.stderr.strip())
    return json.loads(result.stdout) if json_output else result.stdout


def patch_app(name, patch):
    kube('patch', 'application', name, '-n', 'argocd', '--type=merge', '-p',
         json.dumps(patch), json_output=False)


def container(deployment):
    return next(c for c in deployment['spec']['template']['spec']['containers']
                if c['name'] == 'aggregator')


def restore_env(current, original):
    """Restore only our URL entries, preserving unrelated concurrent env edits."""
    present = {v['name']: v for v in current if v['name'] in URLS}
    before = {v['name']: v for v in original}
    if present == before:
        return current
    frozen = {name: {'name': name, 'value': value} for name, value in URLS.items()}
    if present != frozen:
        raise RuntimeError('Mock URLs changed externally; refusing to overwrite them')
    return [v for v in current if v['name'] not in URLS] + original


def set_env(deployment, env):
    index = next(i for i, c in enumerate(deployment['spec']['template']['spec']['containers'])
                 if c['name'] == 'aggregator')
    labels = deployment['spec']['selector'].get('matchLabels', {})
    if not labels or deployment['spec']['selector'].get('matchExpressions'):
        raise RuntimeError('Unsupported Aggregator selector; cannot verify old pollers stopped')
    selector = ','.join(f'{key}={value}' for key, value in sorted(labels.items()))
    old_pods = {pod['metadata']['uid'] for pod in
                kube('get', 'pods', '-n', NS, '-l', selector, '-o', 'json')['items']}
    patch = [
        {'op': 'test', 'path': '/metadata/uid', 'value': deployment['metadata']['uid']},
        {'op': 'test', 'path': '/metadata/resourceVersion', 'value': deployment['metadata']['resourceVersion']},
        {'op': 'add', 'path': f'/spec/template/spec/containers/{index}/env', 'value': env},
    ]
    kube('patch', 'deployment', 'aggregator', '-n', NS, '--type=json', '-p',
         json.dumps(patch), json_output=False)
    print('Waiting for Aggregator rollout...', flush=True)
    kube('rollout', 'status', 'deployment/aggregator', '-n', NS, '--timeout=120s', json_output=False)
    deadline = time.monotonic() + 120
    while True:
        remaining = {pod['metadata']['uid'] for pod in
                     kube('get', 'pods', '-n', NS, '-l', selector, '-o', 'json')['items']}
        if not old_pods.intersection(remaining):
            break
        if time.monotonic() >= deadline:
            raise RuntimeError('Old Aggregator pods have not stopped; freeze is not established')
        time.sleep(2)


def resume(path=STATE):
    if not path.exists():
        print('No saved polling freeze to resume.', flush=True)
        return
    state = json.loads(path.read_text())
    deployment = kube('get', 'deployment', 'aggregator', '-n', NS, '-o', 'json')
    if deployment['metadata']['uid'] != state['deployment_uid']:
        raise RuntimeError('Aggregator deployment was replaced; journal retained for manual recovery')
    current = container(deployment).get('env', [])
    env = restore_env(current, state['original_urls'])
    if env != current:
        set_env(deployment, env)
    else:
        kube('rollout', 'status', 'deployment/aggregator', '-n', NS, '--timeout=120s', json_output=False)
    # Restore children first, then their parent Applications.
    for app in reversed(state['apps']):
        patch_app(app['name'], {
            'metadata': {'uid': app['uid'], 'annotations': {'argocd.argoproj.io/skip-reconcile': app['skip']}},
            'spec': {'syncPolicy': {'automated': app['automated']}},
        })
    path.unlink()
    print('BMKG/PVMBG polling URLs and previous Argo settings restored.', flush=True)


def wait_for_inbox():
    # Poll loops drain their durable inbox before attempting the disconnected URL.
    query = "SELECT count(*) FROM inbox_messages WHERE source IN ('BMKG','PVMBG') AND status = 'retrying'"
    command = 'exec psql -h /tmp -U "$PATRONI_SUPERUSER_USERNAME" -d "$PATRONI_CITUS_DATABASE" -At -v ON_ERROR_STOP=1 -c "$1"'
    deadline, last = time.monotonic() + 180, None
    while True:
        pending = int(kube('exec', 'citus-g0-aat-control-0', '-n', NS, '-c', 'postgres',
                           '--', 'sh', '-c', command, 'polling-freeze', query, json_output=False).strip())
        if pending != last:
            print(f'Queued BMKG/PVMBG inbox records: {pending}', flush=True)
            last = pending
        if pending == 0:
            return
        if time.monotonic() >= deadline:
            raise RuntimeError('Mock inbox did not drain; resuming polling instead of claiming a freeze')
        time.sleep(2)


@contextmanager
def paused_polling(path=STATE):
    if path.exists():
        raise RuntimeError('A freeze journal already exists; run task polling:resume first')
    deployment = kube('get', 'deployment', 'aggregator', '-n', NS, '-o', 'json')
    apps = []
    for name in APPS:
        app = kube('get', 'application', name, '-n', 'argocd', '-o', 'json')
        apps.append({'name': name, 'uid': app['metadata']['uid'],
                     'skip': app['metadata'].get('annotations', {}).get('argocd.argoproj.io/skip-reconcile'),
                     'automated': app.get('spec', {}).get('syncPolicy', {}).get('automated')})
    state = {'deployment_uid': deployment['metadata']['uid'], 'apps': apps,
             'original_urls': [e for e in container(deployment).get('env', []) if e['name'] in URLS]}
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('x') as stream:
        os.fchmod(stream.fileno(), 0o600)
        json.dump(state, stream, indent=2)
        stream.write('\n')
    try:
        for app in apps:
            print(f"Pausing Argo reconciliation: {app['name']}", flush=True)
            patch_app(app['name'], {'spec': {'syncPolicy': {'automated': None}}})
            deadline = time.monotonic() + 120
            while True:
                status = kube('get', 'application', app['name'], '-n', 'argocd', '-o', 'json')
                if not status.get('operation') and status.get('status', {}).get('operationState', {}).get('phase') not in ('Running', 'Terminating'):
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError(f"Argo sync did not finish: {app['name']}")
                time.sleep(2)
            patch_app(app['name'], {'metadata': {'annotations': {'argocd.argoproj.io/skip-reconcile': 'true'}}})
        deployment = kube('get', 'deployment', 'aggregator', '-n', NS, '-o', 'json')
        env = [e for e in container(deployment).get('env', []) if e['name'] not in URLS]
        env.extend({'name': name, 'value': value} for name, value in URLS.items())
        set_env(deployment, env)
        wait_for_inbox()
        print('\nBMKG/PVMBG upstream fetches are disconnected on all Aggregators.\n'
              '  Mock inbox drained; verify matching before/after checksums during your checkpoint.\n'
              '  Direct ingest requests, satellite, and coastal ingestion remain active.\n'
              '  Mock pods keep running; source-status/polling errors are expected.\n'
              '  Use this only for quiet checkpoints, separately from polling/failover/load tests.\n', flush=True)
        yield
    finally:
        resume(path)


def freeze(path=STATE):
    with paused_polling(path):
        input('Press Enter to restore polling (Ctrl+C also restores it): ')


def interrupted(*_):
    raise KeyboardInterrupt


def main():
    signal.signal(signal.SIGTERM, interrupted)
    try:
        if sys.argv[1:] == ['freeze']:
            if not sys.stdin.isatty():
                raise RuntimeError('Run task polling:freeze in an interactive terminal')
            freeze()
        elif sys.argv[1:] == ['resume']:
            resume()
        else:
            raise RuntimeError('Usage: freeze-polling.py freeze|resume')
    except (Exception, KeyboardInterrupt) as error:
        print(str(error) or 'Interrupted; polling restoration attempted.', file=sys.stderr)
        if STATE.exists():
            print('Recovery journal retained. Run task polling:resume.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
