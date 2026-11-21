"""Offline feed progress tests; no cluster access or real polling delays."""
from contextlib import contextmanager
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

M2 = Path(__file__).resolve().parent / "m2"
sys.path.insert(0, str(M2))
sys.path.insert(0, str(M2 / "p4"))
spec = importlib.util.spec_from_file_location("p4_feed_progress", M2 / "p4/group1_feed.py")
feed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(feed)
sys.path.remove(str(M2 / "p4"))
from run_progress import Progress


def snapshot(offset=1000, lag=1000, rejected=0):
    return {"feed": {"offset": offset, "latest_offset": 2000, "lag_events": lag,
                     "rejected_events": rejected, "virtual_now": "2026-11-21T00:00:00Z"}}


class FeedProgressTests(unittest.TestCase):
    @contextmanager
    def run_samples(self, samples):
        with tempfile.TemporaryDirectory() as folder:
            reporters, lines, clock = [], [], [0.0]
            run = SimpleNamespace(folder=Path(folder),
                                  config=SimpleNamespace(catch_up_timeout=300, max_lag_events=500,
                                                         lag_observe_seconds=30, request_timeout=15, observe_seconds=180),
                                  clean=lambda value: value.replace("private-token", "[REDACTED]"),
                                  aggregator=Mock(side_effect=samples), cluster=Mock())

            def reporter(*args, **kwargs):
                # Pulse deterministically, without waiting for the periodic timer.
                progress = Progress(*args, **kwargs, interval=86400)
                reporters.append(progress)
                return progress

            def wait(description, check, timeout):
                self.assertIn(timeout, (65, 215, 300))
                for _ in samples:
                    try:
                        result = check()
                    except (RuntimeError, ValueError):
                        result = None
                    reporters[-1].pulse()
                    if result:
                        return result
                    clock[0] += 10
                raise ValueError("timed out waiting for " + description)

            run.wait = wait
            with patch.object(feed.time, "monotonic", side_effect=lambda: clock[0]), \
                    patch.object(feed, "Progress", side_effect=reporter), \
                    patch.object(feed, "output", side_effect=lambda kind, message, *details: lines.append((kind, " ".join((message, *details))))):
                try:
                    yield run, lines
                finally:
                    self.assertTrue(reporters)
                    self.assertTrue(all(not p.thread.is_alive() for p in reporters))

    def test_backlog_warns_and_continues_after_fixed_observation(self):
        samples = [snapshot(1000 + i * 100, 1000 + i * 100) for i in range(4)]
        with self.run_samples(samples + [snapshot(1400, 1400)]) as (run, lines):
            result = feed.observe_backlog(run)
            self.assertFalse(result["caught_up"])
            self.assertFalse(result["burst_recovery_verified"])
            self.assertEqual(result["seconds"], 30)
            self.assertEqual(result["lag_before"], 1000)
            self.assertEqual(result["lag_after"], 1300)
            self.assertEqual(result["events_per_second"], 10)
            self.assertEqual(feed.no_rejected_events(run), {"rejected_events": 0})
            self.assertTrue(any(kind == "WARN" for kind, _ in lines))
            text = "\n".join(message for _, message in lines)
            for detail in ("offset=1000", "offset=1300", "latest_offset=2000", "lag_events=1300",
                           "rejected_events=0", "observe feed activity for 30s", " / 65s"):
                self.assertIn(detail, text)
            rows = [json.loads(line) for line in (run.folder / "progress.jsonl").read_text().splitlines()]
            self.assertEqual(rows[-1]["elapsed_seconds"], 30)
            self.assertIn("lag_events=1300", rows[-1]["detail"])

    def test_small_lag_still_observes_full_window_without_warning(self):
        with self.run_samples([snapshot(1000 + i * 100, 0) for i in range(4)]) as (run, lines):
            result = feed.observe_backlog(run)
            self.assertTrue(result["caught_up"])
            self.assertEqual(result["seconds"], 30)
            self.assertEqual(run.aggregator.call_count, 4)
            self.assertFalse(any(kind == "WARN" for kind, _ in lines))

    def test_unknown_lag_and_transient_errors_are_reported_and_redacted(self):
        with self.run_samples([snapshot(lag=None), RuntimeError("private-token unavailable"),
                               snapshot(1200, None), snapshot(1300, None)]) as (run, lines):
            self.assertFalse(feed.observe_backlog(run)["caught_up"])
            text = "\n".join(message for _, message in lines)
            evidence = (run.folder / "progress.jsonl").read_text()
            for log in (text, evidence):
                self.assertIn("lag_events=unknown", log)
                self.assertIn("stats request failed", log)
                self.assertIn("[REDACTED] unavailable", log)
                self.assertNotIn("private-token", log)

    def test_stalled_feed_fails_even_with_small_lag(self):
        with self.run_samples([snapshot(lag=0) for _ in range(4)]) as (run, lines):
            with self.assertRaisesRegex(ValueError, "offset did not advance"):
                feed.observe_backlog(run)

    def test_repeated_request_failures_do_not_pass(self):
        with self.run_samples([RuntimeError("unavailable") for _ in range(4)]) as (run, lines):
            with self.assertRaisesRegex(ValueError, "timed out waiting for observe feed activity"):
                feed.observe_backlog(run)
            self.assertIn("stats request failed", lines[-1][1])

    def test_rejected_events_fail_separately_even_with_large_backlog(self):
        samples = [snapshot(1000 + i * 100, 1000, rejected=2) for i in range(5)]
        with self.run_samples(samples) as (run, lines):
            self.assertFalse(feed.observe_backlog(run)["caught_up"])
            with self.assertRaisesRegex(ValueError, "2 feed events were rejected"):
                feed.no_rejected_events(run)

    def test_unknown_rejection_count_does_not_certify_zero(self):
        samples = [snapshot(1000 + i * 100, rejected=None) for i in range(5)]
        with self.run_samples(samples) as (run, lines):
            feed.observe_backlog(run)
            with self.assertRaisesRegex(ValueError, "cannot prove zero"):
                feed.no_rejected_events(run)

    def test_group_continues_to_statistics_after_backlog_warning(self):
        samples = [snapshot(1000 + i * 100, 10000) for i in range(5)]
        with self.run_samples(samples) as (run, lines):
            stages = []

            def stage(name, operation, *args):
                stages.append(name)
                return operation(*args)

            run.stage = stage
            with patch("builtins.input", return_value=""), \
                    patch.object(feed, "live_feed", return_value={}), \
                    patch.object(feed.consumer_stats, "measure", return_value={"skipped": True}):
                feed.run_group(run)
            self.assertEqual(stages, ["live-feed-consumed", "feed-backlog-observation",
                                      "no-rejected-feed-events", "consumer-stats"])
            self.assertTrue(any(kind == "WARN" for kind, _ in lines))

    def test_live_observation_runs_for_several_minutes_with_progress(self):
        samples = [snapshot(1000 + i * 20, 0) for i in range(20)]
        with self.run_samples(samples) as (run, lines):
            result = feed.live_feed(run)
            self.assertGreaterEqual(result["seconds"], 180)
            self.assertGreater(result["offset_after"], result["offset_before"])
            self.assertIn("observe live satellite feed for 180s", " ".join(text for _, text in lines))

    def test_first_offset_reports_missing_offset_and_accepts_zero(self):
        with self.run_samples([snapshot(offset=None, lag=None), snapshot(offset=0)]) as (run, lines):
            self.assertEqual(feed.first_offset(run)["feed"]["offset"], 0)
            self.assertIn("offset=unknown", "\n".join(message for _, message in lines))


if __name__ == "__main__":
    unittest.main()
