"""Exercise interruption recovery and preservation of unrelated configuration."""
from contextlib import contextmanager
import importlib.util
import json
from pathlib import Path
import tempfile
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    'freeze_polling', Path(__file__).resolve().parents[1] / 'scripts/tester/freeze-polling.py')
freeze = importlib.util.module_from_spec(spec)
spec.loader.exec_module(freeze)


class PollingFreezeTests(unittest.TestCase):
    def test_restore_preserves_other_changes_and_value_from(self):
        original = [{'name': 'BMKG_URL', 'valueFrom': {'configMapKeyRef': {'name': 'urls', 'key': 'bmkg'}}}]
        current = [{'name': 'NEW_SETTING', 'value': 'keep'}] + [
            {'name': name, 'value': value} for name, value in freeze.URLS.items()]
        self.assertEqual(freeze.restore_env(current, original), [current[0]] + original)

    def test_unmodified_urls_do_not_restart_deployment(self):
        original = [{'name': 'PVMBG_URL', 'value': 'http://original:8082'}]
        current = original + [{'name': 'OTHER', 'value': 'keep'}]
        self.assertEqual(freeze.restore_env(current, original), current)

    def test_external_url_edits_are_not_overwritten(self):
        for current in ([{'name': 'BMKG_URL', 'value': 'http://changed'}],
                        [{'name': 'BMKG_URL', 'value': freeze.URLS['BMKG_URL']}]):
            with self.subTest(current=current), self.assertRaisesRegex(RuntimeError, 'externally'):
                freeze.restore_env(current, [])

    def test_failed_pause_restores_from_journal_before_returning(self):
        deployment = {'metadata': {'uid': 'deployment', 'resourceVersion': '1'}, 'spec': {'template': {
            'spec': {'containers': [{'name': 'aggregator', 'env': []}]}}}}
        app = {'metadata': {'uid': 'app'}, 'spec': {'syncPolicy': {'automated': {'selfHeal': True}}}}
        def kube(*args, **kwargs):
            return deployment if args[1] == 'deployment' else app
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'journal.json'
            restored = []
            def restore(journal):
                restored.append(json.loads(journal.read_text()))
                journal.unlink()
            with patch.object(freeze, 'kube', side_effect=kube), \
                 patch.object(freeze, 'patch_app', side_effect=RuntimeError('pause failed')), \
                 patch.object(freeze, 'resume', side_effect=restore):
                with self.assertRaisesRegex(RuntimeError, 'pause failed'):
                    freeze.freeze(path)
            self.assertEqual(restored[0]['deployment_uid'], 'deployment')
            self.assertEqual(len(restored[0]['apps']), len(freeze.APPS))
            self.assertFalse(path.exists())

    def test_interrupt_or_failed_drain_restores_after_url_change(self):
        deployment = {'metadata': {'uid': 'deployment', 'resourceVersion': '1'}, 'spec': {'template': {
            'spec': {'containers': [{'name': 'aggregator', 'env': []}]}}}}
        app = {'metadata': {'uid': 'app'}, 'spec': {}}
        for interrupted in (False, True):
            with self.subTest(interrupted=interrupted), tempfile.TemporaryDirectory() as folder:
                path = Path(folder) / 'journal.json'
                def kube(*args, **kwargs):
                    return deployment if args[1] == 'deployment' else app
                with patch.object(freeze, 'kube', side_effect=kube), \
                     patch.object(freeze, 'patch_app'), patch.object(freeze, 'set_env') as changed, \
                     patch.object(freeze, 'wait_for_inbox', side_effect=None if interrupted else RuntimeError('drain failed')), \
                     patch('builtins.input', side_effect=KeyboardInterrupt), \
                     patch.object(freeze, 'resume') as restored:
                    with self.assertRaises(KeyboardInterrupt if interrupted else RuntimeError):
                        freeze.freeze(path)
                    changed.assert_called_once()
                    restored.assert_called_once_with(path)

    def test_failed_restoration_keeps_recovery_journal(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'journal.json'
            path.write_text(json.dumps({'deployment_uid': 'old'}))
            with patch.object(freeze, 'kube', return_value={'metadata': {'uid': 'replacement'}}):
                with self.assertRaisesRegex(RuntimeError, 'replaced'):
                    freeze.resume(path)
            self.assertTrue(path.exists())


class P1CheckpointFreezeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        m2 = Path(__file__).resolve().parents[2] / 'apps/aat/tests/m2'
        sys.path.insert(0, str(m2))
        spec = importlib.util.spec_from_file_location('p1_freeze_cluster', m2 / 'p1/helper/cluster.py')
        cls.cluster = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.cluster)

    def make_run(self, events):
        return SimpleNamespace(
            active_monitors=0, mock_polling_frozen=False, report={},
            config=SimpleNamespace(kubeconfig=self.cluster.INFRA / '.state/kubeconfig',
                                   ensure_endpoint=lambda _: events.append('connect')),
            save=lambda *_: None,
        )

    def test_automatic_checkpoint_resumes_without_extra_prompt(self):
        events = []
        run = self.make_run(events)
        @contextmanager
        def paused():
            events.append('pause')
            try:
                yield
            finally:
                events.append('resume')
        with tempfile.TemporaryDirectory() as folder:
            backend = SimpleNamespace(paused_polling=paused, STATE=Path(folder) / 'journal')
            with patch.object(self.cluster, 'polling_freeze_module', return_value=backend), \
                 patch('builtins.input', side_effect=AssertionError('unexpected prompt')):
                with self.cluster.frozen_mock_checkpoint(run, 'global-reads'):
                    self.assertTrue(run.mock_polling_frozen)
                    events.append('check')
        self.assertEqual(events, ['pause', 'connect', 'check', 'resume', 'connect'])
        self.assertFalse(run.mock_polling_frozen)
        self.assertTrue(run.report['polling_freezes'][0]['restored'])

    def test_failed_checkpoint_still_resumes(self):
        events = []
        run = self.make_run(events)
        @contextmanager
        def paused():
            try:
                yield
            finally:
                events.append('resume')
        with tempfile.TemporaryDirectory() as folder:
            backend = SimpleNamespace(paused_polling=paused, STATE=Path(folder) / 'journal')
            with patch.object(self.cluster, 'polling_freeze_module', return_value=backend):
                with self.assertRaisesRegex(ValueError, 'comparison failed'):
                    with self.cluster.frozen_mock_checkpoint(run, 'dashboard-analytics'):
                        raise ValueError('comparison failed')
        self.assertIn('resume', events)
        self.assertFalse(run.mock_polling_frozen)
        record = run.report['polling_freezes'][0]
        self.assertEqual(record['status'], 'failed')
        self.assertTrue(record['restored'])

    def test_live_monitor_cannot_be_frozen(self):
        run = self.make_run([])
        run.active_monitors = 1
        with patch.object(self.cluster, 'polling_freeze_module') as loader:
            with self.assertRaisesRegex(ValueError, 'load/recovery monitor'):
                with self.cluster.frozen_mock_checkpoint(run, 'invalid'):
                    self.fail('entered freeze')
            loader.assert_not_called()


if __name__ == '__main__':
    unittest.main()
