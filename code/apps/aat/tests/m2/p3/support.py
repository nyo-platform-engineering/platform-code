"""P3 cluster access, HTTP, evidence and cleanup."""
from contextlib import contextmanager, nullcontext
import base64
import datetime as dt
import json
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from common import INFRA, NAMESPACE, ROOT, Run as CommonRun, output, utc, require
from run_progress import Progress
from fixtures import encode, message_id, schema_mode


def sql_literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def ready(pod):
    return not pod["metadata"].get("deletionTimestamp") and any(
        c.get("type") == "Ready" and c.get("status") == "True"
        for c in pod.get("status", {}).get("conditions", []))


def validate_raw(detail, raw, kind, status="parked"):
    require(detail.get("message_id") == message_id(raw, kind), "inbox identity differs from original bytes")
    require(detail.get("source") == "PVMBG" and detail.get("kind") == kind, "wrong inbox source/kind")
    require(base64.b64decode(detail.get("raw_base64", ""), validate=True) == raw, "raw payload was changed or truncated")
    require(detail.get("status") == status and detail.get("reason"), "missing failure state/reason")
    stamp = dt.datetime.fromisoformat(detail["received_at"].replace("Z", "+00:00"))
    require(stamp.tzinfo is not None and detail.get("attempts", 0) >= 1, "missing capture time/processing attempt")


class Run(CommonRun):
    def __init__(self, config, folder=None):
        super().__init__(3, config, [
            "Official Tester Agent and exam inputs",
            "Report: schema policy, retry/parking, per-key ordering and delivery semantics",
            "Recorded demo and physical storage durability beyond this bounded crash scenario",
        ], folder, evidence_root=Path(__file__).resolve().parent / "evidence")
        self.name = "m2-p3-" + uuid.uuid4().hex[:10]
        self.report["full_demo_certified"] = False
        self.report["selected_groups"] = list(config.groups)
        self.report["configured_limits"] = {"request_timeout_seconds": config.request_timeout,
            "recovery_seconds": config.recovery_seconds, "max_accept_ms": config.max_accept_ms,
            "retry_observe_seconds": config.retry_observe_seconds,
            "report_count": config.report_count, "duplicate_requests": config.duplicate_requests}
        self.restore = []
        self.replay_ids = set()
        self.tokens = {}
        self.token_roles, self.token_expiry = {}, {}
        self.auth_lock = threading.RLock()
        self.original_env = None
        self.env_changed = False
        self.progress = None

    def stage(self, name, operation, *args, **kwargs):
        with Progress(self.folder, name, clean=self.clean, writer=lambda text: output("WAIT", text)) as progress:
            self.progress = progress
            try:
                return super().stage(name, operation, *args, **kwargs)
            finally:
                self.progress = None

    def phase(self, description, timeout=None, quiet=False, announce=True):
        progress = getattr(self, "progress", None)
        return progress.phase(description, timeout, quiet, announce) if progress else nullcontext()

    def status(self, message):
        if getattr(self, "progress", None):
            self.progress.detail(message)

    def wait(self, description, check, timeout):
        with self.phase(description, timeout):
            return super().wait(description, check, timeout)

    def prompt(self, text):
        with self.phase("waiting for Enter", quiet=True):
            input("\n" + text + "\nPress Enter to continue: ")

    def http(self, service, path, token=None, body=None, raw=None, method=None,
             headers=None, expected=(200,), record=True):
        if service == "client" and token in self.token_roles:
            role = self.token_roles[token]
            with self.auth_lock:
                if time.monotonic() >= self.token_expiry[role]:
                    self.login_role(role)
                token = self.tokens[role]
        url = self.url(service) + path
        parsed = urllib.parse.urlsplit(url)
        require(parsed.scheme in ("http", "https") and parsed.hostname and not parsed.username
                and not parsed.password and not parsed.fragment, "invalid service URL")
        values = {"Content-Type": "application/json", "X-Correlation-ID": self.name}
        if token:
            values["Authorization"] = "Bearer " + token
        values.update(headers or {})
        require(all("\n" not in str(v) and "\r" not in str(v) for v in values.values()), "invalid HTTP header")
        data = encode(body) if body is not None else raw
        with tempfile.TemporaryDirectory(prefix="aat-p3-http-") as folder:
            response = Path(folder) / "response"
            args = ["curl", "--disable", "--silent", "--show-error", "--noproxy", "*",
                    "--proto", "=http,https", "--max-time", str(self.config.request_timeout),
                    "--max-filesize", str(12 << 20), "--header", "@-", "--output", str(response),
                    "--write-out", "%{http_code}", "--request", method or ("POST" if data is not None else "GET")]
            if data is not None:
                payload = Path(folder) / "request"
                payload.write_bytes(data)
                args += ["--data-binary", "@" + str(payload)]
            started = time.monotonic()
            with self.phase(f"HTTP {service} {path.split('?')[0]}", self.config.request_timeout, announce=False):
                result = subprocess.run(args + ["--url", url], input="".join(f"{k}: {v}\n" for k, v in values.items()),
                                        text=True, capture_output=True, timeout=self.config.request_timeout + 2)
            elapsed = (time.monotonic() - started) * 1000
            require(result.returncode == 0, f"{service} {path.split('?')[0]}: curl exit {result.returncode}")
            status = int(result.stdout)
            raw_response = response.read_bytes()
        if record:
            with self.lock, (self.folder / "http.jsonl").open("a") as stream:
                stream.write(json.dumps({"at": utc(), "service": service, "path": path,
                                         "status": status, "ms": round(elapsed, 2)}) + "\n")
        require(status in expected, f"{service} {path.split('?')[0]}: HTTP {status}, expected {expected}")
        try:
            value = json.loads(raw_response)
        except ValueError:
            value = None
        return status, value, elapsed

    def ops(self, path, **kwargs):
        return self.http("client", "/ops/" + path, token=self.tokens["analyst"], **kwargs)[1]

    def detail(self, identity):
        status, value, _ = self.http("client", "/ops/unprocessed/" + identity,
                                     token=self.tokens["analyst"], expected=(200, 404))
        if status == 200 and value:
            self.status(f"message={identity[:12]}; state={value.get('status')}; attempts={value.get('attempts')}")
        return value if status == 200 else None

    def unprocessed(self):
        with self.phase("reading unprocessed-message pages", announce=False):
            return self._unprocessed()

    def _unprocessed(self):
        rows, cursor, seen = [], "", set()
        for _ in range(self.config.max_pages):
            path = "unprocessed?" + urllib.parse.urlencode({"source": "PVMBG", "limit": 1000, "cursor": cursor})
            page = self.ops(path)
            self.status(f"page={_ + 1}; messages read={len(rows) + len(page.get('items', []))}")
            require(isinstance(page, dict) and isinstance(page.get("items"), list), "invalid unprocessed page")
            for row in page["items"]:
                require(row["message_id"] not in seen, "duplicate unprocessed message")
                seen.add(row["message_id"])
                rows.append(row)
            cursor = page.get("next_cursor")
            if not cursor:
                return rows
        raise ValueError("unprocessed pagination exceeded CONFIG.max_pages")

    def sql_command(self, host="127.0.0.1"):
        # Read credentials inside the database pod.
        script = ('PGPASSWORD="$PATRONI_SUPERUSER_PASSWORD" exec psql -h "$1" '
                  '-U "$PATRONI_SUPERUSER_USERNAME" -d "$PATRONI_CITUS_DATABASE" -w -X -qAt -v ON_ERROR_STOP=1')
        return ["kubectl", "--kubeconfig", str(self.config.kubeconfig), "-n", NAMESPACE,
                "--request-timeout=0", "exec", "-i", self.database, "--", "sh", "-c", script, "p3-sql", host]

    def sql(self, statement):
        with self.phase("reading database evidence", self.config.request_timeout + 3, announce=False):
            result = subprocess.run(self.sql_command(), input=statement + "\n", text=True, capture_output=True,
                                    timeout=self.config.request_timeout + 3)
        require(result.returncode == 0, "database evidence query failed (details withheld to protect credentials)")
        return json.loads(result.stdout.strip())

    def hazard(self, ref):
        rows = self.sql("SELECT coalesce(json_agg(t),'[]'::json) FROM (SELECT id,source_ref_id,severity,attributes "
                        "FROM hazard_events WHERE source='PVMBG' AND source_ref_id=" + sql_literal(ref) + ") t;")
        require(len(rows) <= 1, "duplicate canonical PVMBG identity")
        return rows[0] if rows else None

    def pods(self):
        return self.cluster.kubectl("get", "pods", "-o", "json", json_output=True)["items"]

    def restart_snapshot(self):
        return {p["metadata"]["uid"]: {"name": p["metadata"]["name"], "containers": {
            s["name"]: s.get("restartCount", 0) for s in p.get("status", {}).get("containerStatuses", [])}}
                for p in self.pods() if not p["metadata"].get("deletionTimestamp")
                and p["metadata"].get("labels", {}).get("p3-fixture") != self.name}

    def check_restarts(self, before):
        after = self.restart_snapshot()
        require(set(before) <= set(after), "application pod disappeared during poison test")
        require(all(after[k]["containers"] == value["containers"] for k, value in before.items()),
                "application container restarted during poison test")
        return after

    def set_env(self, **values):
        self.env_changed = True
        self.cluster.kubectl("set", "env", "deployment/aggregator", *(f"{k}={v}" for k, v in values.items()))
        self.settled()

    def settled(self):
        def check():
            deploy = self.cluster.kubectl("get", "deployment", "aggregator", "-o", "json", json_output=True)
            count = deploy["spec"]["replicas"]
            pods = [p for p in self.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"]
            self.status(f"target={count}; updated={deploy.get('status', {}).get('updatedReplicas', 0)}; "
                        f"Ready={sum(ready(p) for p in pods)}/{len(pods)}; "
                        + ", ".join(p["metadata"]["name"] + ":" + p.get("status", {}).get("phase", "Unknown")
                                    for p in pods if not ready(p)))
            return (deploy.get("status", {}).get("observedGeneration", 0) >= deploy["metadata"]["generation"]
                    and deploy["status"].get("updatedReplicas", 0) == count and len(pods) == count
                    and all(ready(p) for p in pods))
        self.wait("Aggregator rollout and old pod termination", check, self.config.recovery_seconds)
        self.forwards.close("aggregator")
        self.forwards.urls.pop("aggregator", None)

    def mock_mode(self, mode):
        return self.http("pvmbg", "/admin/schema-version", token=self.env["PVMBG_TOKEN"], body={"mode": mode})[1]

    def login_role(self, identity):
        _, pair, _ = self.http("auth", "/auth/token", body={"client_id": identity,
                               "client_secret": self.env[identity.upper() + "_CLIENT_SECRET"]})
        require(isinstance(pair, dict) and pair.get("access_token") and pair.get("expires_in", 0) > 0,
                "invalid login response")
        self.tokens[identity] = pair["access_token"]
        self.token_roles[pair["access_token"]] = identity
        self.token_expiry[identity] = time.monotonic() + max(0, pair["expires_in"] - 5)
        self.secrets.update(value for value in (pair.get("access_token"), pair.get("refresh_token")) if value)

    def preflight(self):
        require(shutil.which("curl"), "curl is required")
        self.verify_environment(("AGGREGATOR_TOKEN", "AUDIT_TOKEN", "PVMBG_TOKEN", "ANALYST_CLIENT_SECRET",
                                 "RESPONDER_CLIENT_SECRET", "PUBLIC_CLIENT_SECRET"))
        require(self.env["AUDIT_TOKEN"] != self.env["AGGREGATOR_TOKEN"], "Audit token must be distinct")
        for identity in ("analyst", "responder", "public"):
            self.login_role(identity)
        self.secrets.discard("")
        from common import SERVICE_PORTS
        SERVICE_PORTS["pvmbg"] = 8082
        pods = self.pods()
        databases = [p for p in pods if p["metadata"].get("labels", {}).get("citus-group") == "0" and ready(p)]
        require(len(databases) == 1, "need one Ready Citus coordinator for read-only evidence")
        self.database = databases[0]["metadata"]["name"]
        self.fixture_image = databases[0]["spec"]["containers"][0]["image"]
        self.sql("SELECT 'true'::json;")
        since = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=2)).isoformat()
        _, sample, _ = self.http("pvmbg", "/volcanic-reports?" + urllib.parse.urlencode({"since": since}),
                                  token=self.env["PVMBG_TOKEN"])
        require(isinstance(sample, list) and sample, "PVMBG must be healthy with recent mock events")
        modes = {schema_mode(row) for row in sample}
        require(len(modes) == 1, "cannot determine original mock schema mode")
        self.original_mock = modes.pop()
        deployment = self.cluster.kubectl("get", "deployment", "aggregator", "-o", "json", json_output=True)
        require(deployment["spec"].get("replicas", 0) > 0, "Aggregator must have replicas")
        hpas = self.cluster.kubectl("get", "hpa", "-o", "json", json_output=True)["items"]
        require(not any(h["spec"]["scaleTargetRef"].get("name") == "aggregator" for h in hpas), "disable Aggregator HPA first")
        container = next(c for c in deployment["spec"]["template"]["spec"]["containers"] if c["name"] == "aggregator")
        self.container_index = deployment["spec"]["template"]["spec"]["containers"].index(container)
        self.original_env = container.get("env", [])
        self.baseline_unprocessed = {row["message_id"] for row in self.unprocessed()}
        self.prompt("P3 changes PVMBG schema and Aggregator env, serves malformed upstream bytes, and briefly locks "
                    "one synthetic row. Group 3 disables processing then kills every Aggregator pod after 202. "
                    "Use an isolated lab; do not run alongside P1/P2/P4/P5. Test data is retained; no disks are erased.")
        baseline_apps = {}
        for app in ("platform", "dev-apps", "aat-aggregator-dev"):
            current = self.cluster.kubectl("get", "application", app, "-n", "argocd", "-o", "json", json_output=True)
            require(not current.get("operation") and current.get("status", {}).get("operationState", {}).get("phase") != "Running",
                    "wait for Argo sync to finish before P3 mutations")
            old = current["metadata"].get("annotations", {}).get("argocd.argoproj.io/skip-reconcile")
            baseline_apps[app] = old
            self.restore.append(lambda app=app, old=old: self.cluster.kubectl("patch", "application", app, "-n", "argocd",
                "--type=merge", "-p", json.dumps({"metadata": {"annotations": {"argocd.argoproj.io/skip-reconcile": old}}})))
            self.cluster.kubectl("annotate", "application", app, "-n", "argocd", "argocd.argoproj.io/skip-reconcile=true", "--overwrite")
        self.restore.append(lambda: self.mock_mode(self.original_mock))
        self.save("baseline.json", {"aggregator_env": self.original_env,
                                   "container_index": self.container_index, "mock_mode": self.original_mock,
                                   "argo_skip_reconcile": baseline_apps})
        return {"groups": self.config.groups, "original_mock_mode": self.original_mock,
                "mutations": "isolated test configuration; finally restores env/schema/GitOps"}

    def restore_env(self):
        # Replace env to remove temporary overrides.
        self.cluster.kubectl("patch", "deployment", "aggregator", "--type=json", "-p", json.dumps([
            {"op": "add", "path": f"/spec/template/spec/containers/{self.container_index}/env", "value": self.original_env}]))
        self.settled()

    def create_fixture(self):
        name = self.name
        labels = {"p3-fixture": name}
        source = (Path(__file__).with_name("upstream.py")).read_text()
        objects = [
            {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": name}, "data": {"server.py": source}},
            {"apiVersion": "v1", "kind": "Service", "metadata": {"name": name},
             "spec": {"selector": labels, "ports": [{"port": 8082, "targetPort": 8082}]}},
            {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": name}, "spec": {
                "replicas": 1, "selector": {"matchLabels": labels}, "template": {"metadata": {"labels": labels}, "spec": {
                    "securityContext": {"runAsNonRoot": True, "runAsUser": 10001},
                    "containers": [{"name": "fixture", "image": self.fixture_image, "imagePullPolicy": "IfNotPresent",
                        "command": ["/opt/patroni/bin/python3", "-B", "/fixture/server.py"],
                        "env": [{"name": "PVMBG_TOKEN", "valueFrom": {"secretKeyRef": {"name": "aat-runtime", "key": "PVMBG_TOKEN"}}}],
                        "resources": {"requests": {"cpu": "20m", "memory": "32Mi"}, "limits": {"cpu": "250m", "memory": "128Mi"}},
                        "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True},
                        "readinessProbe": {"httpGet": {"path": "/health", "port": 8082}},
                        "volumeMounts": [{"name": "script", "mountPath": "/fixture", "readOnly": True}]}],
                    "volumes": [{"name": "script", "configMap": {"name": name}}]}}}},
        ]
        self.restore.append(lambda: self.cluster.kubectl("delete", "deployment,service,configmap", name, "--ignore-not-found", "--wait=false"))
        manifest = json.dumps({"apiVersion": "v1", "kind": "List", "items": objects})
        args = ["kubectl", "--kubeconfig", str(self.config.kubeconfig), "-n", NAMESPACE, "apply", "-f", "-"]
        result = subprocess.run(args, input=manifest, capture_output=True, text=True, timeout=30)
        require(result.returncode == 0, "cannot create temporary upstream fixture")
        self.wait("temporary upstream", lambda: self.cluster.ready_replicas(name) == 1, self.config.recovery_seconds)
        from common import SERVICE_PORTS
        SERVICE_PORTS[name] = 8082
        self.set_env(PVMBG_URL=f"http://{name}:8082", PVMBG_SCHEMA_POLICY="strict")

    def serve(self, raw):
        self.http(self.name, "/state", token=self.env["PVMBG_TOKEN"],
                  body={"raw_base64": base64.b64encode(raw).decode()})

    def captured(self, raw, kind, status="parked"):
        identity = message_id(raw, kind)
        def found():
            detail = self.detail(identity)
            return detail if detail and detail.get("status") == status else None
        detail = self.wait("inbox " + identity[:12] + " " + status, found, self.config.recovery_seconds)
        validate_raw(detail, raw, kind, status)
        self.save("inbox-" + identity + ".json", detail)
        return detail

    @contextmanager
    def locked_row(self, ref):
        app = self.name + "-lock"
        placement = self.sql("SELECT json_build_object('host',n.nodename,'shard',p.shardid) "
            "FROM pg_dist_placement p JOIN pg_dist_node n USING(groupid) WHERE n.noderole='primary' AND n.isactive "
            "AND p.shardid=get_shard_id_for_distribution_column('hazard_events'," + sql_literal(ref) + "::text) LIMIT 1;")
        require(placement and type(placement.get("shard")) is int and placement["shard"] > 0,
                "cannot find primary shard for synthetic row")
        table = "hazard_events_" + str(placement["shard"])
        sql = ("SET application_name=" + sql_literal(app) + ";\nBEGIN;\n"
               "SET LOCAL lock_timeout='3s';\nSET LOCAL statement_timeout='105s';\n"
               "SET LOCAL idle_in_transaction_session_timeout=0;\n"
               "SELECT 'P3_LOCKED' FROM " + table + " WHERE source_ref_id=" + sql_literal(ref) + " FOR UPDATE;\n"
               "SELECT pg_sleep(90);\nROLLBACK;\n")
        process = subprocess.Popen(self.sql_command(placement["host"]), stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.DEVNULL, text=True)
        signals = queue.Queue()
        def read():
            for line in process.stdout:
                if line.strip() == "P3_LOCKED":
                    signals.put(True)
            signals.put(False)
        thread = threading.Thread(target=read, daemon=True)
        thread.start()
        try:
            process.stdin.write(sql)
            process.stdin.close()
            try:
                with self.phase("acquiring synthetic row lock on primary worker", 10):
                    acquired = signals.get(timeout=10)
            except queue.Empty:
                acquired = False
            require(acquired, "could not lock the synthetic test row")
            yield process
        finally:
            cleanup = "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name=" + sql_literal(app) + ";\n"
            try:
                with self.phase("releasing synthetic worker row lock", self.config.request_timeout + 3):
                    result = subprocess.run(self.sql_command(placement["host"]), input=cleanup, text=True,
                                            capture_output=True, timeout=self.config.request_timeout + 3)
                require(result.returncode == 0, "could not release synthetic worker row lock; automatic expiry is 90 seconds")
            finally:
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                thread.join(timeout=3)

    def finish(self, exit_code):
        with Progress(self.folder, "cleanup", clean=self.clean, writer=lambda text: output("WAIT", text)) as progress:
            self.progress = progress
            try:
                return self.restore_run(exit_code)
            finally:
                self.progress = None

    def restore_run(self, exit_code):
        errors = []
        if self.env_changed:
            try:
                with self.phase("restoring original Aggregator env and waiting for rollout"):
                    self.restore_env()
            except BaseException as error:
                errors.append("restore_env: " + type(error).__name__)
        for index, operation in enumerate(reversed(self.restore), 1):
            try:
                with self.phase(f"restoring mock/fixture/GitOps settings {index}/{len(self.restore)}"):
                    operation()
            except BaseException as error:
                errors.append(type(error).__name__)
        self.report["cleanup_errors"] = errors
        if errors:
            self.report["status"] = "cleanup_failed"
            output("FAIL", "Cleanup incomplete; restore Aggregator env, mock schema, fixture and Argo annotations before rerunning")
            exit_code = 1
        super().finish(exit_code)
        return exit_code
