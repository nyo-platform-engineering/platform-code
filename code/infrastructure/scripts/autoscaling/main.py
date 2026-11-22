#!/usr/bin/env python3
"""Explicit KEDA toggle for the existing Aggregator; default deployment stays fixed."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
NS = 'sistem-informasi-kebencanaan-bnpb'
NAME = 'aggregator-cpu'
HPA = 'keda-hpa-aggregator-cpu'
OWNER = 'aat.platform.local/managed-by'
MANIFEST = ROOT / 'autoscaling/aggregator-cpu.yaml'


def require(ok, reason):
    if not ok:
        raise ValueError(reason)


def kube(*args, namespace=NS, data=False):
    command = ['kubectl', '--kubeconfig', os.environ.get('KUBECONFIG', str(ROOT / '.state/kubeconfig')),
               '-n', namespace, '--request-timeout=20s', *args]
    result = subprocess.run(command, text=True, capture_output=True, timeout=35)
    require(result.returncode == 0, result.stderr.strip()[-600:] or 'kubectl failed')
    return json.loads(result.stdout) if data and result.stdout.strip() else result.stdout.strip()


def optional(kind, name):
    return kube('get', kind, name, '--ignore-not-found', '-o', 'json', data=True) or None


def hpas():
    return [h for h in kube('get', 'hpa', '-o', 'json', data=True)['items']
            if h['spec']['scaleTargetRef'].get('name') == 'aggregator']


def owned(scaler):
    require(scaler['metadata'].get('labels', {}).get(OWNER) == 'autoscaling-task',
            'existing aggregator-cpu is not owned by this toggle; refusing to modify it')
    require(scaler['spec']['scaleTargetRef']['name'] == 'aggregator', 'unexpected scaler target')


def guard():
    # M2 suites control replicas/source configuration; never change their live setup.
    for name in ('platform', 'dev-apps', 'aat-aggregator-dev'):
        app = kube('get', 'application', name, '-o', 'json', namespace='argocd', data=True)
        require(app['metadata'].get('annotations', {}).get('argocd.argoproj.io/skip-reconcile') != 'true',
                f'{name} reconciliation is paused, possibly by an M2 test; finish/restore that test first')
        require(not app.get('operation') and app.get('status', {}).get('operationState', {}).get('phase') not in ('Running', 'Terminating'),
                f'{name} is syncing; wait until it finishes')
        if name == 'aat-aggregator-dev':
            require(any('/spec/replicas' in r.get('jsonPointers', []) for r in app['spec'].get('ignoreDifferences', []))
                    and 'RespectIgnoreDifferences=true' in app['spec'].get('syncPolicy', {}).get('syncOptions', []),
                    'Aggregator Argo Application must ignore /spec/replicas to avoid fighting KEDA')
    dep = kube('get', 'deployment', 'aggregator', '-o', 'json', data=True)
    require(any(c['name'] == 'aggregator' for c in dep['spec']['template']['spec']['containers']),
            'Aggregator container is missing')
    for container in dep['spec']['template']['spec']['containers']:
        if container['name'] == 'aggregator':
            require(not any(e['name'] == 'P4_TEST_SCHEMA' and e.get('value') for e in container.get('env', [])),
                    'P4 verification is active; restore it before changing autoscaling')
            require(container.get('resources', {}).get('requests', {}).get('cpu'), 'Aggregator needs a CPU request')
    return dep


def enable():
    guard()
    kube('get', 'crd', 'scaledobjects.keda.sh')
    operator = kube('get', 'deployment', 'keda-operator', '-o', 'json', namespace='keda', data=True)
    require(operator.get('status', {}).get('readyReplicas', 0) > 0, 'KEDA operator is not Ready; sync the keda Argo application first')
    existing = optional('scaledobject', NAME)
    if existing:
        owned(existing)
    scalers = kube('get', 'scaledobjects', '-o', 'json', data=True)['items']
    require(not any(s['metadata']['name'] != NAME and s['spec']['scaleTargetRef']['name'] == 'aggregator'
                    for s in scalers), 'another ScaledObject targets Aggregator')
    for hpa in hpas():
        require(hpa['metadata']['name'] == HPA and existing is not None
                and any(o.get('uid') == existing['metadata']['uid'] for o in hpa['metadata'].get('ownerReferences', [])),
                'another HPA controls Aggregator; disable the native HPA before enabling KEDA')
    metrics = kube('get', '--raw', f'/apis/metrics.k8s.io/v1beta1/namespaces/{NS}/pods', data=True)
    require(any(p['metadata']['name'].startswith('aggregator-') for p in metrics.get('items', [])),
            'metrics-server has no Aggregator CPU samples')
    kube('apply', '-f', str(MANIFEST))
    print('Autoscaling ON: KEDA CPU target 60%, replicas 3–5.')


def disable():
    guard()
    # Missing CRD means KEDA has not been installed; there is no ScaledObject to delete.
    crd = optional('crd', 'scaledobjects.keda.sh')
    existing = optional('scaledobject', NAME) if crd else None
    if existing:
        owned(existing)
    foreign = [h for h in hpas() if not existing or h['metadata']['name'] != HPA
               or not any(o.get('uid') == existing['metadata']['uid'] for o in h['metadata'].get('ownerReferences', []))]
    require(not foreign, 'another HPA controls Aggregator; refusing to scale under a foreign controller')
    if existing:
        kube('delete', 'scaledobject', NAME, '--wait=true', '--timeout=30s')
    deadline = time.monotonic() + 60
    while hpas():
        require(time.monotonic() < deadline, 'HPA still exists after scaler deletion; no manual scaling performed')
        time.sleep(2)
    kube('scale', 'deployment/aggregator', '--replicas=3')
    print('Autoscaling OFF: no Aggregator HPA; fixed replicas=3. Wait for Ready before M2 tests.')


def status():
    dep = kube('get', 'deployment', 'aggregator', '-o', 'json', data=True)
    crd = optional('crd', 'scaledobjects.keda.sh')
    scaler = optional('scaledobject', NAME) if crd else None
    return {'desired': dep['spec'].get('replicas'), 'ready': dep.get('status', {}).get('readyReplicas', 0),
            'scaledobject': scaler.get('spec') if scaler else None,
            'hpas': [{'name': h['metadata']['name'], 'spec': h['spec'], 'status': h.get('status', {})} for h in hpas()]}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('on', 'off', 'status'))
    args = parser.parse_args()
    try:
        if args.action == 'on': enable()
        elif args.action == 'off': disable()
        else: print(json.dumps(status(), indent=2))
    except (ValueError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr); sys.exit(1)
