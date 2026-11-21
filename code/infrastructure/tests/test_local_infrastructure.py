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

    def test_workloads_have_no_cpu_limits_and_keep_cpu_requests(self):
        documents = self.dependencies + self.monitoring + sum(self.services.values(), [])
        for document in documents:
            pod = document.get("spec", {}).get("template", {}).get("spec", {})
            for field in ("containers", "initContainers", "ephemeralContainers"):
                for container in pod.get(field, []):
                    with self.subTest(workload=document["metadata"]["name"], container=container["name"]):
                        resources = container.get("resources", {})
                        self.assertNotIn("cpu", resources.get("limits", {}))
                        if field == "containers":
                            self.assertIn("cpu", resources["requests"])
        radar = yaml.safe_load((ROOT / "argo-apps/platform/02-monitoring/radar/values.yaml").read_text())
        self.assertNotIn("cpu", radar["resources"]["limits"])
        self.assertEqual(radar["resources"]["requests"]["cpu"], "3m")

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
        self.assertEqual(len(applications), 15)
        self.assertFalse((ROOT / "argo-apps/platform/kustomization.yaml").exists())
        self.assertEqual({app["metadata"]["name"] for app in applications},
                         {"argocd", "loki", "tempo", "mimir", "grafana", "otel-collector-cluster",
                          "otel-collector-daemon", "kube-state-metrics", "dev-apps",
                          "gateway-api", "traefik", "radar", "gateway", "kyverno", "cluster-policies"})
        for folder in ("03-cd/gitlab", "02-cd/gitlab-services", "03-observability/clickhouse",
                       "03-observability/policy-reporter"):
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

    def test_kyverno_enables_only_cpu_limit_mutation(self):
        platform = ROOT / "argo-apps/platform"
        folder = platform / "03-policy/policies/manifests"
        documents = [d for path in folder.glob("*.yaml") for d in yaml.safe_load_all(path.read_text()) if d]
        self.assertEqual([(d["kind"], d["metadata"]["name"]) for d in documents],
                         [("ClusterPolicy", "remove-cpu-limits")])
        self.assertEqual(len(documents[0]["spec"]["rules"]), 1)
        self.assertFalse(documents[0]["spec"]["background"])
        for name in ("policies", "ownership", "ownership-reporting"):
            self.assertTrue((folder / f"{name}.yaml.disabled").exists())
        config = yaml.safe_load((platform / "02-policy/kyverno/values.yaml").read_text())
        for controller in ("backgroundController", "cleanupController", "reportsController"):
            self.assertFalse(config[controller]["enabled"])
        self.assertEqual(config["config"]["resourceFilters"], [])
        self.assertFalse(config["config"]["excludeKyvernoNamespace"])
        self.assertEqual(config["config"]["webhooks"]["namespaceSelector"]["matchExpressions"], [])

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
        self.assertEqual(spread["matchLabelKeys"], ["pod-template-hash"])
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
        self.assertEqual(len(workloads), 8)
        for document in workloads:
            self.assertEqual(document["spec"]["volumeClaimTemplates"][0]["spec"]["storageClassName"], "local-path")
            container = document["spec"]["template"]["spec"]["containers"][0]
            self.assertEqual(container["resources"]["requests"]["memory"], "512Mi")
            self.assertEqual(container["resources"]["limits"]["memory"], "1Gi")
        policy = next(d for d in self.dependencies if d["metadata"]["name"] == "postgres-owner-only")
        allowed = policy["spec"]["ingress"][0]["from"]
        self.assertEqual(allowed, [
            {"podSelector": {"matchLabels": {"app.kubernetes.io/instance": "aggregator"}}},
            {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "aat-citus"}}},
            {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "tester-agent"}}},
        ])
        self.assertEqual(policy["spec"]["ingress"][0]["ports"][0]["port"], 5432)

    def test_citus_replica_placement_and_patroni_configuration(self):
        storage = [d for d in self.dependencies if d["kind"] == "StatefulSet" and d["metadata"]["name"].startswith("citus-")]
        groups = {}
        per_node = {}
        for document in storage:
            pod = document["spec"]["template"]
            self.assertEqual(pod["metadata"]["labels"]["role"], "store")
            group = pod["metadata"]["labels"]["citus-group"]
            node = pod["spec"]["nodeSelector"]["kubernetes.io/hostname"]
            groups.setdefault(group, []).append(node)
            if group != "0":
                per_node.setdefault(node, set()).add(group)
            self.assertEqual(pod["spec"]["securityContext"]["fsGroupChangePolicy"], "OnRootMismatch")
        self.assertEqual(len(groups["0"]), 1)
        for group in ("1", "2", "3"):
            self.assertEqual(len(groups[group]), 2)
            self.assertEqual(len(set(groups[group])), 2)
        self.assertTrue(all(len(groups) == 2 for groups in per_node.values()))
        config = next(d for d in self.dependencies if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "aat-citus")
        self.assertEqual(len(json.loads(config["data"]["members.json"])), 6)
        patroni = yaml.safe_load(config["data"]["patroni.yaml"])
        self.assertTrue(patroni["bootstrap"]["dcs"]["synchronous_mode"])
        self.assertEqual(patroni["kubernetes"]["role_label"], "patroni-role")
        self.assertTrue(patroni["kubernetes"]["use_endpoints"])

    def test_existing_platform_values_connect_lgtm_and_dashboard(self):
        platform = ROOT / "argo-apps/platform"
        def values(path):
            return yaml.safe_load((platform / path / "values.yaml").read_text())
        collector = values("03-observability/otel-collector-cluster")["alternateConfig"]
        self.assertEqual(collector["exporters"]["otlp_http/loki"]["endpoint"], "http://loki:3100/otlp")
        self.assertEqual(collector["exporters"]["otlp_http/tempo"]["endpoint"], "http://tempo:4318")
        self.assertEqual(set(collector["service"]["pipelines"]), {"logs", "metrics", "metrics/nats_jetstream", "traces"})
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

    def test_jetstream_scrape_keeps_only_pending_in_its_own_pipeline(self):
        values = yaml.safe_load((ROOT / "argo-apps/platform/03-observability/otel-collector-cluster/values.yaml").read_text())
        config = values["alternateConfig"]
        receiver = config["receivers"]["prometheus/nats_jetstream"]
        job = receiver["config"]["scrape_configs"][0]
        self.assertEqual(job["metric_relabel_configs"], [{"source_labels": ["__name__"],
                         "regex": "jetstream_consumer_num_pending", "action": "keep"}])
        self.assertEqual(config["processors"]["filter/nats_jetstream"]["metric_conditions"],
                         ['metric.name != "jetstream_consumer_num_pending"'])
        pipelines = config["service"]["pipelines"]
        self.assertEqual(pipelines["metrics/nats_jetstream"]["receivers"], ["prometheus/nats_jetstream"])
        self.assertNotIn("filter/nats_jetstream", pipelines["metrics"]["processors"])
        nats = next(d for d in self.dependencies if d["kind"] == "StatefulSet" and d["metadata"]["name"] == "nats")
        exporter = next(c for c in nats["spec"]["template"]["spec"]["containers"] if c["name"] == "prometheus-exporter")
        self.assertIn("http://127.0.0.1:8222", exporter["args"])
        service = next(d for d in self.dependencies if d["kind"] == "Service" and d["metadata"]["name"] == "nats")
        self.assertEqual({p["port"] for p in service["spec"]["ports"]}, {4222, 7777})
        policy = next(d for d in self.dependencies if d["kind"] == "NetworkPolicy" and d["metadata"]["name"] == "nats-publishers-consumers-only")
        scrape = next(rule for rule in policy["spec"]["ingress"] if rule["ports"] == [{"protocol": "TCP", "port": 7777}])
        self.assertEqual(scrape["from"][0]["namespaceSelector"]["matchLabels"]["kubernetes.io/metadata.name"], "monitoring")

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
                self.assertEqual(set(objects[("secret", "aat-runtime")]["data"]), self.runtime.REQUIRED | {"POSTGRES_REPLICATION_PASSWORD", "AUDIT_TOKEN"})

    def test_recover_existing_cluster_credentials_without_rotation(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / ".state").mkdir()
            credentials = self.runtime.new_credentials()
            old = {"data": {key: base64.b64encode(value.encode()).decode() for key, value in credentials.items()}}
            with patch.object(self.runtime, "ROOT", root), patch.object(self.runtime, "existing", side_effect=lambda kind, name, ns: old if name == "aat-runtime" else None), patch.object(self.runtime, "apply"):
                self.runtime.ensure()
                recovered = self.runtime.read_env(root / ".env.runtime")
                self.assertEqual({key: recovered[key] for key in credentials}, credentials)
                self.assertEqual(recovered["START_TIME"], (root / ".state/mock-start-time").read_text().strip())

    def test_explicit_mock_start_time_overrides_old_state_without_rotating_credentials(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / ".state").mkdir()
            credentials = self.runtime.new_credentials()
            configured = "2026-11-18T07:00:00+07:00"
            credentials["START_TIME"] = configured
            self.runtime.write_private(root / ".env.runtime", "".join(f"{k}={v}\n" for k, v in credentials.items()))
            (root / ".state/mock-start-time").write_text("2026-11-17T00:00:00Z\n")
            with patch.object(self.runtime, "ROOT", root), patch.object(self.runtime, "apply") as apply:
                self.runtime.ensure_aat_credentials()
                secret_data = apply.call_args.args[3]
                self.assertNotIn("START_TIME", secret_data)
                self.runtime.ensure_mock_start_time()
                self.assertEqual(self.runtime.read_env(root / ".env.runtime"), credentials)
                self.assertEqual((root / ".state/mock-start-time").read_text(), configured + "\n")
                self.assertEqual(apply.call_args.args[3], {"START_TIME": configured})

    def test_missing_env_start_time_recovers_local_or_cluster_time(self):
        for local in (True, False):
            with self.subTest(local=local), tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                (root / ".state").mkdir()
                credentials = self.runtime.new_credentials()
                credentials["START_TIME"] = ""
                self.runtime.write_private(root / ".env.runtime", "# retained comment\n" + "".join(f"{k}={v}\n" for k, v in credentials.items()))
                start = "2026-11-17T05:20:09.790155Z"
                if local:
                    (root / ".state/mock-start-time").write_text(start + "\n")
                with patch.object(self.runtime, "ROOT", root), patch.object(self.runtime, "existing", return_value={"data": {"START_TIME": start}}), patch.object(self.runtime, "apply"):
                    self.runtime.ensure_mock_start_time()
                    saved = self.runtime.read_env(root / ".env.runtime")
                    self.assertEqual(saved, dict(credentials, START_TIME=start))
                    self.assertTrue((root / ".env.runtime").read_text().startswith("# retained comment\n"))
                    self.assertEqual((root / ".env.runtime").stat().st_mode & 0o777, 0o600)
                    self.assertEqual((root / ".state/mock-start-time").read_text(), start + "\n")

    def test_invalid_start_time_does_not_change_clock_or_apply_config(self):
        for value in ("2026-11-18", "2026-11-18T07:00:00", "2026-02-30T00:00:00Z", "2026-11-18T00:00:00+25:00"):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as folder:
                root = Path(folder)
                (root / ".state").mkdir()
                credentials = dict(self.runtime.new_credentials(), START_TIME=value)
                self.runtime.write_private(root / ".env.runtime", "".join(f"{k}={v}\n" for k, v in credentials.items()))
                before = (root / ".env.runtime").read_bytes()
                with patch.object(self.runtime, "ROOT", root), patch.object(self.runtime, "apply") as apply:
                    with self.assertRaises(ValueError):
                        self.runtime.ensure_mock_start_time()
                    apply.assert_not_called()
                    self.assertEqual((root / ".env.runtime").read_bytes(), before)
                    self.assertFalse((root / ".state/mock-start-time").exists())

    def test_upgrade_adds_replication_and_audit_secrets_without_rotating_old_values(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / ".state").mkdir()
            credentials = self.runtime.new_credentials()
            credentials.pop("POSTGRES_REPLICATION_PASSWORD")
            credentials.pop("AUDIT_TOKEN")
            self.runtime.write_private(root / ".env.runtime", "".join(f"{k}={v}\n" for k, v in credentials.items()))
            with patch.object(self.runtime, "ROOT", root), patch.object(self.runtime, "apply"):
                self.runtime.ensure_aat_credentials()
                upgraded = self.runtime.read_env(root / ".env.runtime")
                self.assertEqual({k: upgraded[k] for k in credentials}, credentials)
                self.assertNotEqual(upgraded["AUDIT_TOKEN"], upgraded["AGGREGATOR_TOKEN"])
                self.assertNotEqual(upgraded["POSTGRES_REPLICATION_PASSWORD"], upgraded["POSTGRES_PASSWORD"])
                self.runtime.ensure_aat_credentials()
                self.assertEqual(self.runtime.read_env(root / ".env.runtime"), upgraded)


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
