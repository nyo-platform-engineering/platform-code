"""Criterion 1: verify every practice critical delivery against durable receipts.

Run first after the source-scoped reset, before the outage changes the scenario.
The practice profile (seed 42, speed 60) emits three critical readings by 150 s.
No critical reading or notification is silently skipped on subsequent runs.
"""
import math
import time

from common import output, parse_time, require
from helper import critical_deliveries, receipt


def critical_alerts(run):
    last_report = [float('-inf')]
    def delivered():
        deliveries = critical_deliveries(run)
        if time.monotonic() - last_report[0] >= 10:
            output('WAIT', 'Critical scenario', f'{len(deliveries)}/3 new critical deliveries; waiting for all three receipts')
            last_report[0] = time.monotonic()
        require(len(deliveries) <= 3, 'practice scenario emitted more than three critical deliveries')
        if len(deliveries) != 3:
            return None
        require(len({d['hazard_id'] for d in deliveries}) == 3,
                'practice critical readings have duplicate identities')
        require(all(parse_time(d['ingested_at']) >= parse_time(run.sensor_reset_at) for d in deliveries),
                'critical delivery predates this run reset')
        receipts = [receipt(run, d) for d in deliveries]
        return receipts if all(receipts) else None

    records = run.wait('all 3 practice critical readings and their durable Field Team receipts',
                       delivered, run.config.wait_critical_seconds)
    run.save('critical-receipts.json', records)
    latencies = [(parse_time(r['received_at']) - parse_time(r['ingested_at'])).total_seconds() * 1000
                 for r in records]
    require(all(ms >= 0 for ms in latencies), 'negative alert latency; verify node clock synchronization')
    worst = sorted(latencies)[math.ceil(0.95 * len(latencies)) - 1]
    require(worst <= run.config.alert_p95_ms,
            f'critical alert p95 {worst:.1f} ms exceeds {run.config.alert_p95_ms:.0f} ms')
    return {'critical_readings': 3, 'alerted': len(records), 'lost': 0,
            'p95_ms': round(worst, 2), 'max_ms': round(max(latencies), 2),
            'evidence': 'durable critical_field_alert effects; original NATS persistence timestamps'}


def run_group(run):
    input('Enter → verify all 3 new critical readings reach the Field Team (up to 150s for the scenario): ')
    alerts = run.stage('critical-alerts-delivered', critical_alerts, run)
    output('RESULT', 'Critical lane', f"{alerts['alerted']}/3 alerted, 0 lost",
           f"p95 {alerts['p95_ms']} ms (target {run.config.alert_p95_ms:.0f} ms)")
