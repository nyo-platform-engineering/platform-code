"""Offline validation of deployment measurements; no alternative runtime is started."""
import importlib.util
from pathlib import Path
import sys
import unittest
from types import SimpleNamespace
from unittest.mock import Mock

P4 = Path(__file__).resolve().parent / "m2/p4"
sys.path.insert(0, str(P4.parent))
sys.path.insert(0, str(P4))
import deployment_checks as checks
sys.path.remove(str(P4))


class BurstMeasurementTests(unittest.TestCase):
    def test_session_is_read_inside_deployed_pod_without_token_in_arguments(self):
        run = SimpleNamespace(cluster=Mock(), config=SimpleNamespace(request_timeout=15))
        run.cluster.kubectl.return_value = {"latest_offset": 100, "speed": 60, "profile": "practice"}
        self.assertEqual(checks.feed_session(run)["speed"], 60)
        args = run.cluster.kubectl.call_args.args
        self.assertEqual(args[:4], ("exec", "deployment/aggregator", "-c", "aggregator"))
        self.assertIn("${SATELLITE_GROUP_TOKEN}", args[-1])
        self.assertIn("/v1/session", args[-1])
        self.assertNotIn("/v1/stream", args[-1])

    def test_undeployed_dashboard_and_missing_reference_remain_pending(self):
        run = SimpleNamespace(env={"ANALYST_CLIENT_SECRET": "test-only"}, login=Mock(return_value="session"),
                              call=Mock(return_value=(404, None, 1)), config=SimpleNamespace(reference_file=None))
        self.assertTrue(checks.dashboard(run)["skipped"])
        self.assertTrue(checks.reference(run)["skipped"])

    def test_recovery_uses_conservative_end_bound(self):
        tracker = checks.BurstTracker()
        self.assertIsNone(tracker.observe(0, 1, 100, 100))
        self.assertIsNone(tracker.observe(5, 60, 200, 110))
        self.assertIsNone(tracker.observe(10, 60, 300, 200))
        self.assertIsNone(tracker.observe(15, 1, 310, 250))
        self.assertEqual(tracker.observe(20, 1, 320, 320), 10)
        self.assertTrue(tracker.whole_burst_observed)

    def test_late_recovery_cannot_pass_due_to_sampling_uncertainty(self):
        tracker = checks.BurstTracker()
        tracker.observe(0, 60, 100, 0)
        tracker.observe(5, 1, 100, 0)
        self.assertGreater(tracker.observe(61, 1, 100, 100), 60)
        self.assertFalse(tracker.whole_burst_observed)

    def test_no_real_burst_or_missing_offset_is_not_recovery(self):
        tracker = checks.BurstTracker()
        self.assertIsNone(tracker.observe(0, 1, 100, 100))
        self.assertIsNone(tracker.observe(5, 1, 100, 100))
        self.assertFalse(tracker.started)
        tracker.observe(10, 60, 150, None)
        self.assertIsNone(tracker.observe(15, 1, 150, None))

    def test_session_reset_and_second_burst_do_not_mix_measurements(self):
        tracker = checks.BurstTracker()
        tracker.observe(0, 60, 100, 10)
        with self.assertRaisesRegex(ValueError, "backwards/reset"):
            tracker.observe(5, 1, 10, 10)
        tracker = checks.BurstTracker()
        tracker.observe(0, 60, 100, 10)
        tracker.observe(5, 1, 100, 10)
        with self.assertRaisesRegex(ValueError, "second burst"):
            tracker.observe(10, 60, 200, 20)


if __name__ == "__main__":
    unittest.main()
