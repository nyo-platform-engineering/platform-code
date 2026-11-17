"""Offline contracts for the assignment; no cluster or VM is changed."""
import base64
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[1]
NAMESPACE = "sistem-informasi-kebencanaan-bnpb"
CLASSES = {
    "aggregator": ("aggregator", "256Mi", "512Mi"),
    "auth": ("api", "128Mi", "256Mi"),
    "client": ("api", "128Mi", "256Mi"),
    "bmkg": ("mock", "64Mi", "128Mi"),
    "pvmbg": ("mock", "64Mi", "128Mi"),
    "dashboard-updater": ("consumer", "128Mi", "256Mi"),
    "field-notifier": ("consumer", "128Mi", "256Mi"),
}


def render(chart, release, *values):
    command = ["helm", "template", release, str(ROOT / "charts" / chart), "-n", NAMESPACE]
    for value in values:
        command.extend(["-f", str(value)])
    return [d for d in yaml.safe_load_all(subprocess.check_output(command, text=True)) if d]


def load_module(name):
    folder = ROOT / "scripts"
    if name in ("runtime", "topology"):
        folder = folder / "up"
    spec = importlib.util.spec_from_file_location(name, folder / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class WorkloadTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.services = {}
        for name in CLASSES:
            folder = ROOT / "argo-apps/dev/aat" / name
            cls.services[name] = render("deployment", name, folder / "values.yaml", folder / "image.yaml")
        cls.dependencies = render("aat-dependencies", "aat-dependencies")
        cls.monitoring = render("mimir", "mimir")

    def test_all_service_images_are_immutable_and_resources_match_pdf(self):
        for name, (role, request, limit) in CLASSES.items():
            with self.subTest(service=name):
                deployment = next(d for d in self.services[name] if d["kind"] == "Deployment")
                pod = deployment["spec"]["template"]
                self.assertEqual(pod["metadata"]["labels"]["role"], role)
                container = pod["spec"]["containers"][0]
                self.assertRegex(container["image"], rf"^ghcr.io/[a-z0-9-]+/aat-apps/{name}@sha256:[a-f0-9]{{64}}$")
                self.assertEqual(container["resources"]["requests"]["memory"], request)
                self.assertEqual(container["resources"]["limits"]["memory"], limit)
                self.assertFalse(pod["spec"]["automountServiceAccountToken"])
                self.assertIn("readinessProbe", container)
                self.assertIn("livenessProbe", container)
                self.assertTrue(container["securityContext"]["readOnlyRootFilesystem"])

    def test_argo_registrations_resolve_to_the_same_runtime_values(self):
        documents = list(yaml.safe_load_all(subprocess.check_output(
            ["kubectl", "kustomize", str(ROOT / "argo-apps/dev")], text=True,
        )))
        self.assertEqual(len(documents), 8)
        self.assertEqual({d["metadata"]["name"] for d in documents},
                         {f"aat-{name}-dev" for name in (*CLASSES, "dependencies")})
        repo = ROOT.parents[1]
        for app in documents:
            self.assertEqual(app["kind"], "Application")
            self.assertEqual(app["metadata"]["namespace"], "argocd")
            self.assertEqual(app["spec"]["destination"]["namespace"], NAMESPACE)
            source = app["spec"]["source"]
            self.assertEqual(source["repoURL"], "https://github.com/nyo-platform-engineering/platform-code.git")
            chart = repo / source["path"]
            self.assertTrue((chart / "Chart.yaml").is_file())
            if source["helm"]["releaseName"] == "aat-dependencies":
                continue
            name = source["helm"]["releaseName"]
            self.assertEqual(
                [(chart / path).resolve() for path in source["helm"]["valueFiles"]],
                [(ROOT / "argo-apps/dev/aat" / name / file).resolve() for file in ("values.yaml", "image.yaml")],
            )

    def test_root_reuses_platform_apps_and_disables_gitlab(self):
        root = yaml.safe_load((ROOT / "argo-apps/root.yaml").read_text())
        self.assertTrue(root["spec"]["source"]["directory"]["recurse"])
        repo = ROOT.parents[1]
        applications = list(load_module("check-lgtm").platform_applications())
        self.assertTrue(all(app["kind"] == "Application" for app in applications))
        self.assertEqual(len(applications), 13)
        self.assertFalse((ROOT / "argo-apps/platform/kustomization.yaml").exists())
        self.assertEqual({app["metadata"]["name"] for app in applications},
                         {"argocd", "loki", "tempo", "mimir", "grafana", "otel-collector-cluster",
                          "otel-collector-daemon", "kube-state-metrics", "dev-apps",
                          "gateway-api", "traefik", "radar", "gateway"})
        for folder in ("03-cd/gitlab", "02-cd/gitlab-services", "03-observability/clickhouse",
                       "02-policy/kyverno", "03-policy/policies", "03-observability/policy-reporter"):
            self.assertTrue((ROOT / "argo-apps/platform" / folder / "app.yaml.disabled").exists())
            self.assertFalse((ROOT / "argo-apps/platform" / folder / "app.yaml").exists())
        self.assertFalse((ROOT / "charts/lgtm").exists())
        self.assertFalse((ROOT / "argo-apps/aat.yaml").exists())
        for app in applications:
            for source in app["spec"].get("sources", [app["spec"].get("source", {})]):
                for value in source.get("helm", {}).get("valueFiles", []):
                    self.assertTrue((repo / value.removeprefix("$values/")).is_file())
        dev = next(app for app in applications if app["metadata"]["name"] == "dev-apps")
        self.assertEqual(repo / dev["spec"]["source"]["path"], ROOT / "argo-apps/dev")

    def test_config_and_credentials_have_separate_sources(self):
        for name, documents in self.services.items():
            with self.subTest(service=name):
                config = next(d for d in documents if d["kind"] == "ConfigMap")
                self.assertFalse(any(re.search("PASSWORD|TOKEN|SECRET|API_KEY", key) for key in config["data"]))
                deployment = next(d for d in documents if d["kind"] == "Deployment")
                container = deployment["spec"]["template"]["spec"]["containers"][0]
                for entry in container.get("env", []):
                    self.assertEqual(entry["valueFrom"]["secretKeyRef"]["name"], "aat-runtime")
                if name in ("bmkg", "pvmbg"):
                    self.assertIn({"configMapRef": {"name": "aat-clock"}}, container["envFrom"])

    def test_gateway_routes_match_service_ports_and_cross_namespace_listener(self):
        platform = ROOT / "argo-apps/platform"
        folder = platform / "04-network/gateway/manifests"
        gateway = yaml.safe_load((folder / "gateway.yaml").read_text())
        listener = gateway["spec"]["listeners"][0]
        self.assertEqual(listener["allowedRoutes"]["namespaces"]["from"], "All")
        routes = list(yaml.safe_load_all((folder / "routes.yaml").read_text()))
        self.assertEqual({r["spec"]["hostnames"][0] for r in routes},
                         {"client.localhost", "auth.localhost", "grafana.localhost", "argocd.localhost"})
        services = {(doc["metadata"].get("namespace", NAMESPACE), doc["metadata"]["name"]):
                    {port["port"] for port in doc["spec"]["ports"]}
                    for docs in self.services.values() for doc in docs if doc["kind"] == "Service"}
        grafana = yaml.safe_load((platform / "03-observability/grafana/values.yaml").read_text())
        services[("monitoring", "grafana")] = {grafana["service"]["port"]}
        services[("argocd", "argocd-server")] = {80}
        for route in routes:
            parent = route["spec"]["parentRefs"][0]
            self.assertEqual((parent["namespace"], parent["name"], parent["sectionName"]),
                             (gateway["metadata"]["namespace"], gateway["metadata"]["name"], listener["name"]))
            for rule in route["spec"]["rules"]:
                for backend in rule["backendRefs"]:
                    key = route["metadata"]["namespace"], backend["name"]
                    self.assertIn(backend["port"], services[key])

    def test_three_aggregators_spread_across_nodes_and_safe_rollout(self):
        deployment = next(d for d in self.services["aggregator"] if d["kind"] == "Deployment")
        self.assertEqual(deployment["spec"]["replicas"], 3)
        spread = deployment["spec"]["template"]["spec"]["topologySpreadConstraints"][0]
        self.assertEqual(spread["topologyKey"], "kubernetes.io/hostname")
        self.assertEqual(spread["whenUnsatisfiable"], "DoNotSchedule")
        self.assertEqual(spread["nodeTaintsPolicy"], "Honor")
        self.assertEqual(deployment["spec"]["strategy"]["rollingUpdate"]["maxUnavailable"], 0)
        auth = next(d for d in self.services["auth"] if d["kind"] == "Deployment")
        self.assertEqual(auth["spec"]["replicas"], 1)

    def test_optional_hpa_leaves_replica_control_to_autoscaler(self):
        folder = ROOT / "argo-apps/dev/aat/aggregator"
        documents = render("deployment", "aggregator", folder / "values.yaml", folder / "image.yaml", ROOT / "argo-apps/dev/aat/aggregator/autoscaling.yaml")
        deployment = next(d for d in documents if d["kind"] == "Deployment")
        hpa = next(d for d in documents if d["kind"] == "HorizontalPodAutoscaler")
        self.assertNotIn("replicas", deployment["spec"])
        self.assertEqual(hpa["spec"]["minReplicas"], 3)
        self.assertGreaterEqual(hpa["spec"]["maxReplicas"], 5)
        self.assertEqual(hpa["spec"]["scaleTargetRef"]["name"], deployment["metadata"]["name"])

    def test_local_storage_and_owner_isolation(self):
        workloads = [d for d in self.dependencies if d["kind"] == "StatefulSet"]
        self.assertEqual(len(workloads), 2)
        for document in workloads:
            self.assertEqual(document["spec"]["volumeClaimTemplates"][0]["spec"]["storageClassName"], "local-path")
            container = document["spec"]["template"]["spec"]["containers"][0]
            self.assertEqual(container["resources"]["requests"]["memory"], "512Mi")
            self.assertEqual(container["resources"]["limits"]["memory"], "1Gi")
        policy = next(d for d in self.dependencies if d["metadata"]["name"] == "postgres-owner-only")
        allowed = policy["spec"]["ingress"][0]["from"]
        self.assertEqual(allowed, [{"podSelector": {"matchLabels": {"app.kubernetes.io/instance": "aggregator"}}}])
        self.assertEqual(policy["spec"]["ingress"][0]["ports"][0]["port"], 5432)

    def test_existing_platform_values_connect_lgtm_and_dashboard(self):
        platform = ROOT / "argo-apps/platform"
        def values(path):
            return yaml.safe_load((platform / path / "values.yaml").read_text())
        collector = values("03-observability/otel-collector-cluster")["alternateConfig"]
        self.assertEqual(collector["exporters"]["otlp_http/loki"]["endpoint"], "http://loki:3100/otlp")
        self.assertEqual(collector["exporters"]["otlp_http/tempo"]["endpoint"], "http://tempo:4318")
        self.assertEqual(set(collector["service"]["pipelines"]), {"logs", "metrics", "traces"})
        daemon = values("03-observability/otel-collector-daemon")["alternateConfig"]
        self.assertEqual(daemon["exporters"]["otlp_http"]["endpoint"], "http://otel-collector-cluster:4318")
        self.assertNotIn("clickhouse", yaml.safe_dump([collector, daemon]).lower())
        for docs in self.services.values():
            config = next(d for d in docs if d["kind"] == "ConfigMap")
            self.assertEqual(config["data"]["OTEL_EXPORTER_OTLP_ENDPOINT"], "http://otel-collector-cluster.monitoring.svc.cluster.local:4318")
        grafana = values("03-observability/grafana")
        self.assertEqual({d["uid"] for d in grafana["datasources"]["datasources.yaml"]["datasources"]}, {"mimir", "loki", "tempo"})
        self.assertEqual(grafana["admin"]["existingSecret"], "grafana-admin")
        dashboard_docs = list(yaml.safe_load_all(subprocess.check_output(
            ["kubectl", "kustomize", str(platform / "03-observability/grafana/manifests")], text=True)))
        dashboard = json.loads(dashboard_docs[0]["data"]["aat.json"])
        self.assertTrue(any("p95" in p["title"] for p in dashboard["panels"]))
        pod = next(d for d in self.monitoring if d["kind"] == "Deployment")["spec"]["template"]
        self.assertEqual(pod["metadata"]["labels"]["role"], "store")
        self.assertEqual(pod["spec"]["containers"][0]["resources"], {"requests": {"cpu": "5m", "memory": "61Mi"}, "limits": {"memory": "1Gi"}})

    def test_no_gitlab_vps_or_policy_blocks_in_local_render(self):
        documents = self.dependencies + self.monitoring + sum(self.services.values(), [])
        for document in documents:
            self.assertNotIn(document["kind"], ("ValidatingPolicy", "ClusterPolicy"))
            self.assertNotIn("gitlab", document["metadata"]["name"])
        text = yaml.safe_dump(documents)
        self.assertNotIn("kodro.id", text)
        self.assertNotIn("clickhouse", text.lower())


class CredentialTests(unittest.TestCase):
    def setUp(self):
        self.runtime = load_module("runtime")

    def test_unique_credentials_private_file_and_no_shell_evaluation(self):
        credentials = self.runtime.new_credentials()
        tokens = [credentials[key] for key in self.runtime.TOKEN_KEYS]
        self.assertEqual(len(set(tokens)), len(tokens))
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "runtime"
            credentials["EXTRA"] = "$(touch should-never-exist)"
            self.runtime.write_private(path, "".join(f"{k}={v}\n" for k, v in credentials.items()))
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(self.runtime.read_env(path), credentials)
            with self.assertRaises(FileExistsError):
                self.runtime.write_private(path, "overwrite")

    def test_rerun_reuses_credentials_and_mock_start_time(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / ".state").mkdir()
            objects = {}

            def existing(kind, name, namespace):
                return objects.get((kind.lower(), name))

            def apply(kind, name, namespace, data):
                objects[(kind.lower(), name)] = {"data": data}

            with patch.object(self.runtime, "ROOT", root), patch.object(self.runtime, "existing", side_effect=existing), patch.object(self.runtime, "apply", side_effect=apply):
                self.runtime.ensure()
                first = (root / ".env.runtime").read_bytes()
                clock = (root / ".state/mock-start-time").read_bytes()
                self.runtime.ensure()
                self.assertEqual((root / ".env.runtime").read_bytes(), first)
                self.assertEqual((root / ".state/mock-start-time").read_bytes(), clock)
                self.assertEqual(set(objects[("secret", "aat-runtime")]["data"]), self.runtime.REQUIRED)

    def test_recover_existing_cluster_credentials_without_rotation(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / ".state").mkdir()
            credentials = self.runtime.new_credentials()
            old = {"data": {key: base64.b64encode(value.encode()).decode() for key, value in credentials.items()}}
            with patch.object(self.runtime, "ROOT", root), patch.object(self.runtime, "existing", side_effect=lambda kind, name, ns: old if name == "aat-runtime" else None), patch.object(self.runtime, "apply"):
                self.runtime.ensure()
                self.assertEqual(self.runtime.read_env(root / ".env.runtime"), credentials)


class TopologyTests(unittest.TestCase):
    def test_reject_container_clusters_and_too_few_nodes(self):
        topology = load_module("topology")
        real = [{"metadata": {"name": f"aat-{i}"}, "status": {"nodeInfo": {"osImage": "Ubuntu 24.04.3 LTS"}}} for i in range(3)]
        self.assertTrue(topology.validate(real))
        with self.assertRaises(ValueError):
            topology.validate(real[:2])
        real[0]["metadata"]["name"] = "k3d-dev-server-0"
        with self.assertRaises(ValueError):
            topology.validate(real)


if __name__ == "__main__":
    unittest.main()
