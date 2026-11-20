"""P5 reset cleanup and complete-scenario notification proof."""
import base64
from contextlib import nullcontext
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

P5 = Path(__file__).resolve().parent / 'm2/p5'
sys.path.insert(0, str(P5.parent))
from common import Run
spec = importlib.util.spec_from_file_location('p5_helper', P5 / 'helper.py')
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
# Avoid colliding with P1/P2 helpers during test discovery.
with patch.dict(sys.modules, {'helper': helper}):
    spec = importlib.util.spec_from_file_location('p5_critical', P5 / 'group4_critical.py')
    critical = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(critical)


def delivery():
    return {'event_key': 'haz-1-2026-11-20T00:00:00Z', 'hazard_id': 'haz-1',
            'ingested_at': '2026-11-20T00:00:00+00:00'}


class ReceiptTests(unittest.TestCase):
    def test_receipt_matches_delivery_and_preserves_original_time(self):
        d = delivery()
        key = helper.hashlib.sha256((d['event_key'] + ':critical').encode()).hexdigest()
        effect = {'delivery_id': key, 'kind': 'critical_field_alert', 'hazard': {
            'source': 'SENSOR_PANTAI', 'hazard_id': d['hazard_id'], 'ingested_at': '2026-11-20T00:00:00Z'}}
        response = {'message': {'time': '2026-11-20T00:00:01Z',
                               'data': base64.b64encode(json.dumps(effect).encode()).decode()}}
        run = SimpleNamespace(url=lambda _: 'http://127.0.0.1:4222')
        with patch.object(helper, 'nats_request', return_value=response):
            result = helper.receipt(run, d)
            self.assertEqual(result['received_at'], '2026-11-20T00:00:01Z')
            effect['hazard']['ingested_at'] = '2026-11-19T00:00:00Z'
            response['message']['data'] = base64.b64encode(json.dumps(effect).encode()).decode()
            with self.assertRaisesRegex(ValueError, 'does not match'):
                helper.receipt(run, d)

    def test_published_outbox_without_payload_joins_exact_version(self):
        h = {'hazard_id': 'haz-1', 'ingested_at': '2026-11-20T00:00:00+00:00'}
        keys = ['haz-1-2026-11-19T00:00:00Z', delivery()['event_key']]
        with patch.object(helper, 'sql', return_value=[[h], keys]):
            self.assertEqual(helper.critical_deliveries(None), [dict(h, event_key=keys[1])])

    def test_nats_framing_handles_ping_and_reads_json(self):
        payload = json.dumps({'message': {'time': 'original'}}).encode()
        stream = io.BytesIO(b'INFO {}\r\nPING\r\nMSG _INBOX.test 1 ' + str(len(payload)).encode()
                            + b'\r\n' + payload + b'\r\n')
        sock = unittest.mock.MagicMock()
        sock.__enter__.return_value = sock
        sock.makefile.return_value = nullcontext(stream)
        with patch.object(helper.socket, 'create_connection', return_value=sock):
            self.assertEqual(helper.nats_request(('localhost', 4222), '$JS.API.TEST', {}),
                             {'message': {'time': 'original'}})
        self.assertEqual(sock.sendall.call_args_list[-1].args[0], b'PONG\r\n')


class CriticalTests(unittest.TestCase):
    def run_scenario(self, count=3, missing=False, latency=1):
        rows = [dict(delivery(), hazard_id=f'haz-{i}') for i in range(count)]
        def wait(description, check, timeout):
            result = check()
            if not result:
                raise ValueError('timed out: ' + description)
            return result
        run = SimpleNamespace(wait=wait, config=SimpleNamespace(wait_critical_seconds=300, alert_p95_ms=5000),
                              sensor_reset_at='2026-11-19T23:59:59Z', save=lambda *args: None)
        def received(_, d):
            return None if missing else dict(d, received_at=f'2026-11-20T00:00:{latency:02d}Z')
        with patch.object(critical, 'critical_deliveries', return_value=rows), patch.object(critical, 'receipt', side_effect=received):
            return critical.critical_alerts(run)

    def test_all_three_receipts_are_required(self):
        self.assertEqual(self.run_scenario()['alerted'], 3)
        for count, missing in ((0, False), (1, False), (2, False), (3, True)):
            with self.subTest(count=count, missing=missing), self.assertRaisesRegex(ValueError, 'timed out'):
                self.run_scenario(count, missing)

    def test_slow_receipt_fails_latency_requirement(self):
        with self.assertRaisesRegex(ValueError, 'exceeds'):
            self.run_scenario(latency=6)


class SetupTests(unittest.TestCase):
    def test_reset_stops_writers_and_resumes_processing_before_sensor(self):
        calls = []
        run = SimpleNamespace(forwards=SimpleNamespace(close=lambda name: calls.append(('close', name))),
                              save=lambda *args: None)
        with patch.object(helper, 'scale', side_effect=lambda _, name, count: calls.append((name, count))), \
             patch.object(helper, 'reset_database', side_effect=lambda *args: calls.append(('sql',)) or
                 {'reset_at': 'now', 'hazard_events_deleted': 3}):
            helper.reset_sensor(run, {'aggregator': 3, 'sensor-pantai': 1})
        self.assertEqual(calls, [('sensor-pantai', 0), ('aggregator', 0), ('sql',),
                                 ('aggregator', 3), ('close', 'aggregator'), ('sensor-pantai', 1)])

    def test_setup_rejects_exam_profile_before_mutations(self):
        run = SimpleNamespace(cluster=SimpleNamespace(kubectl=lambda *args, **kw: {'data': {
            'PANTAI_PROFILE': 'exam', 'PANTAI_SEED': '42', 'PANTAI_SPEED': '60'}}))
        with patch.object(helper, 'scale') as scale, self.assertRaisesRegex(ValueError, 'never reset the exam'):
            with helper.practice_run(run):
                self.fail('must not enter exam setup')
        scale.assert_not_called()

    def test_replica_and_argo_cleanup_runs_after_failed_reset(self):
        def kube(*args, **kwargs):
            if args[0] == 'rollout':
                return ''
            if args[1] == 'hpa':
                return {'items': []}
            if args[1] == 'configmap':
                return {'data': {'PANTAI_PROFILE': 'practice', 'PANTAI_SEED': '42', 'PANTAI_SPEED': '60'}}
            return {'spec': {'replicas': 3 if args[2] == 'aggregator' else 1}}
        def argo(_, *args, **kwargs):
            if args[0] == 'get':
                return {'metadata': {'annotations': {}}, 'spec': {'syncPolicy': {'automated': {'selfHeal': True}}}}
        run = SimpleNamespace(cluster=SimpleNamespace(kubectl=kube),
                              wait=lambda _, check, timeout: check(), save=lambda *args: None)
        with patch.object(helper, 'argo', side_effect=argo) as gitops, patch.object(helper, 'scale') as scale:
            with self.assertRaisesRegex(RuntimeError, 'reset failed'):
                with helper.practice_run(run):
                    raise RuntimeError('reset failed')
        self.assertEqual(scale.call_args_list, [unittest.mock.call(run, 'aggregator', 3),
                                              unittest.mock.call(run, 'sensor-pantai', 1)])
        restores = [c for c in gitops.call_args_list if c.args[1:3] == ('patch', 'application')
                    and 'annotations' in c.args[-1]]
        self.assertEqual(len(restores), 5)

    def test_skipped_check_is_not_reported_as_all_passed(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = Path(tmp) / 'runtime.env'; env.write_text('')
            run = Run(5, SimpleNamespace(env_file=env), [], tmp)
            run.report['automated_checks']['critical'] = {'status': 'skipped'}
            with patch('common.output') as output:
                run.finish(0)
            self.assertEqual(run.report['status'], 'automated_checks_incomplete')
            self.assertFalse(any(call.args[0] == 'PASS' for call in output.call_args_list))


if __name__ == '__main__':
    unittest.main()
