"""Temporarily attach a controlled feed to the existing deployment; restore on exit."""
import json
import re
import subprocess
import time
import uuid
from pathlib import Path

from common import NAMESPACE, SERVICE_PORTS, output, require

TABLES = ("seismic_events", "tsunami_warnings", "hazard_events", "poll_states", "poll_owners",
          "inbox_messages", "field_report_submissions", "field_reports", "hazard_outboxes",
          "satellite_detections", "satellite_fires", "satellite_feeds", "satellite_rejects",
          "sensor_stations", "sensor_states", "hazard_read_revision")
SCHEMA = re.compile(r"^p4_verify_[a-f0-9]{12}_(baseline|crash)$")


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def ready(pod):
    return not pod["metadata"].get("deletionTimestamp") and any(
        c.get("type") == "Ready" and c.get("status") == "True" for c in pod.get("status", {}).get("conditions", []))


class Verification:
    def __init__(self, run, baseline=None):
        self.run = run
        if baseline is not None:
            require(isinstance(baseline, dict) and re.fullmatch(r"[a-f0-9]{12}", baseline.get("id", "")), "invalid P4 restore journal")
            require(baseline.get("fixture") == "p4-feed-" + baseline["id"], "fixture journal ownership mismatch")
            require(set(baseline.get("apps", {})) <= {"platform", "dev-apps", "aat-aggregator-dev"}, "unexpected app in P4 journal")
            require(baseline.get("holder") in (None, "p4-holder-" + baseline["id"]), "unexpected lock holder in P4 journal")
            require(all(SCHEMA.fullmatch(name) and name.startswith("p4_verify_" + baseline["id"] + "_")
                        for name in baseline.get("schemas", [])), "schema journal ownership mismatch")
        self.id = baseline["id"] if baseline is not None else uuid.uuid4().hex[:12]
        self.name = "p4-feed-" + self.id
        self.token = "p4-fixture-" + self.id
        self.baseline = baseline or {"id": self.id, "fixture": self.name, "schemas": [], "apps": {}, "env": None}
        self.journal = run.folder / "restore-p4.json"
        self.holder = None

    def write_journal(self):
        # This must preserve valueFrom entries exactly, not the redacted report projection.
        self.journal.touch(mode=0o600, exist_ok=True)
        self.journal.chmod(0o600)
        self.journal.write_text(json.dumps(self.baseline, indent=2) + "\n")

    def sql_command(self):
        script = ('PGPASSWORD="$PATRONI_SUPERUSER_PASSWORD" exec psql -h 127.0.0.1 '
                  '-U "$PATRONI_SUPERUSER_USERNAME" -d "$PATRONI_CITUS_DATABASE" -w -X -qAt -v ON_ERROR_STOP=1')
        return ["kubectl", "--kubeconfig", str(self.run.config.kubeconfig), "-n", NAMESPACE,
                "--request-timeout=0", "exec", "-i", self.baseline["database"], "--", "sh", "-c", script]

    def sql(self, statement, timeout=60):
        result = subprocess.run(self.sql_command(), input=statement + "\n", text=True,
                                capture_output=True, timeout=timeout)
        require(result.returncode == 0, "P4 SQL failed: " + self.run.clean(result.stderr[-500:]))
        text = result.stdout.strip()
        return json.loads(text.splitlines()[-1]) if text else None

    def pods(self):
        return self.run.cluster.kubectl("get", "pods", "-l", "app.kubernetes.io/instance=aggregator",
                                        "-o", "json", json_output=True)["items"]

    def settle(self):
        def settled():
            deploy = self.run.cluster.kubectl("get", "deployment", "aggregator", "-o", "json", json_output=True)
            pods = self.pods(); count = deploy["spec"]["replicas"]
            return (deploy.get("status", {}).get("observedGeneration", 0) >= deploy["metadata"]["generation"]
                    and deploy.get("status", {}).get("updatedReplicas", 0) == count
                    and len(pods) == count and all(ready(p) for p in pods))
        def progress():
            pods = self.pods()
            return (f"{sum(1 for p in pods if ready(p))}/{len(pods)} Aggregator pods ready; "
                    + ", ".join(f"{p['metadata']['name']}={p.get('status', {}).get('phase')}" for p in pods))
        self.run.wait("Aggregator rollout and old-pod termination", settled, self.run.config.resume_timeout, status=progress)
        self.run.forwards.close("aggregator")
        self.run.forwards.urls.pop("aggregator", None)

    def __enter__(self):
        code, capability, _ = self.run.call("aggregator", "/internal/satellite/capabilities",
                                             token=self.run.env["AGGREGATOR_TOKEN"], expected=(200, 404))
        require(code == 200 and capability.get("controlled_verification_version") == 1,
                "deploy the PR's Aggregator image before controlled P4 checks; no deployment changes were made")
        require(not capability.get("verification_schema"), "another P4 verification is active; restore it first")
        require(self.run.env.get("ANALYST_CLIENT_SECRET"), "ANALYST_CLIENT_SECRET required for deployed dashboard checks")
        self.client_token = self.run.login("analyst")
        code, _, _ = self.run.call("client", "/satellite/stats", token=self.client_token, expected=(200, 404))
        require(code == 200, "deploy the PR's Client image before controlled P4 checks")
        pods = self.run.cluster.kubectl("get", "pods", "-o", "json", json_output=True)["items"]
        dbs = [p for p in pods if p["metadata"].get("labels", {}).get("citus-group") == "0" and ready(p)]
        require(len(dbs) == 1, "one ready Citus coordinator is required")
        self.baseline["database"] = dbs[0]["metadata"]["name"]
        self.fixture_image = dbs[0]["spec"]["containers"][0]["image"]  # Existing image already contains Python.
        deploy = self.run.cluster.kubectl("get", "deployment", "aggregator", "-o", "json", json_output=True)
        container = next(c for c in deploy["spec"]["template"]["spec"]["containers"] if c["name"] == "aggregator")
        self.baseline["env"] = container.get("env", [])
        self.baseline["image"] = container["image"]
        hpas = self.run.cluster.kubectl("get", "hpa", "-o", "json", json_output=True)["items"]
        require(not any(h["spec"]["scaleTargetRef"].get("name") == "aggregator" for h in hpas),
                "disable Aggregator HPA before the controlled run")
        role = capability.get("database_role", "")
        require(role, "Aggregator capability must identify its database role")
        self.baseline["role"] = role
        self.write_journal()
        try:
            for name in ("platform", "dev-apps", "aat-aggregator-dev"):
                app = self.run.cluster.kubectl("get", "application", name, "-n", "argocd", "-o", "json", json_output=True)
                require(not app.get("operation") and app.get("status", {}).get("operationState", {}).get("phase") != "Running",
                        "wait for Argo sync to finish before controlled P4 checks")
                self.baseline["apps"][name] = app["metadata"].get("annotations", {}).get("argocd.argoproj.io/skip-reconcile")
                self.write_journal()
                self.run.cluster.kubectl("annotate", "application", name, "-n", "argocd",
                                        "argocd.argoproj.io/skip-reconcile=true", "--overwrite")
            self.create_feed()
            return self
        except BaseException:
            self.restore()
            raise

    def __exit__(self, *_):
        self.restore()

    def create_feed(self):
        labels = {"p4-fixture": self.name}
        objects = [
            {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": self.name},
             "data": {name: (Path(__file__).parent / name).read_text() for name in ("fixture_feed.py", "p4_fixtures.py")}},
            {"apiVersion": "v1", "kind": "Service", "metadata": {"name": self.name},
             "spec": {"selector": labels, "ports": [{"port": 9000, "targetPort": 9000}]}},
            {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": self.name},
             "spec": {"replicas": 1, "selector": {"matchLabels": labels}, "template": {"metadata": {"labels": labels},
                      "spec": {"automountServiceAccountToken": False,
                               "securityContext": {"runAsNonRoot": True, "runAsUser": 10001},
                               "containers": [{"name": "feed", "image": self.fixture_image,
                                   "command": ["/opt/patroni/bin/python3", "-B", "/fixture/fixture_feed.py"],
                                   "env": [{"name": "P4_FIXTURE_TOKEN", "value": self.token}],
                                   "resources": {"requests": {"cpu": "20m", "memory": "32Mi"}, "limits": {"memory": "128Mi"}},
                                   "securityContext": {"readOnlyRootFilesystem": True, "allowPrivilegeEscalation": False,
                                                       "capabilities": {"drop": ["ALL"]}},
                                   "readinessProbe": {"httpGet": {"path": "/health", "port": 9000}},
                                   "volumeMounts": [{"name": "source", "mountPath": "/fixture", "readOnly": True}]}],
                               "volumes": [{"name": "source", "configMap": {"name": self.name}}]}}}}]
        result = subprocess.run(["kubectl", "--kubeconfig", str(self.run.config.kubeconfig), "-n", NAMESPACE,
                                 "apply", "-f", "-"], input=json.dumps({"apiVersion": "v1", "kind": "List", "items": objects}),
                                text=True, capture_output=True, timeout=60)
        require(result.returncode == 0, "could not create controlled SSE source")
        def feed_ready():
            dep = self.run.cluster.kubectl("get", "deployment", self.name, "-o", "json", json_output=True)
            return dep.get("status", {}).get("readyReplicas", 0) >= 1

        def feed_status():
            pods = self.run.cluster.kubectl("get", "pods", "-l", "p4-fixture=" + self.name, "-o", "json", json_output=True)["items"]
            return "; ".join(f"{p['metadata']['name']}: {p.get('status', {}).get('phase')} "
                             + ",".join((c.get('state', {}).get('waiting') or {}).get('reason', '')
                                        for c in p.get('status', {}).get('containerStatuses', []))
                             for p in pods) or "no fixture pod yet"
        self.run.wait("controlled SSE feed pod ready", feed_ready, 180, status=feed_status)
        SERVICE_PORTS[self.name] = 9000

    def feed(self, path, body=None):
        return self.run.call(self.name, path, token=self.token, body=body)[1]

    def prepare_schema(self, phase):
        schema = "p4_verify_" + self.id + "_" + phase
        require(SCHEMA.fullmatch(schema), "invalid verification schema")
        self.baseline["schemas"].append(schema)
        self.write_journal()  # Record ownership before creating anything.
        names = ",".join(literal(name) for name in TABLES)
        role = self.baseline["role"]
        # Copies are empty; recreate serial sequences in the owned schema rather
        # than advancing production sequences through copied nextval defaults.
        statement = f'''CREATE SCHEMA "{schema}" AUTHORIZATION "{role.replace('"', '""')}";
DO $$ DECLARE t text; c record; seq text; BEGIN
 FOR t IN SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename IN ({names}) LOOP
  EXECUTE format('CREATE TABLE %I.%I (LIKE public.%I INCLUDING ALL)', {literal(schema)}, t, t);
  FOR c IN SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=t AND column_default LIKE 'nextval(%' LOOP
   seq := t || '_' || c.column_name || '_seq';
   EXECUTE format('CREATE SEQUENCE %I.%I', {literal(schema)}, seq);
   EXECUTE format('ALTER TABLE %I.%I ALTER COLUMN %I SET DEFAULT nextval(%L::regclass)', {literal(schema)}, t, c.column_name, {literal(schema)} || '.' || seq);
   EXECUTE format('ALTER SEQUENCE %I.%I OWNED BY %I.%I.%I', {literal(schema)}, seq, {literal(schema)}, t, c.column_name);
  END LOOP;
  EXECUTE format('ALTER TABLE %I.%I OWNER TO %I', {literal(schema)}, t, {literal(role)});
 END LOOP;
END $$;
SELECT to_json(true);'''
        self.sql(statement)
        return schema

    def activate(self, phase):
        schema = self.prepare_schema(phase)
        self.feed("/admin/reset", {})
        self.run.cluster.kubectl("set", "env", "deployment/aggregator", "P4_TEST_SCHEMA=" + schema,
                                "SATELLITE_FEED_URL=http://" + self.name + ":9000", "SATELLITE_GROUP_TOKEN=" + self.token,
                                "SATELLITE_ALLOWED_LATENESS_MINUTES=360")
        self.settle()
        code, body, _ = self.run.call("aggregator", "/internal/satellite/capabilities",
                                      token=self.run.env["AGGREGATOR_TOKEN"])
        require(body.get("verification_schema") == schema, "deployed Aggregator did not activate verification isolation")
        self.schema = schema
        return schema

    def snapshot(self):
        schema = self.schema
        require(SCHEMA.fullmatch(schema), "invalid schema")
        return self.sql(f'''SET search_path TO "{schema}",public;
SELECT json_build_object(
 'hazards', coalesce((SELECT jsonb_agg(to_jsonb(h)-'ingested_at' ORDER BY source_ref_id) FROM hazard_events h), '[]'::jsonb),
 'detections', coalesce((SELECT jsonb_agg(to_jsonb(d)-'received_at' ORDER BY detection_key) FROM satellite_detections d), '[]'::jsonb),
 'fires', coalesce((SELECT jsonb_agg(to_jsonb(f) ORDER BY ref) FROM satellite_fires f), '[]'::jsonb),
 'offset', (SELECT feed_offset FROM satellite_feeds WHERE source='SATELIT'));''')

    def restore(self):
        errors = []
        try:
            self.release_lock()
        except Exception as error:
            errors.append(str(error))
        env_restored = self.baseline.get("env") is None
        if self.baseline.get("env") is not None:
            try:
                deploy = self.run.cluster.kubectl("get", "deployment", "aggregator", "-o", "json", json_output=True)
                containers = deploy["spec"]["template"]["spec"]["containers"]
                index = next(i for i, c in enumerate(containers) if c["name"] == "aggregator")
                current = containers[index].get("env", [])
                owned = next((v.get("value") for v in current if v["name"] == "P4_TEST_SCHEMA"), None)
                require((owned is None and current == self.baseline["env"]) or owned in self.baseline["schemas"],
                        "deployment env changed outside this verification; restore refused")
                self.run.cluster.kubectl("patch", "deployment", "aggregator", "--type=json", "-p", json.dumps([
                    {"op": "add", "path": f"/spec/template/spec/containers/{index}/env", "value": self.baseline["env"]}]))
                self.settle()
                env_restored = True
            except Exception as error:
                errors.append(str(error))
        if env_restored:
            try:
                self.run.forwards.close(self.baseline["fixture"])
                self.run.cluster.kubectl("delete", "deployment,service,configmap", self.baseline["fixture"], "--ignore-not-found", "--wait=false")
                for schema in self.baseline["schemas"]:
                    require(SCHEMA.fullmatch(schema) and schema.startswith("p4_verify_" + self.baseline["id"] + "_"), "schema ownership mismatch")
                    self.sql(f'DROP SCHEMA IF EXISTS "{schema}" CASCADE; SELECT to_json(true);')
            except Exception as error:
                errors.append(str(error))
            for name, old in self.baseline["apps"].items():
                try:
                    self.run.cluster.kubectl("patch", "application", name, "-n", "argocd", "--type=merge", "-p",
                                            json.dumps({"metadata": {"annotations": {"argocd.argoproj.io/skip-reconcile": old}}}))
                except Exception as error:
                    errors.append(str(error))
        self.run.save("restore-result.json", {"restored": not errors, "errors": errors})
        require(not errors, "P4 restoration incomplete; retain restore-p4.json and run --restore: " + "; ".join(errors))

    def hold_commit(self):
        self.lock_key = 40000000 + int(self.id[:6], 16)
        self.holder_name = "p4-holder-" + self.id
        self.baseline["holder"] = self.holder_name
        self.write_journal()
        script = f"SET statement_timeout=0; SET idle_session_timeout=0; SET application_name={literal(self.holder_name)}; SELECT pg_advisory_lock({self.lock_key}); SELECT pg_sleep(600);"
        self.holder = subprocess.Popen(self.sql_command(), stdin=subprocess.PIPE, stdout=subprocess.DEVNULL,
                                       stderr=subprocess.DEVNULL, text=True)
        self.holder.stdin.write(script + "\n"); self.holder.stdin.close()
        def held():
            return self.sql(f"SELECT to_json(EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid={self.lock_key} AND granted));")
        self.run.wait("pre-commit checkpoint lock", held, 15)
        self.sql(f'''CREATE FUNCTION "{self.schema}".p4_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.feed_offset >= 1 THEN PERFORM pg_advisory_xact_lock({self.lock_key}); END IF; RETURN NEW; END $$;
CREATE TRIGGER p4_checkpoint BEFORE UPDATE ON "{self.schema}".satellite_feeds FOR EACH ROW EXECUTE FUNCTION "{self.schema}".p4_checkpoint();
SELECT to_json(true);''')

    def release_lock(self):
        name = self.baseline.get("holder")
        if name and self.baseline.get("database"):
            self.sql(f"SELECT coalesce(json_agg(pg_terminate_backend(pid)), '[]'::json) FROM pg_stat_activity WHERE application_name={literal(name)} AND pid<>pg_backend_pid();")
        if self.holder is not None:
            try:
                self.holder.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.holder.terminate(); self.holder.wait(timeout=5)
            self.holder = None
