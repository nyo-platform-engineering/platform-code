#!/usr/bin/env python3
"""Verify Problem 2: concurrency, sustained load and PVMBG outage recovery.
Run after starting the base + client Compose stack with PVMBG min=max=3000ms.
"""
from concurrent.futures import ThreadPoolExecutor
import json
import math
import subprocess
import threading
import time

from test_helpers import IntegrationTest, ROOT, require, run_test


class Problem2Test(IntegrationTest):
    def page(self, source, limit=1, evidence=None):
        if time.monotonic() >= self.renew_at:
            self.session = self.refresh(self.session)
            self.renew_at = time.monotonic() + self.session['expires_in'] - 5
        page = self.call('client', f'/hazards?source={source}&limit={limit}', token=self.session['access_token'], evidence=evidence)
        require(page.get('items') and all(h['source'] == source for h in page['items']), 'Missing/wrong source data: ' + source)
        require(len(page.get('sources', [])) == 1 and page['sources'][0]['source'] == source,
                'Missing/wrong freshness metadata: ' + source)
        return page

    def run(self):
        baseline = self.preflight()
        config = json.loads(self.cmd(['docker', 'inspect', baseline['pvmbg']['id']]))[0]['Config']['Env']
        require('PVMBG_DELAY_MIN_MS=3000' in config and 'PVMBG_DELAY_MAX_MS=3000' in config,
                'Set PVMBG_DELAY_MIN_MS=PVMBG_DELAY_MAX_MS=3000 in .env and recreate only PVMBG before test')
        started = time.perf_counter()
        self.call('pvmbg', '/volcanic-reports', token=self.env['PVMBG_TOKEN'], evidence='PVMBG delay measurement')
        elapsed = (time.perf_counter() - started) * 1000
        require(elapsed >= 2900, 'PVMBG runtime delay was not approximately 3 seconds')
        self.show('PVMBG configured and observed delay', {'delay_min_ms': 3000, 'delay_max_ms': 3000, 'observed_ms': round(elapsed, 3)})
        self.passed('pvmbg_runtime_delay_3s', {'observed_ms': elapsed})
        self.session = self.login('analyst')
        self.renew_at = time.monotonic() + self.session['expires_in'] - 5
        self.page('BMKG')
        self.page('PVMBG')
        samples = {'BMKG': [], 'PVMBG': []}
        with ThreadPoolExecutor(max_workers=2) as pool:
            for _ in range(self.args.samples):
                # Refresh before paired requests to avoid rotating a shared token mid-request.
                if time.monotonic() >= self.renew_at:
                    self.session = self.refresh(self.session)
                    self.renew_at = time.monotonic() + self.session['expires_in'] - 5
                token = self.session['access_token']
                barrier = threading.Barrier(2)

                def timed(source):
                    barrier.wait(timeout=5)
                    start = time.perf_counter()
                    page = self.call('client', f'/hazards?source={source}&limit=1', token=token)
                    require(page.get('items') and all(h['source'] == source for h in page['items']),
                            'Paired request returned missing/wrong data')
                    return (time.perf_counter() - start) * 1000

                fast, slow = pool.submit(timed, 'BMKG'), pool.submit(timed, 'PVMBG')
                samples['BMKG'].append(fast.result())
                samples['PVMBG'].append(slow.result())
        p95 = sorted(samples['BMKG'])[math.ceil(len(samples['BMKG']) * .95) - 1]
        require(p95 < 300, f'BMKG-only p95={p95:.2f} ms must be <300 ms')
        self.show('Paired latency measurement', {'pairs': len(samples['BMKG']), 'bmkg_p95_ms': round(p95, 3), 'required_p95_below_ms': 300})
        self.passed('bmkg_only_p95_under_300ms_with_concurrent_pvmbg', {'p95_ms': p95, 'samples_ms': samples})

        print('[INFO] sustained k6 load: >=50 parallel connections, >=60 seconds', flush=True)
        load_dir = self.args.output_dir / 'load'
        code = subprocess.call([self.args.python, str(ROOT / 'scripts/load-test.py'),
            '--env-file', str(self.args.env_file), '--project', self.args.project,
            '--vus', str(self.args.vus), '--seconds', str(self.args.seconds),
            '--output-dir', str(load_dir)], cwd=ROOT)
        require(code == 0, 'Sustained load failed; inspect load/result.json and load/k6-console.txt')
        load = json.loads((load_dir / 'result.json').read_text())
        self.show('Load error rate and controlled rejection', {k: load[k] for k in ('requests', 'successful_requests', 'controlled_429', 'unexpected_failures', 'error_rate_excluding_429')})
        self.passed('sustained_load', {k: load[k] for k in ('requests', 'controlled_429',
            'error_rate_excluding_429', 'throughput_total_rps', 'throughput_successful_rps',
            'latency_all_responses_ms', 'latency_successful_responses_ms', 'criteria')})

        saved = self.statuses()
        saved_item = self.page('PVMBG')['items'][0]
        outage_attempted = False
        try:
            outage_attempted = True
            self.call('pvmbg', '/admin/outage', {'enabled': True}, token=self.env['PVMBG_TOKEN'], evidence='Enable PVMBG outage')
            self.call('pvmbg', '/volcanic-reports', token=self.env['PVMBG_TOKEN'], expected=(503,), evidence='PVMBG upstream during outage')
            self.wait('failed PVMBG polling', lambda: not self.statuses()['PVMBG']['healthy'])
            self.wait('BMKG polling advances during outage', lambda: self.statuses()['BMKG']['last_success_at'] > saved['BMKG']['last_success_at'])
            bmkg = self.page('BMKG', evidence='Seismic data during PVMBG outage')
            volcanic = self.page('PVMBG', 1000, evidence='Stored volcanic data during outage')
            require(bmkg['sources'][0]['status'] == 'fresh' and not bmkg['sources'][0]['stale'], 'BMKG degraded during PVMBG outage')
            meta = volcanic['sources'][0]
            require(meta['status'] == 'stale' and meta['stale'] and meta['last_success_at'], 'Volcanic freshness marker absent')
            require(saved_item in volcanic['items'], 'Previously stored volcanic record missing/changed during outage')
            self.show('Preserved volcanic record', {'hazard_id': saved_item['hazard_id'], 'record_unchanged': True})
            self.passed('outage_bmkg_fresh_and_canonical_volcanic_stale', {'sources': [bmkg['sources'][0], meta],
                        'preserved_hazard_id': saved_item['hazard_id']})
        finally:
            if outage_attempted:
                self.call('pvmbg', '/admin/outage', {'enabled': False}, token=self.env['PVMBG_TOKEN'], evidence='Disable PVMBG outage')
                print('[INFO] PVMBG outage disabled (cleanup)', flush=True)
        self.wait('automatic PVMBG recovery', lambda: self.statuses()['PVMBG']['healthy'] and
                  self.statuses()['PVMBG']['last_success_at'] > saved['PVMBG']['last_success_at'])
        require(self.page('PVMBG', evidence='Volcanic data after recovery')['sources'][0]['status'] == 'fresh', 'PVMBG not fresh after recovery')
        after = self.snapshot()
        require(after == baseline, 'Container restarted/replaced during Problem 2')
        containers = {name: {'id_before': baseline[name]['id'][:12], 'id_after': after[name]['id'][:12],
                             'restart_count_before': baseline[name]['restart_count'],
                             'restart_count_after': after[name]['restart_count'],
                             'started_at_unchanged': baseline[name]['started_at'] == after[name]['started_at']}
                      for name in baseline}
        self.show('Containers before and after outage', containers)
        self.passed('automatic_recovery_without_restart', {'containers': containers})



if __name__ == '__main__':
    run_test('2', Problem2Test, __doc__)
