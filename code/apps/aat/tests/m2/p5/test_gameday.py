"""Offline checks for the P5 game-day rehearsal: verdicts, SLO math, and that every
started fault is ended (newest first) even when a later one fails to start."""
import importlib.util
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

P5 = Path(__file__).resolve().parent
sys.path.insert(0, str(P5.parent))
spec = importlib.util.spec_from_file_location('p5_helper', P5 / 'helper.py')
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
with patch.dict(sys.modules, {'helper': helper}):
    spec = importlib.util.spec_from_file_location('p5_gameday', P5 / 'gameday.py')
    gameday = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(gameday)
    spec = importlib.util.spec_from_file_location('p5_entry', P5 / 'test-problem-5.py')
    entry = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {'gameday': gameday, **{name: __import__('types').ModuleType(name) for name in (
            'group1_alerts', 'group2_outage', 'group3_overload', 'group4_critical', 'group5_gameday')}}):
        spec.loader.exec_module(entry)


def sample(status, phase='faults', kind='field', ms=50, **extra):
    return {'status': status, 'phase': phase, 'kind': kind, 'ms': ms, **extra}


class FakeFault(gameday.Fault):
    def __init__(self, name, log, fail_inject=False, fail_revert=False, storage=False):
        self.name, self.log, self.fail_inject, self.fail_revert, self.storage = name, log, fail_inject, fail_revert, storage

    def inject(self, run):
        if self.fail_inject:
            raise ValueError('cannot start')
        self.log.append(('inject', self.name))

    def revert(self, run):
        if self.fail_revert:
            raise RuntimeError('cannot stop')
        self.log.append(('revert', self.name))


RUN = SimpleNamespace(clean=lambda s: s)


class AnswerTests(unittest.TestCase):
    def test_answers_and_non_answers(self):
        self.assertTrue(gameday.answered(sample(200)))
        self.assertTrue(gameday.answered(sample(429)))
        self.assertTrue(gameday.answered(sample(503, explicit_error=True)))
        for status in (0, 500, 502, 504):
            self.assertFalse(gameday.answered(sample(status)), status)
        self.assertFalse(gameday.answered(sample(503)), 'a bare 503 without an error body is not explicit')


class SloTests(unittest.TestCase):
    def test_error_budget(self):
        good = [sample(200)] * 990 + [sample(0)] * 10
        self.assertEqual(gameday.slo(good, .99)['error_budget_used'], 1.0)
        half = [sample(200)] * 995 + [sample(503, explicit_error=True)] * 5
        result = gameday.slo(half, .99)
        self.assertEqual((result['availability'], result['error_budget_used']), (.995, .5))

    def test_only_field_requests_count(self):
        self.assertEqual(gameday.slo([sample(0, kind='bmkg_only'), sample(200)], .99)['requests'], 1)


class VerdictTests(unittest.TestCase):
    config = SimpleNamespace(field_availability_slo=.99, other_source_p95_ms=300, recovery_timeout=300)

    def scenario(self, storage=False):
        log = []
        return gameday.Scenario('s', 'story', 60, [gameday.Step(0, FakeFault('f', log, storage=storage))])

    def test_clean_run_passes(self):
        samples = [sample(200), sample(200, kind='bmkg_only', ms=80), sample(200, phase='recovery')]
        result = gameday.verdict(self.scenario(), samples, 12.0, self.config)
        self.assertTrue(result['passed'], result['failures'])

    def test_each_failure_is_reported(self):
        samples = [sample(200)] * 50 + [sample(500), sample(0, phase='recovery'), sample(200, kind='bmkg_only', ms=900)]
        result = gameday.verdict(self.scenario(), samples, None, self.config)
        text = ' | '.join(result['failures'])
        self.assertFalse(result['passed'])
        for expected in ('got no answer', 'not fully healthy', 'below the', 'BMKG-only p95'):
            self.assertIn(expected, text)

    def test_storage_faults_do_not_assert_m1_latency(self):
        samples = [sample(200), sample(200, kind='bmkg_only', ms=900)]
        result = gameday.verdict(self.scenario(storage=True), samples, 5.0, self.config)
        self.assertTrue(result['passed'], result['failures'])


class ScheduleTests(unittest.TestCase):
    def play(self, faults):
        scenario = gameday.Scenario('s', 'story', 60, [gameday.Step(i, f) for i, f in enumerate(faults)])
        with patch.object(gameday.time, 'sleep'), patch.object(gameday, 'output'):
            active, error = gameday.inject_all(RUN, scenario, None, gameday.time.monotonic() + 100, [])
            errors = gameday.revert_all(RUN, active, [])
        return error, errors

    def test_all_faults_end_newest_first(self):
        log = []
        error, errors = self.play([FakeFault('a', log), FakeFault('b', log), FakeFault('c', log)])
        self.assertIsNone(error)
        self.assertEqual(errors, [])
        self.assertEqual(log, [('inject', 'a'), ('inject', 'b'), ('inject', 'c'),
                               ('revert', 'c'), ('revert', 'b'), ('revert', 'a')])

    def test_failed_start_still_ends_what_started(self):
        log = []
        error, _ = self.play([FakeFault('a', log), FakeFault('b', log, fail_inject=True), FakeFault('c', log)])
        self.assertIn('b: cannot start', error)
        self.assertEqual(log, [('inject', 'a'), ('revert', 'a')], 'c must not start; a must still end')

    def test_failed_revert_does_not_stop_the_others(self):
        log = []
        _, errors = self.play([FakeFault('a', log), FakeFault('b', log, fail_revert=True)])
        self.assertEqual(errors, ['b: cannot stop'])
        self.assertIn(('revert', 'a'), log)


class ScenarioTests(unittest.TestCase):
    def test_default_scenario_is_valid_and_short(self):
        from dataclasses import replace
        entry.validate_config(entry.CONFIG)
        combined = gameday.scenario(entry.CONFIG)
        self.assertEqual([s.fault.name for s in combined.steps],
                         ['media-flood', 'pvmbg-outage', 'sensor-unreachable', 'notifier-partition', 'node-down-aat-worker-2'])
        self.assertTrue(combined.storage, 'node loss means M1 latency is not asserted')
        self.assertFalse(gameday.scenario(replace(entry.CONFIG, gameday_node_loss=False)).storage)
        c = entry.CONFIG
        self.assertLess(c.baseline_seconds + c.fault_window + c.recovery_timeout + c.after_recovery_seconds, 240)

    def test_invalid_scenarios_are_rejected(self):
        log = []
        with self.assertRaisesRegex(ValueError, 'outside its window'):
            gameday.Scenario('s', '', 10, [gameday.Step(10, FakeFault('a', log))]).validate()
        with self.assertRaisesRegex(ValueError, 'twice'):
            gameday.Scenario('s', '', 10, [gameday.Step(0, FakeFault('a', log)), gameday.Step(1, FakeFault('a', log))]).validate()
        from dataclasses import replace
        with self.assertRaisesRegex(ValueError, 'under 4 minutes'):
            entry.validate_config(replace(entry.CONFIG, recovery_timeout=600))

    def test_control_plane_is_never_stopped(self):
        with self.assertRaisesRegex(ValueError, 'never the control plane'):
            gameday.WorkerDown('aat-control')
        with self.assertRaisesRegex(ValueError, 'refusing'):
            gameday.node_action(RUN, 'stop-worker', 'aat-control')

    def test_network_policies(self):
        drop = gameday.NetworkDrop('x', {'role': 'sensor'}).policy()
        self.assertEqual(drop['spec']['ingress'], [])
        self.assertEqual(drop['metadata']['labels'], {gameday.LABEL: 'true'})
        cut = gameday.NetworkDrop('y', {'role': 'consumer'}, 'egress').policy()
        self.assertEqual(cut['spec']['policyTypes'], ['Egress'])
        self.assertEqual({p['port'] for p in cut['spec']['egress'][0]['ports']}, {53}, 'only DNS stays reachable')


if __name__ == '__main__':
    unittest.main()
