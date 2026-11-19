"""Offline P3 oracle/HTTP tests. No Kubernetes mutations or load generation."""
import base64
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
import urllib.request
from unittest.mock import patch
from http.server import ThreadingHTTPServer

P3 = Path(__file__).resolve().parent / "m2/p3"
sys.path.insert(0, str(P3))
import fixtures
import support
import upstream
import group3_reports
spec = importlib.util.spec_from_file_location("p3_runner", P3 / "test-problem-3.py")
runner = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = runner
spec.loader.exec_module(runner)
sys.path.remove(str(P3))


def make_run(folder):
    run = object.__new__(support.Run)
    run.config = runner.TestConfig()
    run.folder = Path(folder)
    run.name = "offline-p3"
    run.tokens, run.token_roles, run.token_expiry = {}, {}, {}
    run.auth_lock = threading.RLock()
    run.lock = threading.Lock()
    run.secrets = set()
    run.report = {"automated_checks": {}, "manual_evidence_required": []}
    return run


class FixtureTests(unittest.TestCase):
    def test_poison_payloads_exercise_distinct_processing_limits(self):
        cases = fixtures.poison_cases("test")
        self.assertEqual(set(cases), {"truncated", "deep", "utf8", "oversized"})
        with self.assertRaises(ValueError):
            json.loads(cases["truncated"][0])
        with self.assertRaises(UnicodeDecodeError):
            cases["utf8"][0].decode("utf8")
        nested = json.loads(cases["deep"][0])
        for _ in range(66):
            self.assertIsInstance(nested, list)
            self.assertEqual(len(nested), 1)
            nested = nested[0]
        self.assertEqual(nested, 0)
        self.assertEqual(cases["deep"][0].count(b"["), 66)
        raw = cases["oversized"][0]
        self.assertGreater(len(raw), 2 << 20)
        self.assertLess(len(raw) + 2, 8 << 20)
        self.assertEqual(json.loads(raw)["report_id"], "test-oversized")

    def test_schema_fixtures_keep_same_key_and_version_order(self):
        first, second, missing = fixtures.incompatible_rows("run")
        self.assertEqual(first["report_id"], second["report_id"])
        self.assertEqual(first["level"], "Siaga")
        self.assertEqual(second["alert_level"], "Awas")
        self.assertEqual([fixtures.schema_mode(r) for r in (first, second, missing)],
                         ["rename_field", "type_change", "missing_required"])
        self.assertNotIn("eruption_count_24h", missing)

    def test_message_identity_separates_envelope_and_record(self):
        raw = fixtures.encode(fixtures.report("one"))
        self.assertNotEqual(fixtures.message_id(raw), fixtures.message_id(raw, "envelope"))
        self.assertNotEqual(fixtures.message_id(raw), fixtures.message_id(raw + b" "))

    def test_raw_validator_rejects_truncation_missing_reason_and_wrong_state(self):
        raw = b'[{"invalid":"\xff"}]'
        detail = {"message_id": fixtures.message_id(raw, "envelope"), "kind": "envelope", "source": "PVMBG",
                  "raw_base64": base64.b64encode(raw).decode(), "status": "parked", "reason": "invalid_utf8",
                  "received_at": "2026-11-19T00:00:00Z", "attempts": 1}
        support.validate_raw(detail, raw, "envelope")
        for changed in ({"raw_base64": base64.b64encode(raw[:-1]).decode()}, {"reason": ""},
                        {"status": "processed"}, {"received_at": "2026-11-19T00:00:00"}, {"attempts": 0}):
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                support.validate_raw(dict(detail, **changed), raw, "envelope")

    def test_report_ids_and_conflicting_body(self):
        import hashlib
        self.assertEqual(group3_reports.receipt_id("key"), "fr-" + hashlib.sha256(b"responder\0key").hexdigest())
        body = {"description": "original"}
        changed = fixtures.changed_report(body)
        self.assertEqual(body["description"], "original")
        self.assertNotEqual(body, changed)


class ConfigurationTests(unittest.TestCase):
    def test_media_login_uses_public_identity_and_existing_secret(self):
        with tempfile.TemporaryDirectory() as folder:
            run = make_run(folder)
            run.env = {"PUBLIC_CLIENT_SECRET": "offline-secret"}
            calls = []
            def http(service, path, **kwargs):
                calls.append((service, path, kwargs["body"]))
                return 200, {"access_token": "offline-access", "expires_in": 300}, 1
            run.http = http
            run.login_role("public")
            self.assertEqual(calls, [("auth", "/auth/token", {"client_id": "public", "client_secret": "offline-secret"})])
            self.assertEqual(run.tokens["public"], "offline-access")
            self.assertNotIn("media", run.tokens)

    def test_default_is_all_groups_with_explicit_acceptance_budget(self):
        runner.validate_config(runner.TestConfig())
        self.assertEqual(runner.CONFIG.groups, (1, 2, 3))
        self.assertEqual(runner.CONFIG.max_accept_ms, 2000)

    def test_invalid_config_fails_before_cluster_access(self):
        for changes in ({"groups": ()}, {"groups": (1, 1)}, {"groups": (4,)}, {"report_count": True},
                        {"duplicate_requests": 1}, {"recovery_seconds": 5}, {"max_accept_ms": float("nan")},
                        {"max_pages": 0}, {"max_accept_ms": 9000}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                runner.validate_config(runner.TestConfig(**changes))

    def test_fresh_process_import_has_no_old_script_dependencies(self):
        script = "import runpy,sys; sys.path.insert(0,sys.argv[1]); runpy.run_path(sys.argv[2],run_name='offline_import')"
        subprocess.run([sys.executable, "-I", "-c", script, str(P3), str(P3 / "test-problem-3.py")],
                       check=True, capture_output=True, text=True)

    def test_sql_literals_quote_data(self):
        self.assertEqual(support.sql_literal("O'Brien"), "'O''Brien'")


class EvidenceTests(unittest.TestCase):
    def test_row_lock_and_cleanup_use_the_same_primary_worker(self):
        with tempfile.TemporaryDirectory() as folder:
            run = make_run(folder)
            run.database = "coordinator"
            run.sql = lambda statement: {"host": "primary-worker", "shard": 123}
            class Input(io.StringIO):
                def close(self):
                    pass
            stream = Input()
            process = SimpleNamespace(stdin=stream, stdout=iter(["P3_LOCKED\n"]),
                                      wait=lambda **kwargs: 0)
            with patch("support.subprocess.Popen", return_value=process) as start, \
                 patch("support.subprocess.run", return_value=SimpleNamespace(returncode=0)) as cleanup:
                with run.locked_row("synthetic-key"):
                    self.assertIn("FROM hazard_events_123", stream.getvalue())
                    self.assertIn("idle_in_transaction_session_timeout=0", stream.getvalue())
                    self.assertIn("SELECT 'P3_LOCKED' FROM", stream.getvalue())
                self.assertEqual(start.call_args.args[0][-1], "primary-worker")
                self.assertEqual(cleanup.call_args.args[0][-1], "primary-worker")
                self.assertIn("application_name=", cleanup.call_args.kwargs["input"])

    def test_cursor_pages_cannot_duplicate_failed_messages(self):
        run = object.__new__(support.Run)
        run.config = SimpleNamespace(max_pages=3)
        pages = [{"items": [{"message_id": "one"}], "next_cursor": "next"},
                 {"items": [{"message_id": "one"}], "next_cursor": None}]
        run.ops = lambda _: pages.pop(0)
        with self.assertRaisesRegex(ValueError, "duplicate unprocessed"):
            run.unprocessed()

    def test_cursor_has_a_page_bound(self):
        run = object.__new__(support.Run)
        run.config = SimpleNamespace(max_pages=2)
        run.ops = lambda _: {"items": [], "next_cursor": "loop"}
        with self.assertRaisesRegex(ValueError, "pagination exceeded"):
            run.unprocessed()

    def test_poison_check_rejects_restart_or_disappeared_pod(self):
        run = object.__new__(support.Run)
        before = {"uid": {"name": "pod", "containers": {"app": 0}}}
        for after in ({}, {"uid": {"name": "pod", "containers": {"app": 1}}}):
            run.restart_snapshot = lambda: after
            with self.assertRaises(ValueError):
                run.check_restarts(before)

    def test_cleanup_failure_still_restores_other_resources(self):
        with tempfile.TemporaryDirectory() as folder:
            run = make_run(folder)
            calls = []
            run.env_changed = True
            def failing_env():
                calls.append("env")
                raise RuntimeError("restore failed")
            run.restore_env = failing_env
            run.restore = [lambda: calls.append("argo"), lambda: calls.append("fixture")]
            run.forwards = SimpleNamespace(close=lambda: calls.append("forwards"))
            with patch("support.output"), patch("common.output"):
                self.assertEqual(run.finish(0), 1)
            self.assertEqual(calls, ["env", "fixture", "argo", "forwards"])
            self.assertTrue(json.loads((Path(folder) / "result.json").read_text())["cleanup_errors"])

    def test_submission_oracle_rejects_slow_202_and_wrong_identity(self):
        receipt = {"report_id": group3_reports.receipt_id("key"), "status": "accepted", "accepted_at": "now"}
        run = SimpleNamespace(config=SimpleNamespace(max_accept_ms=2000), tokens={"responder": "fake"})
        run.http = lambda *a, **kw: (202, receipt, 2001)
        with self.assertRaisesRegex(ValueError, "202 took"):
            group3_reports.submit(run, "key", {})
        run.http = lambda *a, **kw: (202, dict(receipt, report_id="other"), 1)
        with self.assertRaisesRegex(ValueError, "invalid acceptance receipt"):
            group3_reports.submit(run, "key", {})


@unittest.skipUnless(__import__('shutil').which('curl'), 'curl required')
class LocalHTTPTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.secret = "offline-pvmbg-only"
        cls.environment = patch.dict(os.environ, {"PVMBG_TOKEN": cls.secret})
        cls.environment.start()
        cls.followed = 0
        class Handler(upstream.Handler):
            def do_GET(self):
                if self.path == "/redirect":
                    self.send_response(302)
                    self.send_header("Location", "/should-not-follow")
                    self.end_headers()
                elif self.path == "/should-not-follow":
                    cls.followed += 1
                    self.reply(200, b'{}')
                else:
                    super().do_GET()
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()
        cls.environment.stop()
        cls.temp.cleanup()

    def setUp(self):
        self.run = make_run(self.temp.name)
        self.run.url = lambda service: f"http://127.0.0.1:{self.server.server_port}"

    def test_fixture_serves_exact_invalid_upstream_bytes(self):
        raw = b'[{"bad":"\xff"}]'
        self.run.http("fixture", "/state", token=self.secret,
                      body={"raw_base64": base64.b64encode(raw).decode()})
        self.run.http("fixture", "/volcanic-reports", token=self.secret, expected=(200,))
        request = urllib.request.Request(self.run.url("fixture") + "/volcanic-reports",
                                         headers={"Authorization": "Bearer " + self.secret})
        with urllib.request.urlopen(request, timeout=2) as response:
            self.assertEqual(response.read(), raw)
        with upstream.lock:
            self.assertEqual(upstream.payload, raw)
        self.run.http("fixture", "/state", token="wrong", body={"raw_base64": ""}, expected=(401,))
        with upstream.lock:
            self.assertEqual(upstream.payload, raw)

    def test_curl_does_not_follow_redirect_with_credentials(self):
        self.run.http("fixture", "/redirect", token=self.secret, expected=(302,))
        self.assertEqual(self.followed, 0)

    def test_evidence_does_not_contain_authorization(self):
        self.run.http("fixture", "/health", token=self.secret)
        evidence = (Path(self.temp.name) / "http.jsonl").read_text()
        self.assertNotIn(self.secret, evidence)
        self.assertNotIn("Authorization", evidence)


if __name__ == "__main__":
    unittest.main()
