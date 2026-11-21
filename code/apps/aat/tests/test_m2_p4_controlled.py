"""Validate the controlled upstream and verification guards without mutating a cluster."""
import copy
import importlib.util
import os
import json
import threading
import urllib.request
from http.server import ThreadingHTTPServer
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

P4 = Path(__file__).resolve().parent / "m2/p4"
sys.path.insert(0, str(P4.parent)); sys.path.insert(0, str(P4))
from verification_session import Verification
from p4_fixtures import CASES, EXPECTED, burst_events, key
from controlled_checks import verify_pair_oracle
spec = importlib.util.spec_from_file_location("p4_controlled_upstream", P4 / "fixture_feed.py")
upstream = importlib.util.module_from_spec(spec)
with patch.dict(os.environ, {"P4_FIXTURE_TOKEN": "test-fixture-only"}):
    spec.loader.exec_module(upstream)
sys.path.remove(str(P4))


class ControlledSourceTests(unittest.TestCase):
    def setUp(self):
        self.saved = copy.deepcopy(upstream.STATE)

    def tearDown(self):
        upstream.STATE.clear(); upstream.STATE.update(self.saved)

    def test_scheduled_source_publishes_actual_60x_then_stops_at_finite_cutoff(self):
        upstream.STATE.update(events=list(CASES) + list(burst_events(3680)), released=len(CASES),
                              burst=100.0, normal=20, duration=3, before=2, after=2)
        with patch.object(upstream.time, "monotonic", return_value=101):
            before = upstream.session()
        with patch.object(upstream.time, "monotonic", return_value=103):
            during = upstream.session()
        with patch.object(upstream.time, "monotonic", return_value=106):
            after = upstream.session()
        with patch.object(upstream.time, "monotonic", return_value=200):
            final = upstream.session()
        self.assertEqual(before["speed"], 1)
        self.assertEqual(during["speed"], 60)
        self.assertEqual(after["speed"], 1)
        self.assertEqual(during["published_count"] - len(CASES), 40 + 1200)
        self.assertEqual(final["published_count"], len(CASES) + 3680)
        self.assertEqual(final["burst_end_elapsed_seconds"], 5)

    def test_sse_reconnect_replays_requested_offset_and_pause_returns_503(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), upstream.Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        base = f"http://127.0.0.1:{server.server_port}"
        headers = {"Authorization": "Bearer test-fixture-only", "Content-Type": "application/json"}
        try:
            request = urllib.request.Request(base + "/admin/publish", data=b'{"count":2}', headers=headers)
            with urllib.request.urlopen(request, timeout=2) as response:
                self.assertEqual(json.load(response)["latest_offset"], 1)
            request = urllib.request.Request(base + "/v1/stream?from=1", headers=headers)
            with urllib.request.urlopen(request, timeout=2) as response:
                self.assertEqual(response.readline().decode().strip(), "id: 1")
                self.assertEqual(response.readline().decode().strip(), "event: detection")
                payload = json.loads(response.readline().decode().removeprefix("data: "))
                self.assertEqual(payload["detection"]["satellite"], "N21")
            self.assertEqual(upstream.session()["stream_connections"], 1)
            self.assertEqual(upstream.session()["last_from"], 1)
            request = urllib.request.Request(base + "/admin/pause", data=b'{}', headers=headers)
            with urllib.request.urlopen(request, timeout=2):
                pass
            with self.assertRaises(urllib.error.HTTPError) as error:
                urllib.request.urlopen(urllib.request.Request(base + "/v1/stream?from=1", headers=headers), timeout=2)
            self.assertEqual(error.exception.code, 503)
        finally:
            server.shutdown(); server.server_close(); worker.join(timeout=2)

    def test_restore_refuses_foreign_schema_journal(self):
        with tempfile.TemporaryDirectory() as folder:
            run = SimpleNamespace(folder=Path(folder))
            baseline = {"id": "012345abcdef", "fixture": "p4-feed-012345abcdef", "apps": {},
                        "schemas": ["public"], "env": []}
            with self.assertRaisesRegex(ValueError, "schema journal ownership"):
                Verification(run, baseline)

    def test_expected_cases_include_updates_retractions_and_inclusive_boundary(self):
        self.assertEqual(EXPECTED[3], ("AWAS", 3))
        self.assertEqual(EXPECTED[4], ("AWAS", 2))
        self.assertEqual(EXPECTED[5], ("NORMAL", 1))
        self.assertEqual(EXPECTED[6], ("AWAS", 2))
        self.assertEqual(CASES[9][1]["detection"]["acq_time"], 1200)
        self.assertEqual(CASES[10][1]["detection"]["acq_time"], 1159)

    def test_missing_capability_aborts_before_argo_or_database_mutation(self):
        with tempfile.TemporaryDirectory() as folder:
            run = SimpleNamespace(folder=Path(folder), call=Mock(return_value=(404, None, 0)), cluster=Mock())
            with self.assertRaisesRegex(ValueError, "no deployment changes"):
                with Verification(run):
                    self.fail("unsafe verification entered")
            run.cluster.kubectl.assert_not_called()
            self.assertFalse((Path(folder) / "restore-p4.json").exists())

    def test_pair_oracle_rejects_wrong_membership_even_if_count_matches(self):
        keys = [key("N20", 1200, "-3.00000", "100.00000", "2026-11-13"),
                key("N21", 1200, "-2.99900", "100.00000", "2026-11-13")]
        h = {"occurred_at": "2026-11-13T12:00:00Z", "source_ref_id": "pair", "source": "SATELIT",
             "hazard_type": "WILDFIRE", "severity": "WASPADA", "latitude": -2.9995, "longitude": 100,
             "attributes": {"detection_keys": keys, "detection_count": 2, "total_frp_mw": 10,
                            "satellites": ["N20", "N21"]}}
        snapshot = {"detections": [{}] * 9, "hazards": [{"occurred_at": "2026-11-12T12:00:00Z"}, h]}
        self.assertEqual(verify_pair_oracle(snapshot, len(CASES) + 2), [])
        h["attributes"]["detection_keys"][1] = "wrong-member"
        self.assertEqual(verify_pair_oracle(snapshot, len(CASES) + 2), ["pair"])
