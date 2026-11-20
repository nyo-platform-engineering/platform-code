"""Offline P2 validator tests; never mutate the cluster or generate real load."""
import copy
import hashlib
import importlib.util
import json
import os
import pty
import termios
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
        ("p2_requests", 30), ("p2_successes", 30), ("p2_failures", 0), ("p2_controlled_rejections", 0),
        ("p2_vus_exercised", 2))}
    for window in range(3):
        metrics[f"p2_requests{{window:{window}}}"] = {"values": {"count": 10}}
        metrics[f"p2_successes{{window:{window}}}"] = {"values": {"count": 10}}
        metrics[f"p2_active_vus{{window:{window}}}"] = {"values": {"min": 2}}
    return {"metrics": metrics}


class HelperCompatibilityTests(unittest.TestCase):
    def test_deployment_records_read_limits_and_rejects_mixed_pods(self):
        run = object.__new__(p2_helper.Run)
        pods = [{"metadata": {"name": name, "labels": {"role": "aggregator"}},
                 "spec": {"nodeName": name},
                 "status": {"conditions": [{"type": "Ready", "status": "True"}],
                            "containerStatuses": [{"name": "aggregator", "imageID": "sha256:abc"}]}}
                for name in ("a", "b", "c")]
        run.pods = lambda: pods
        settings = {name: "80\n20\n" for name in ("a", "b", "c")}
        def kube(*args, **kwargs):
            if args[0] == "exec":
                self.assertIn("${READ_RPS_LIMIT:-0}", args[-1])
                self.assertNotIn("TOKEN", args[-1])
                return settings[args[1]]
            return {"spec": {"template": {"spec": {"containers": [{"name": "aggregator", "image": "same"}]}}}}
        run.kube = kube
        state = run.deployment_state()
        self.assertEqual(state["read_admission"], {"rps_per_replica": 80, "burst_per_replica": 20})
        settings["c"] = "40\n20\n"
        with self.assertRaisesRegex(ValueError, "different read rate limits"):
            run.deployment_state()

    def test_previous_version_excludes_current_runtime_digest_and_unsafe_releases(self):
        current, old, safe = "a" * 64, "b" * 64, "c" * 64
        def pin(digest, source):
            return SimpleNamespace(stdout=f"  repository: example/aggregator\n  tag: sha-{source}\n  digest: sha256:{digest}\n")
        policy=SimpleNamespace(stdout='const CacheFillPolicy = "coalesced-until-deadline-v2"')
        responses=[SimpleNamespace(returncode=0,stdout="current\nunsafe\nsafe\n"),
                   pin(current,"1"*40),policy,pin(old,"2"*40),SimpleNamespace(stdout="old fixed-attempt loop"),
                   pin(safe,"3"*40),policy]
        with patch.object(p2_helper.subprocess,"run",side_effect=responses):
            self.assertEqual(p2_helper.previous_rolling_image("local:current",{"sha256:"+current}),
                             "example/aggregator@sha256:"+safe)
        responses=[SimpleNamespace(returncode=0,stdout="current\nunsafe\n"),
                   pin(current,"1"*40),policy,pin(old,"2"*40),SimpleNamespace(stdout="old cache")]
        with patch.object(p2_helper.subprocess,"run",side_effect=responses):
            with self.assertRaisesRegex(ValueError,"publish a second Aggregator build"):
                p2_helper.previous_rolling_image("local:current",{"sha256:"+current})
        with patch.object(p2_helper.subprocess,"run",return_value=SimpleNamespace(returncode=0,stdout="")):
            with self.assertRaisesRegex(ValueError,"running Aggregator image predates"):
                p2_helper.previous_rolling_image("local:current")

    def test_image_preload_cleans_up_after_failed_pull(self):
        run = object.__new__(p2_helper.Run)
        run.run_id = "test"
        run.config = SimpleNamespace(rolling_image="example/aggregator@sha256:" + "b" * 64)
        run.cleanup = []
        calls = []
        run.kube = lambda *args, **kwargs: calls.append((args, kwargs))
        run.wait = lambda *args, **kwargs: (_ for _ in ()).throw(ValueError("pull failed"))
        with self.assertRaisesRegex(ValueError, "pull failed"):
            run.preload_rolling_image(["worker"], {"spec": {"template": {"spec": {}}}})
        manifest = calls[0][1]["body"]
        pod = manifest["spec"]["template"]["spec"]
        self.assertEqual(pod["containers"][0]["command"], ["/bin/sh", "-c", "sleep 3600"])
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertEqual(calls[-1][0][:2], ("delete", "daemonset"))
        self.assertEqual(run.cleanup, [])

    def test_wait_timeout_keeps_the_condition_name(self):
        run = object.__new__(p2_helper.Run)
        run.config = SimpleNamespace(recovery_seconds=5)
        clock = [0]
        def stalled():
            clock[0] = 6
            return False
        with patch.object(p2_helper.time, "monotonic", side_effect=lambda: clock[0]):
            with self.assertRaisesRegex(ValueError, "timed out waiting for new owner"):
                run.wait(stalled, description="new owner")

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

    def test_freeze_reconnect_requires_anchor_replay_and_fresh_records(self):
        uid = "owner"
        pod = {"metadata": {"uid": uid, "name": "owner", "labels": {}},
               "spec": {"nodeName": "aat-worker-1"},
               "status": {"conditions": [{"type": "Ready", "status": "True"}]}}
        before = json.dumps({"msg": "poll succeeded", "source": "BMKG", "epoch": 1}) + "\n"
        fresh = json.dumps({"msg": "poll succeeded", "source": "BMKG", "epoch": 2}) + "\n"
        anchor = (uid, hashlib.sha256(before.encode()).hexdigest())
        old = SimpleNamespace(poll=lambda: 1, returncode=1)
        new = SimpleNamespace(poll=lambda: None, stdout=[fresh])
        with tempfile.TemporaryDirectory() as folder:
            logs = p2_helper.Logs(SimpleNamespace(credentials={}, run_id="test", folder=Path(folder),
                          pods=lambda: [pod], save=lambda *args: None))
            logs.streams[uid] = old
            logs.last_fingerprints[uid] = anchor
            logs.seen.add(anchor)
            logs.begin_freeze(uid)
            self.assertFalse(logs.attachable(pod, old))
            logs.resume_freeze(uid)
            self.assertTrue(logs.attachable(pod, old))
            self.assertEqual(logs.errors, [])
            logs.streams[uid] = new
            logs.read(new, pod, "aggregator")
            self.assertFalse(logs.freeze_recovered(uid), "fresh records alone cannot prove continuity")
            new.stdout = [before, fresh]
            logs.read(new, pod, "aggregator")
            self.assertTrue(logs.freeze_recovered(uid))
            evidence = logs.finish_freeze(uid)
            self.assertTrue(evidence["pre_freeze_anchor_replayed"])
            self.assertFalse(evidence["same_stream"])
            self.assertNotIn(uid, logs.freeze_streams)
            self.assertTrue(logs.attachable(pod, old))
            self.assertTrue(logs.errors, "unexpected later disconnects remain fatal")

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


class GatewayPreflightTests(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.root = Path(folder.name)
        infra = patch.object(p2_helper, "INFRA", self.root)
        infra.start()
        self.addCleanup(infra.stop)
        self.run = p2_helper.Run(runner.TestConfig(kubeconfig=self.root / ".state/kubeconfig"),
                                 self.root, "gateway-test")
        self.nodes = [{"metadata": {"name": "aat-control"}, "status": {
            "nodeInfo": {"osImage": "Ubuntu 24.04"},
            "addresses": [{"type": "InternalIP", "address": "192.168.56.10"}]}}]

    def test_vm_uses_reachable_control_node_instead_of_loopback(self):
        self.run.configure_cluster(self.nodes)
        self.assertEqual(self.run.config.url, "http://192.168.56.10:30088")
        self.assertFalse(self.run.container_nodes)
        self.assertEqual(self.run.report["gateway_url"], self.run.config.url)

    def test_k3d_uses_published_host_port(self):
        self.nodes[0]["status"]["nodeInfo"]["osImage"] = "K3s v1.33"
        self.nodes[0]["status"]["addresses"][0]["address"] = "172.18.0.2"
        self.run.configure_cluster(self.nodes)
        self.assertEqual(self.run.config.url, "http://127.0.0.1:30088")
        self.assertTrue(self.run.report["container_nodes_development_only"])

    def test_task_up_mode_marker_is_used_only_for_local_kubeconfig(self):
        (self.root / ".state").mkdir()
        (self.root / ".state/mode").write_text("k3d\n")
        self.run.configure_cluster(self.nodes)
        self.assertTrue(self.run.container_nodes)
        self.run.config.kubeconfig = self.root / "another-cluster.yaml"
        self.run.config.url = None
        self.run.configure_cluster(self.nodes)
        self.assertFalse(self.run.container_nodes)
        self.assertEqual(self.run.config.url, "http://192.168.56.10:30088")

    def test_custom_gateway_is_preserved(self):
        self.run.config.url = "https://gateway.example:8443"
        self.run.configure_cluster(self.nodes)
        self.assertEqual(self.run.config.url, "https://gateway.example:8443")

    def test_native_control_plane_and_ipv6_address(self):
        self.nodes[0]["metadata"] = {"name": "native-server", "labels": {"node-role.kubernetes.io/control-plane": "true"}}
        self.nodes[0]["status"]["addresses"][0]["address"] = "fd00::10"
        self.run.configure_cluster(self.nodes)
        self.assertEqual(self.run.config.url, "http://[fd00::10]:30088")

    def test_missing_vm_address_requests_explicit_gateway(self):
        self.nodes[0]["status"]["addresses"] = []
        with self.assertRaisesRegex(ValueError, "set CONFIG.url"):
            self.run.configure_cluster(self.nodes)

    def test_route_can_be_ready_before_any_events_are_ingested(self):
        self.run.config.url = "http://192.168.56.10:30088"
        self.run.request = lambda *args, **kwargs: {"items": [], "partial": False, "unavailable_units": []}
        self.run.wait_for_route()

    def test_route_timeout_reports_actual_failure_and_short_budget(self):
        self.run.config.url = "http://127.0.0.1:30088"
        def disconnected(*args, **kwargs):
            raise p2_helper.urllib.error.URLError("Connection refused")
        self.run.request = disconnected
        def time_out(operation, timeout, description):
            self.assertEqual(timeout, 30)
            self.assertIn(self.run.config.url, description)
            self.assertFalse(operation())
            raise ValueError("timed out waiting for " + description)
        self.run.wait = time_out
        with self.assertRaisesRegex(ValueError, "last response: .*Connection refused.*CONFIG.url"):
            self.run.wait_for_route()

    def test_incomplete_route_response_is_not_treated_as_ready(self):
        self.run.config.url = "http://192.168.56.10:30088"
        def time_out(operation, **kwargs):
            self.assertFalse(operation())
            raise ValueError("timed out")
        self.run.wait = time_out
        for page in (None, [], {"items": []}, {"items": [], "partial": True, "unavailable_units": ["citus"]}):
            with self.subTest(page=page):
                self.run.request = lambda *args, **kwargs: page
                with self.assertRaisesRegex(ValueError, "response is incomplete or invalid"):
                    self.run.wait_for_route()

    def test_k3d_does_not_run_vm_clock_or_suspend_actions(self):
        run = SimpleNamespace(container_nodes=True,
                              stage=lambda *args: self.fail("must not change the shared container clock"))
        with self.assertRaisesRegex(ValueError, "separate-kernel VM"):
            runner.group2_clock_freeze.run_group(run)


class PollingEvidenceTests(unittest.TestCase):
    def test_two_successful_instances_in_same_epoch_fail(self):
        evidence = records()
        evidence.append(dict(evidence[-1], pod_uid="competing-owner"))
        with self.assertRaisesRegex(ValueError, "multiple instances successfully"):
            p2_helper.polling_evidence(evidence, 0, 20, 3)

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

    def test_upstream_response_can_outlive_cancelled_caller(self):
        data = records()
        data[4]["log"]["status"] = 0
        result = p2_helper.polling_evidence(data, 0, 20, 3)
        self.assertEqual(result["upstream_requests"]["PVMBG/volcanic-reports"], 1)
        self.assertEqual(result["unconfirmed_responses"], [{
            "source": "PVMBG", "path": "/volcanic-reports",
            "correlation_id": "PVMBG/volcanic-reports", "pod_uid": "owner-a", "epoch": 1}])

    def test_cancelled_caller_still_checks_competing_owner(self):
        data = records()
        data[2]["log"].update(status=0, poll_owner="b" * 32)
        with self.assertRaisesRegex(ValueError, "multiple owners"):
            p2_helper.polling_evidence(data, 0, 20, 3)

    def test_cancelled_caller_does_not_prove_successful_poll(self):
        data = records()
        data[4]["log"]["status"] = 0
        data.pop()
        with self.assertRaisesRegex(ValueError, "no successful PVMBG"):
            p2_helper.polling_evidence(data, 0, 20, 3)

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
        for sample_seconds, scaling_seconds, startup_seconds in ((120, None, 50), (20, 60, 12)):
            with self.subTest(sample_seconds=sample_seconds):
                clock, transitions, budgets = [0.0], {}, []
                window = scaling_seconds or sample_seconds
                run = SimpleNamespace(config=SimpleNamespace(load_seconds=sample_seconds, scaling_seconds=scaling_seconds))
                def replicas(count, timeout=None):
                    if timeout is not None:
                        budgets.append(timeout)
                    if count == 5:
                        clock[0] += startup_seconds
                run.replicas = replicas
                run.observe = lambda: {}
                run.save = lambda name, value: transitions.update({name: copy.deepcopy(value)})
                def fake_load(run, name, action, seconds):
                    self.assertEqual(seconds, window)
                    return {"actions": action(0, SimpleNamespace(poll=lambda: None))}
                with patch.object(group3_load, "load", side_effect=fake_load), \
                        patch.object(group3_load.time, "monotonic", side_effect=lambda: clock[0]), \
                        patch.object(group3_load.time, "sleep", side_effect=lambda seconds: clock.__setitem__(0, clock[0] + seconds)):
                    group3_load.scaling(run)
                phases = transitions["scale-transitions.json"]
                self.assertGreaterEqual(phases[2]["requested_seconds"], phases[1]["ready_seconds"] + 5)
                self.assertLess(phases[2]["ready_seconds"], window - 5)
                self.assertEqual(len(budgets), 2)
                self.assertTrue(all(0 < budget < window for budget in budgets))

    def test_quick_throughput_keeps_two_times_requirement_and_saves_comparison(self):
        saved, counts = {}, []
        run = SimpleNamespace(config=runner.TestConfig(load_seconds=20, scaling_seconds=60),
                              replicas=counts.append, save=lambda name, value: saved.update({name: value}),
                              deployment_state=lambda: {"image": "same-image", "nodes": ["a"] if counts[-1] == 1 else ["a", "b", "c"]})
        def measured(run, name):
            self.assertEqual(run.config.load_seconds, 20)
            return {"successes_per_second": 33.19 if name == "throughput-1" else 20.12}
        with patch.object(group3_load, "load", side_effect=measured):
            with self.assertRaisesRegex(ValueError, "0.61x.*below 2.0x"):
                group3_load.throughput(run)
        self.assertEqual(counts, [1, 3])
        self.assertLess(saved["throughput-comparison.json"]["ratio"], 2)

    def test_reverted_image_invalidates_throughput_instead_of_reporting_ratio(self):
        states = iter([{"image": "new"}, {"image": "old"}])
        run = SimpleNamespace(replicas=lambda count: None, save=lambda *args: None,
                              deployment_state=lambda: next(states))
        with patch.object(group3_load, "load", return_value={"successes_per_second": 100}):
            with self.assertRaisesRegex(ValueError, "changed during throughput measurement; comparison is invalid"):
                group3_load.throughput(run)

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
        self.assertEqual(totals["retry_sleeps"], 5)
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

    def test_rolling_requires_every_vu_to_send_a_request(self):
        group3_load.validate_summary(summary(), 2, 15, strict=True)
        data = summary()
        data["metrics"]["p2_vus_exercised"]["values"]["count"] = 1
        with self.assertRaisesRegex(ValueError, "exercise every VU"):
            group3_load.validate_summary(data, 2, 15, strict=True)

    def test_error_denominator_excludes_rejections(self):
        data = summary()
        data["metrics"]["p2_requests"]["values"]["count"] = 1030
        data["metrics"]["p2_successes"]["values"]["count"] = 29
        data["metrics"]["p2_failures"]["values"]["count"] = 1
        data["metrics"]["p2_controlled_rejections"]["values"]["count"] = 1000
        with self.assertRaisesRegex(ValueError, ">=1%"):
            group3_load.validate_summary(data, 2, 15)


class ManualBuildPlanTests(unittest.TestCase):
    def test_manual_build_can_target_aggregator_without_rebuilding_other_services(self):
        script=p2_helper.ROOT.parents[2]/".github/scripts/aat/detect_changed_services.py"
        for service,valid in (("aggregator",True),("all",True),("unknown-service",False)):
            with self.subTest(service=service), tempfile.TemporaryDirectory() as folder:
                event,output=Path(folder)/"event.json",Path(folder)/"output"
                event.write_text(json.dumps({"inputs":{"service":service}}))
                result=subprocess.run([sys.executable,"-B",str(script)],env=dict(os.environ,
                    GITHUB_EVENT_NAME="workflow_dispatch",GITHUB_EVENT_PATH=str(event),GITHUB_OUTPUT=str(output)),
                    capture_output=True,text=True)
                self.assertEqual(result.returncode==0,valid,result.stderr)
                if not valid: continue
                line=next(line for line in output.read_text().splitlines() if line.startswith("matrix="))
                services=[v["service"] for v in json.loads(line.partition("=")[2])["include"]]
                self.assertEqual(services,["aggregator"] if service=="aggregator" else sorted(services))
                if service=="all": self.assertGreater(len(services),1)


class AutomaticClockTests(unittest.TestCase):
    def make_run(self):
        return SimpleNamespace(config=runner.TestConfig(worker="aat-worker-1"),
            cleanup=[], pods=lambda: [], prompt=lambda text: None,
            save=lambda name, value: None, observe=lambda: {"single_owner": True})

    def exercise(self, run, restore_error=None):
        group = runner.group2_clock_freeze
        with patch.object(group, "prepare_owner", return_value=({"pod": "owner"}, [])), \
             patch.object(group, "worker_pod", return_value="owner"), \
             patch.object(group, "clock_offset", side_effect=[{"offset_seconds": x} for x in (0, 45, -30)]), \
             patch.object(group, "vm_python", side_effect=[["chrony.service"], {}]) as vm, \
             patch.object(group, "shift_clock") as shift, \
             patch.object(group, "restore_clock", side_effect=restore_error, return_value={"offset_seconds": 0}) as restore:
            try:
                return group.clock(run)
            finally:
                restore.assert_called_once_with(run, ["chrony.service"])
                self.assertIn("systemctl','stop", vm.call_args_list[1].args[1])
                self.assertEqual(shift.call_args_list[0].args, (run, 45))

    def test_automatic_offsets_and_successful_restoration(self):
        run = self.make_run()
        result = self.exercise(run)
        self.assertEqual([r["requested_offset"] for r in result["offsets"]], [45, -30])
        self.assertEqual(run.cleanup, [])

    def test_observation_failure_and_interrupt_restore_without_prompt(self):
        for failure in (ValueError("polling failed"), KeyboardInterrupt()):
            with self.subTest(failure=type(failure).__name__):
                run = self.make_run()
                run.observe = lambda: (_ for _ in ()).throw(failure)
                with self.assertRaises(type(failure)):
                    self.exercise(run)
                self.assertEqual(run.cleanup, [])

    def test_failed_restore_keeps_outer_cleanup_retry(self):
        run = self.make_run()
        with self.assertRaisesRegex(ValueError, "restore failed"):
            self.exercise(run, ValueError("restore failed"))
        self.assertEqual(len(run.cleanup), 1)

    def test_vm_ssh_cannot_change_the_callers_terminal(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            fake = root / "vagrant"
            fake.write_text(f'''#!{sys.executable}
import os,termios,json
if os.isatty(0):
    state=termios.tcgetattr(0)
    state[1] &= ~termios.OPOST
    termios.tcsetattr(0,termios.TCSANOW,state)
print(json.dumps({{"ok":True}}))
''')
            fake.chmod(0o755)
            script = f'''import sys
from contextlib import nullcontext
from types import SimpleNamespace
from unittest.mock import patch
from pathlib import Path
sys.path.insert(0,{str(P2)!r})
import group2_clock_freeze as group
run=SimpleNamespace(container_nodes=False,
    config=SimpleNamespace(worker="aat-worker-1",kubeconfig=group.INFRA/".state/kubeconfig"),
    phase=lambda *a,**kw:nullcontext())
with patch.object(Path,"is_file",return_value=True):
    assert group.vm_python(run,"pass")["ok"]
'''
            master, slave = pty.openpty()
            try:
                before=termios.tcgetattr(slave)
                subprocess.run([sys.executable,"-c",script],stdin=slave,stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE,env=dict(os.environ,PATH=folder+os.pathsep+os.environ["PATH"]),
                               check=True,timeout=15)
                self.assertEqual(before,termios.tcgetattr(slave))
            finally:
                os.close(master)
                os.close(slave)

    def test_shift_uses_relative_correction_not_a_stale_absolute_timestamp(self):
        group = runner.group2_clock_freeze
        with patch.object(group.time, "time", side_effect=[100, 102]), \
             patch.object(group, "vm_python", side_effect=[146, {}]) as vm:
            group.shift_clock(self.make_run(), -30)
        self.assertIn("time.time() + -75.0", vm.call_args.args[1])

    def test_shared_kernel_clock_injection_is_rejected_before_ssh(self):
        run = self.make_run()
        run.container_nodes = True
        with patch.object(runner.group2_clock_freeze.subprocess, "run") as command:
            with self.assertRaisesRegex(ValueError, "separate-kernel VM"):
                runner.group2_clock_freeze.vm_python(run, "pass")
            command.assert_not_called()


class WorkerOwnerPreparationTests(unittest.TestCase):
    def test_restart_waits_for_real_new_epoch_on_target_worker(self):
        current = {"pod": "old", "pod_uid": "uid-old", "node": "control", "log": {"poll_epoch": "2"}}
        target = {"pod": "target", "pod_uid": "uid-new", "node": "worker", "log": {"poll_epoch": "3"}}
        writes, calls = {}, []
        run = SimpleNamespace(config=SimpleNamespace(source="BMKG", worker="worker", recovery_seconds=180, takeover_seconds=60),
                              owner=lambda source: current, status=lambda text: None,
                              kube=lambda *args, **kwargs: calls.append(args),
                              save=lambda name, value: writes.update({name: value}),
                              replicas=lambda count, **kwargs: calls.append(("replicas", count)))
        logs = []
        run.logs = SimpleNamespace(snapshot=lambda: logs)
        def wait(operation, **kwargs):
            nonlocal current
            base = {"observed": p2_helper.time.monotonic()+1, "pod_uid": "uid-new", "initial": False,
                    "log": {"msg": "poll succeeded", "source": "BMKG", "epoch": 2}}
            logs.append(base)
            self.assertFalse(operation(), "old epochs must not qualify")
            logs[0] = dict(base, log=dict(base["log"], epoch=3), initial=True)
            self.assertFalse(operation(), "historical startup logs must not qualify")
            logs[0]["initial"] = False
            self.assertTrue(operation())
            current = target
        run.wait = wait
        owner, moves = runner.group2_clock_freeze.prepare_owner(run)
        self.assertEqual(owner, target)
        self.assertEqual(moves, [{"deleted_owner": "old", "old_epoch": 2}])
        self.assertEqual(calls[0][:3], ("delete", "pod", "old"))
        self.assertIn(("replicas", 3), calls)


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

    def test_request_explains_transport_failure_without_credentials(self):
        with tempfile.TemporaryDirectory() as folder:
            run = self.make_run(folder)
            run.config.url = "http://127.0.0.1:1"
            run.credentials = {"AUDIT_TOKEN": "private-audit-token"}
            with patch.object(p2_helper.urllib.request, "build_opener") as opener:
                opener.return_value.open.side_effect = p2_helper.urllib.error.URLError(
                    ConnectionRefusedError(111, "Connection refused"))
                with self.assertRaisesRegex(p2_helper.RequestError, "audit/events: network error:.*Connection refused") as caught:
                    run.request("/internal/audit/events", "AUDIT_TOKEN")
                self.assertTrue(caught.exception.retryable)
                self.assertNotIn("private-audit-token", str(caught.exception))
                opener.return_value.open.side_effect = p2_helper.urllib.error.HTTPError("unused",401,"Unauthorized",None,None)
                with self.assertRaises(p2_helper.RequestError) as caught:
                    run.request("/internal/audit/events", "AUDIT_TOKEN")
                self.assertFalse(caught.exception.retryable)

    def test_audit_retry_keeps_the_same_cursor_and_checks_all_identities(self):
        with tempfile.TemporaryDirectory() as folder:
            run = self.make_run(folder)
            def event(number):
                return {"source":"BMKG", "source_ref_id":str(number),
                        "hazard_id":"haz-"+str(number), "ingested_at":"first"}
            pages = iter([{"partial":False,"unavailable_units":[],"items":[event(1)],"next_cursor":"page-2"},
                          p2_helper.RequestError("connection reset",retryable=True),
                          {"partial":False,"unavailable_units":[],"items":[event(2)],"next_cursor":None}])
            calls = []
            def request(path, token, params):
                calls.append(dict(params))
                result = next(pages)
                if isinstance(result,Exception): raise result
                return result
            run.request = request
            with patch.object(run, "pause") as pause:
                self.assertEqual(len(run.audit()),2)
            self.assertEqual(calls,[{"limit":1000},{"limit":1000,"cursor":"page-2"},{"limit":1000,"cursor":"page-2"}])
            pause.assert_called_once()

    def test_audit_does_not_retry_auth_failures_or_exceed_retry_budget(self):
        with tempfile.TemporaryDirectory() as folder:
            for retryable in (False,True):
                run = self.make_run(folder)
                def failed(*args): raise p2_helper.RequestError("test failure",retryable=retryable)
                run.request = failed
                with patch.object(p2_helper.time,"monotonic",side_effect=[0,31]), patch.object(run,"pause") as pause:
                    with self.assertRaisesRegex(p2_helper.RequestError,"test failure"):
                        run.audit()
                    pause.assert_not_called()

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

    def test_checked_in_config_runs_all_groups_at_pdf_load_size(self):
        self.assertEqual(runner.CONFIG.groups, (1, 2, 3))
        self.assertGreaterEqual(runner.CONFIG.load_vus, 10_000)
        self.assertGreaterEqual(runner.CONFIG.load_seconds, 120)
        with patch.object(sys.stdin, "isatty", return_value=True):
            config = runner.TestConfig(rolling_seconds=120)
            with self.assertRaisesRegex(ValueError, "every VU"):
                runner.validate_config(config)

    def test_pacing_must_exercise_all_vus_during_each_measurement(self):
        with patch.object(sys.stdin, "isatty", return_value=True):
            runner.validate_config(runner.TestConfig())
            with self.assertRaisesRegex(ValueError, "every VU"):
                runner.validate_config(runner.TestConfig(load_rps=20))

    def test_clock_examples_are_configurable_and_invalid_offsets_fail(self):
        with patch.object(sys.stdin, "isatty", return_value=True):
            runner.validate_config(runner.TestConfig(clock_offsets=(20, -15)))
            for offsets in ((), (0,), (float("nan"),), (float("inf"),), ("45",)):
                with self.subTest(offsets=offsets):
                    with self.assertRaisesRegex(ValueError, "clock_offsets"):
                        runner.validate_config(runner.TestConfig(clock_offsets=offsets))

    def test_short_throughput_can_use_a_separate_scaling_window(self):
        with patch.object(sys.stdin, "isatty", return_value=True):
            config = runner.TestConfig(groups=(3,), load_vus=100, load_seconds=20)
            with self.assertRaisesRegex(ValueError, "replica transitions"):
                runner.validate_config(config)
            config.scaling_seconds = 60
            runner.validate_config(config)
            config.scaling_seconds = 15
            with self.assertRaisesRegex(ValueError, "replica transitions"):
                runner.validate_config(config)
            runner.validate_config(runner.TestConfig())

if __name__ == "__main__":
    unittest.main()
