"""Offline P2 validator tests; never mutate the cluster or generate real load."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import shutil
import subprocess
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

P2 = Path(__file__).resolve().parent / "m2/p2"
# Use a private helper name so P1's helper cannot be accidentally imported.
old_helper = sys.modules.pop("helper", None)
sys.path.insert(0, str(P2))
import helper as p2_helper
import group3_load
spec = importlib.util.spec_from_file_location("p2_test_runner", P2 / "test-problem-2.py")
runner = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = runner
spec.loader.exec_module(runner)
sys.path.remove(str(P2))
sys.modules.pop("helper", None)
if old_helper is not None:
    sys.modules["helper"] = old_helper


def records():
    result = []
    for source, path in (("BMKG", "/seismic-events"), ("BMKG", "/tsunami-warnings"), ("PVMBG", "/volcanic-reports")):
        correlation = source + path
        log = {"msg": "outbound_request", "source": source, "path": path, "status": 200,
               "correlation_id": correlation, "poll_owner": "a" * 32, "poll_epoch": "1"}
        result.append({"observed": 5, "initial": False, "role": "aggregator", "pod_uid": "owner-a", "log": log})
        result.append({"observed": 5, "initial": False, "role": source.lower(), "pod_uid": "mock-" + source,
                       "log": {"msg": "request", "path": path, "status": 200, "correlation_id": correlation,
                               "time": "2026-11-19T00:00:05Z", "latency_ms": 100}})
    for source in ("BMKG", "PVMBG"):
        result.append({"observed": 6, "initial": False, "role": "aggregator", "pod_uid": "owner-a",
                       "log": {"msg": "poll succeeded", "source": source, "epoch": 1}})
    return result


def summary():
    metrics = {name: {"values": {"count": count}} for name, count in (
        ("p2_requests", 30), ("p2_successes", 30), ("p2_failures", 0), ("p2_controlled_rejections", 0))}
    for window in range(3):
        metrics[f"p2_requests{{window:{window}}}"] = {"values": {"count": 10}}
        metrics[f"p2_successes{{window:{window}}}"] = {"values": {"count": 10}}
        metrics[f"p2_active_vus{{window:{window}}}"] = {"values": {"min": 2}}
    return {"metrics": metrics}


class HelperCompatibilityTests(unittest.TestCase):
    def test_initial_attachment_waits_for_ready(self):
        logs = p2_helper.Logs(SimpleNamespace(credentials={}))
        pod = {"metadata": {"uid": "starting", "name": "aggregator-starting"}}
        self.assertFalse(logs.attachable(pod, None))
        pod["status"] = {"conditions": [{"type": "Ready", "status": "True"}]}
        self.assertTrue(logs.attachable(pod, None))
        self.assertEqual(logs.errors, [])

    def test_terminating_pod_is_not_reattached(self):
        run = SimpleNamespace(credentials={}, pods=lambda: self.fail("must not query a terminating pod"))
        logs = p2_helper.Logs(run)
        pod = {"metadata": {"uid": "old", "name": "aggregator-old", "deletionTimestamp": "now"}}
        self.assertFalse(logs.attachable(pod, None))
        self.assertEqual(logs.errors, [])

    def test_stream_end_rechecks_pod_deletion(self):
        pod = {"metadata": {"uid": "old", "name": "aggregator-old"}}
        deleting = {"metadata": dict(pod["metadata"], deletionTimestamp="now")}
        for current in ([], [deleting]):
            logs = p2_helper.Logs(SimpleNamespace(credentials={}, pods=lambda: current))
            self.assertFalse(logs.attachable(pod, SimpleNamespace(returncode=0)))
            self.assertEqual(logs.errors, [])

    def test_live_pod_stream_failure_remains_fatal_and_is_saved(self):
        pod = {"metadata": {"uid": "live", "name": "aggregator-live"}}
        saved = {}
        run = SimpleNamespace(credentials={}, pods=lambda: [pod], save=lambda name, value: saved.update({name: value}))
        logs = p2_helper.Logs(run)
        self.assertTrue(logs.attachable(pod, SimpleNamespace(returncode=1)))
        self.assertIn("exit 1", logs.errors[0])
        self.assertEqual(saved["log-errors.json"], logs.errors)

    def test_runner_imports_in_fresh_process(self):
        script = "import runpy, sys; sys.path.insert(0, sys.argv[1]); runpy.run_path(sys.argv[2], run_name='p2_import_check')"
        subprocess.run([sys.executable, "-I", "-c", script, str(P2), str(P2 / "test-problem-2.py")],
                       check=True, capture_output=True, text=True)

    def test_credentials_preserve_values_and_environment_precedence(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "runtime.env"
            path.write_text("# test\n\nAGGREGATOR_TOKEN=file=value\nAUDIT_TOKEN=file-audit\n")
            with patch.dict(os.environ, {"AUDIT_TOKEN": "environment-audit"}, clear=True):
                values = p2_helper.read_credentials(path)
            self.assertEqual(values, {"AGGREGATOR_TOKEN": "file=value", "AUDIT_TOKEN": "environment-audit"})

    def test_invalid_environment_file_fails(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "runtime.env"
            path.write_text("not an assignment\n")
            with self.assertRaisesRegex(ValueError, "invalid environment"):
                p2_helper.read_credentials(path)

    def test_redirects_are_not_followed(self):
        self.assertIsNone(p2_helper.NoRedirect().redirect_request(None, None, 302, "Found", {}, "http://other"))


class PollingEvidenceTests(unittest.TestCase):
    def test_joined_upstream_evidence(self):
        result = p2_helper.polling_evidence(records(), 0, 20, 3)
        self.assertEqual(sum(result["upstream_requests"].values()), 3)
        self.assertEqual(len(result["epochs"]), 2)

    def test_duplicate_upstream_request_fails(self):
        data = records()
        data.append(copy.deepcopy(data[1]))
        with self.assertRaisesRegex(ValueError, "duplicate upstream"):
            p2_helper.polling_evidence(data, 0, 20, 3)

    def test_missing_caller_is_not_certified(self):
        with self.assertRaisesRegex(ValueError, "matching Aggregator"):
            p2_helper.polling_evidence(records()[1:], 0, 20, 3)

    def test_two_owners_in_one_epoch_fail(self):
        data = records()
        data[2]["log"]["poll_owner"] = "b" * 32
        with self.assertRaisesRegex(ValueError, "multiple owners"):
            p2_helper.polling_evidence(data, 0, 20, 3)

    def test_overlapping_different_owners_fail(self):
        data = records()
        caller, upstream = copy.deepcopy(data[0]), copy.deepcopy(data[1])
        caller["log"].update(correlation_id="competing", poll_owner="b" * 32, poll_epoch="2")
        upstream["log"].update(correlation_id="competing", latency_ms=150)
        data.extend((caller, upstream))
        with self.assertRaisesRegex(ValueError, "overlapping"):
            p2_helper.polling_evidence(data, 0, 20, 3)


class LoadSummaryTests(unittest.TestCase):
    def test_slow_scale_up_still_gets_five_seconds_before_scale_down(self):
        clock, transitions = [0.0], {}
        run = SimpleNamespace(config=SimpleNamespace(load_seconds=120))
        def replicas(count):
            if count == 5:
                clock[0] += 50
        run.replicas = replicas
        run.observe = lambda: {}
        run.save = lambda name, value: transitions.update({name: copy.deepcopy(value)})
        def fake_load(run, name, action):
            return {"actions": action(0, SimpleNamespace(poll=lambda: None))}
        with patch.object(group3_load, "load", side_effect=fake_load), \
                patch.object(group3_load.time, "monotonic", side_effect=lambda: clock[0]), \
                patch.object(group3_load.time, "sleep", side_effect=lambda seconds: clock.__setitem__(0, clock[0] + seconds)):
            group3_load.scaling(run)
        phases = transitions["scale-transitions.json"]
        self.assertGreaterEqual(phases[2]["requested_seconds"], phases[1]["ready_seconds"] + 5)
        self.assertLess(phases[2]["ready_seconds"], 115)

    @unittest.skipUnless(shutil.which("node"), "Node is needed for the offline workload contract test")
    def test_workload_classifies_real_response_shapes_without_network(self):
        script = r'''
const fs = require('fs');
const vm = require('vm');
const observations = {};
class Metric {
  constructor(name) { this.name = name; observations[name] = []; }
  add(value, tags) { observations[this.name].push({value: Number(value), tags}); }
}
let source = fs.readFileSync(process.argv[1], 'utf8')
  .replace(/^import .*;$/gm, '')
  .replace('export const options', 'const options')
  .replace('export default function ()', 'function iteration()')
  .replace('export function handleSummary', 'function handleSummary');
const ctx = {
  __ENV: {P2_VUS: '2', P2_SECONDS: '15', P2_WARMUP: '1', P2_URL: 'http://unused',
    P2_TOKEN: 'offline-only', P2_HOST: 'test.localhost', P2_RUN_ID: 'test', P2_LIMIT: '1'},
  __VU: 1, __ITER: 0, Date: {now: () => 6000}, console: {log() {}},
  exec: {scenario: {startTime: 0}, instance: {vusActive: 2}},
  Counter: Metric, Rate: Metric, Trend: Metric,
  sleep(seconds) { (observations.retry_sleeps ||= []).push({value: seconds}); },
};
ctx.http = {get() { ctx.exec.instance.vusActive = 0; return ctx.response; }};
vm.createContext(ctx);
vm.runInContext(source, ctx);
const page = {items: [{hazard_id: 'one', source: 'BMKG', source_ref_id: 'one'}], partial: false, unavailable_units: []};
for (const [status, body, headers] of [
  [200, page, {}], [429, {}, {'Retry-After': '1'}], [429, {}, {}], [500, {}, {}],
  [200, {...page, partial: true}, {}], [200, {...page, items: []}, {}],
]) {
  ctx.exec.instance.vusActive = 2;
  ctx.response = {status, headers, timings: {duration: 1}, json: () => body};
  vm.runInContext('iteration()', ctx);
  ctx.__ITER++;
}
console.log(JSON.stringify(observations));
'''
        result = json.loads(subprocess.check_output(["node", "-e", script, str(P2 / "load.js")], text=True))
        totals = {name: sum(item["value"] for item in values) for name, values in result.items()}
        self.assertEqual(totals["p2_requests"], 6)
        self.assertEqual(totals["p2_successes"], 1)
        self.assertEqual(totals["p2_controlled_rejections"], 1)
        self.assertEqual(totals["p2_failures"], 4)
        self.assertEqual(totals["retry_sleeps"], 1)
        self.assertEqual(len(result["p2_error_rate_excluding_controlled"]), 5)
        self.assertTrue(all(item["value"] == 2 for item in result["p2_active_vus"]))
        status_counts = {}
        for item in result["p2_http_status"]:
            status = item["tags"]["status"]
            status_counts[status] = status_counts.get(status, 0) + item["value"]
        self.assertEqual(status_counts, {"200": 3, "429": 2, "500": 1})

    def test_failure_message_includes_counts_and_failed_threshold(self):
        data = summary()
        data["metrics"]["p2_failures"]["values"]["count"] = 1
        data["metrics"]["p2_error_rate_excluding_controlled"] = {"thresholds": {"rate<0.01": {"ok": False}}}
        message = group3_load.failure_message(data, 99)
        self.assertIn("1/30 (3.33%)", message)
        self.assertIn("p2_error_rate_excluding_controlled", message)

    def test_small_run_cannot_claim_pdf_minimum(self):
        result = group3_load.validate_summary(summary(), 2, 15)
        self.assertFalse(result["pdf_load_minimum_met"])
        self.assertEqual(result["successes_per_second"], 2)

    def test_missing_window_fails(self):
        data = summary()
        del data["metrics"]["p2_requests{window:1}"]
        with self.assertRaisesRegex(ValueError, "every measurement"):
            group3_load.validate_summary(data, 2, 15)

    def test_insufficient_vus_fails(self):
        data = summary()
        data["metrics"]["p2_active_vus{window:1}"]["values"]["min"] = 1
        with self.assertRaisesRegex(ValueError, "sustain configured VUs"):
            group3_load.validate_summary(data, 2, 15)

    def test_rolling_rejects_controlled_failures_too(self):
        data = summary()
        data["metrics"]["p2_requests"]["values"]["count"] = 31
        data["metrics"]["p2_controlled_rejections"]["values"]["count"] = 1
        group3_load.validate_summary(data, 2, 15)
        with self.assertRaisesRegex(ValueError, "failed or rejected"):
            group3_load.validate_summary(data, 2, 15, strict=True)

    def test_error_denominator_excludes_rejections(self):
        data = summary()
        data["metrics"]["p2_requests"]["values"]["count"] = 1030
        data["metrics"]["p2_successes"]["values"]["count"] = 29
        data["metrics"]["p2_failures"]["values"]["count"] = 1
        data["metrics"]["p2_controlled_rejections"]["values"]["count"] = 1000
        with self.assertRaisesRegex(ValueError, ">=1%"):
            group3_load.validate_summary(data, 2, 15)


class AuditAndCleanupTests(unittest.TestCase):
    def test_kubectl_error_redacts_secrets_and_url_passwords(self):
        message = 'Error: token-value Bearer unknown-value postgres://aat:db-password@postgres/aat'
        result = p2_helper.safe_error(message, {"AUDIT_TOKEN": "token-value"})
        self.assertNotIn("token-value", result)
        self.assertNotIn("unknown-value", result)
        self.assertNotIn("db-password", result)
        self.assertIn("[REDACTED]", result)

    def make_run(self, folder):
        return p2_helper.Run(runner.TestConfig(), Path(folder), "unit-test")

    def test_duplicate_version_fails_but_new_version_is_allowed(self):
        with tempfile.TemporaryDirectory() as folder:
            run = self.make_run(folder)
            event = {"source": "BMKG", "source_ref_id": "one", "hazard_id": "haz-one", "ingested_at": "first"}
            def page(items):
                return {"partial": False, "unavailable_units": [], "items": items, "next_cursor": None}
            run.request = lambda *args: page([event, dict(event)])
            with self.assertRaisesRegex(ValueError, "identical event version"):
                run.audit()
            run.request = lambda *args: page([event, dict(event, ingested_at="updated")])
            self.assertEqual(len(run.audit()), 2)
            run.request = lambda *args: page([])
            with self.assertRaisesRegex(ValueError, "disappeared"):
                run.audit()

    def test_cleanup_continues_after_one_restore_fails(self):
        with tempfile.TemporaryDirectory() as folder:
            run = self.make_run(folder)
            run.logs_started = False
            calls = []
            def fail():
                raise ValueError("restore failed")
            run.cleanup = [lambda: calls.append("parent"), fail, lambda: calls.append("worker")]
            with self.assertRaisesRegex(ValueError, "cleanup incomplete"):
                run.__exit__(None, None, None)
            self.assertEqual(calls, ["worker", "parent"])

    def test_bad_configuration_fails_before_cluster_access(self):
        with patch.object(sys.stdin, "isatty", return_value=True):
            config = runner.TestConfig()
            config.url = "http://token:secret@localhost"
            with self.assertRaisesRegex(ValueError, "without credentials"):
                runner.validate_config(config)

    def test_short_scaling_window_is_rejected_before_cluster_mutation(self):
        with patch.object(sys.stdin, "isatty", return_value=True):
            config = runner.TestConfig(groups=(3,), load_vus=100, load_seconds=30)
            with self.assertRaisesRegex(ValueError, "replica transitions"):
                runner.validate_config(config)
            config.load_seconds = 120
            runner.validate_config(config)


if __name__ == "__main__":
    unittest.main()
