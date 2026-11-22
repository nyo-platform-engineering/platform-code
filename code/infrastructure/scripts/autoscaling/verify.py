#!/usr/bin/env python3
"""Measure actual CPU autoscaling under public Client load, then restore fixed replicas."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

import main as scaling



def cpu_utilizations(state):
    """An HPA can report null metrics while its first CPU sample is pending."""
    values = []
    for hpa in state['hpas']:
        for metric in (hpa.get('status') or {}).get('currentMetrics') or []:
            resource = metric.get('containerResource') or metric.get('resource') or {}
            if resource.get('name') == 'cpu':
                values.append((resource.get('current') or {}).get('averageUtilization'))
    return values


def idle_baseline(state):
    cpu = cpu_utilizations(state)
    return (state['desired'] == 3 and state['ready'] == 3 and bool(cpu)
            and all(value is not None and value < 60 for value in cpu))


def run(args):
    scaling.require(shutil.which('k6'), 'Install k6 before running this test')
    env = os.environ.copy()
    for line in args.env_file.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            env.setdefault(key.strip(), value.strip().strip('\"\''))
    scaling.require(env.get('RESPONDER_CLIENT_SECRET'), 'RESPONDER_CLIENT_SECRET missing')
    scaling.require(not scaling.hpas(), 'Start with autoscaling OFF; this test restores fixed replicas afterward')
    initial = scaling.guard()
    scaling.require(initial['spec']['replicas'] == 3 and initial.get('status', {}).get('readyReplicas') == 3,
                    'Start with all 3 fixed Aggregator replicas Ready')
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    output = scaling.ROOT / '.state/autoscaling' / stamp
    output.mkdir(parents=True, exist_ok=False)
    env.update(CLIENT_URL=args.client_url.rstrip('/'), AUTH_URL=args.auth_url.rstrip('/'),
               VUS=str(args.vus), HOLD=f'{args.hold}s')
    process = None
    result = {'passed': False, 'scaled_up': False, 'scaled_down': False}
    try:
        scaling.enable()
        deadline = time.monotonic() + 120
        while True:
            state = scaling.status()
            if idle_baseline(state):
                break
            scaling.require(time.monotonic() < deadline,
                            'No idle baseline at 3 replicas with CPU below 60%; stop background load first')
            print(f'[WAIT] Waiting for idle HPA baseline: desired={state["desired"]} '
                  f'ready={state["ready"]} CPU={cpu_utilizations(state) or "pending"}', flush=True)
            time.sleep(5)
        # Enable happens before the observation clock; no manual replica mutations
        # happen until after the verdict and evidence have been written.
        started = time.monotonic()
        with (output / 'load.log').open('w') as log, (output / 'samples.jsonl').open('w') as samples:
            process = subprocess.Popen(['k6', 'run', '--summary-export', str(output / 'load-summary.json'),
                                        str(Path(__file__).with_name('load.js'))], env=env, stdout=log, stderr=log)
            recovery_started = None
            while True:
                state = scaling.status()
                elapsed = round(time.monotonic() - started, 1)
                sample = {'elapsed_seconds': elapsed, **state}
                samples.write(json.dumps(sample) + '\n'); samples.flush()
                ready, desired = state['ready'], state['desired']
                running = process.poll() is None
                if running and desired > 3 and ready > 3:
                    result['scaled_up'] = True
                print(f'[WAIT] {elapsed}s load={running} desired={desired} ready={ready} '
                      f'CPU={cpu_utilizations(state) or "pending"}', flush=True)
                if not running:
                    if recovery_started is None:
                        recovery_started = time.monotonic()
                    if result['scaled_up'] and desired == 3 and ready == 3:
                        result['scaled_down'] = True
                        break
                    if time.monotonic() - recovery_started >= args.recovery:
                        break
                scaling.require(elapsed < args.hold + args.recovery + 120, 'Load process exceeded its deadline')
                time.sleep(5)
        result['load_exit_code'] = process.wait(timeout=10)
        result['passed'] = result['scaled_up'] and result['scaled_down'] and result['load_exit_code'] == 0
        scaling.require(result['passed'], 'Automatic scale-up/down or HTTP checks failed; inspect evidence. '
                        'CPU must actually exceed the target; 429/503 responses alone are not proof.')
    finally:
        if process and process.poll() is None:
            process.terminate()
            try: process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(timeout=10)
        # Record the automatic result BEFORE disabling KEDA/manual restoration.
        (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        print(f'Evidence: {output}', flush=True)
        scaling.disable()
    print('[PASS] Aggregator automatically scaled above 3 and returned to 3 before cleanup.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--client-url', default='http://192.168.56.10:30080')
    parser.add_argument('--auth-url', default='http://192.168.56.10:30084')
    parser.add_argument('--env-file', type=Path, default=scaling.ROOT / '.env.runtime')
    parser.add_argument('--vus', type=int, default=48)
    parser.add_argument('--hold', type=int, default=180)
    parser.add_argument('--recovery', type=int, default=300)
    args = parser.parse_args()
    try:
        scaling.require(args.vus > 0 and args.hold > 0 and args.recovery > 0, 'Durations and VUs must be positive')
        run(args)
    except (ValueError, OSError, subprocess.TimeoutExpired) as error:
        print(str(error), file=sys.stderr); sys.exit(1)
