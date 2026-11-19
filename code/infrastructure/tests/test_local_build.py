import importlib.util
import json
import re
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("local_build", ROOT / "scripts/up/local-build.py")
local_build = importlib.util.module_from_spec(spec)
spec.loader.exec_module(local_build)


class LocalBuildTest(unittest.TestCase):
    def setUp(self):
        self.plan = local_build.image_plan("aat", "arm64", "local-012345abcdef")
        self.root = {"spec": {"syncPolicy": {"automated": {"selfHeal": True}, "syncOptions": ["ServerSideApply=true"]}}}
        self.dev = {"spec": {"source": {"path": "code/infrastructure/argo-apps/dev"}}}

    def test_parent_preserves_only_local_dev_overrides(self):
        root, dev = local_build.applications(self.root, self.dev, self.plan)
        rule = root["spec"]["ignoreDifferences"][0]
        self.assertEqual(rule["name"], "dev-apps")
        self.assertEqual(rule["jsonPointers"], ["/spec/source/kustomize"])
        self.assertIn("RespectIgnoreDifferences=true", root["spec"]["syncPolicy"]["syncOptions"])
        self.assertTrue(root["spec"]["syncPolicy"]["automated"]["selfHeal"])
        self.assertNotIn("kustomize", self.dev["spec"]["source"])
        patches = dev["spec"]["source"]["kustomize"]["patches"]
        self.assertEqual(len(patches), 8)
        self.assertNotIn("aat-sensor-pantai-dev", {p["target"]["name"] for p in patches})

    def test_kustomize_applies_overrides_to_real_applications(self):
        _, dev = local_build.applications(self.root, self.dev, self.plan)
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            resources = []
            for i, patch_spec in enumerate(dev["spec"]["source"]["kustomize"]["patches"]):
                name = patch_spec["target"]["name"]
                service = name.removeprefix("aat-").removesuffix("-dev")
                source = ROOT / "argo-apps/dev/aat" / service / "app.yaml"
                filename = f"app-{i}.yaml"
                (folder / filename).write_bytes(source.read_bytes())
                resources.append(filename)
            (folder / "kustomization.yaml").write_text(json.dumps({
                "apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization",
                "resources": resources, "patches": dev["spec"]["source"]["kustomize"]["patches"],
            }))
            rendered = subprocess.check_output(["kubectl", "kustomize", str(folder)], text=True)
            self.assertEqual(len(re.findall(r'name: image\.digest\s+value: ""', rendered)), 8)
            self.assertEqual(rendered.count("value: Never"), 8)
            for service in local_build.SERVICES:
                self.assertIn(f"value: docker.io/aat-local/{service}", rendered)

    def test_digest_override_wins_over_remote_pin_in_helm(self):
        output = subprocess.check_output([
            "helm", "template", "aggregator", str(ROOT / "charts/deployment"),
            "-f", str(ROOT / "argo-apps/dev/aat/aggregator/values.yaml"),
            "-f", str(ROOT / "argo-apps/dev/aat/aggregator/image.yaml"),
            "--set-string", "image.repository=docker.io/aat-local/aggregator",
            "--set-string", "image.tag=" + self.plan["tag"],
            "--set-string", "image.digest=", "--set-string", "image.pullPolicy=Never",
        ], text=True)
        self.assertIn('image: "docker.io/aat-local/aggregator:local-012345abcdef"', output)
        self.assertIn("imagePullPolicy: Never", output)
        self.assertNotIn("@sha256:", output)

    def test_non_k3d_context_is_rejected_before_build(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / ".state").mkdir()
            (root / ".state/mode").write_text("k3d\n")
            with patch.object(local_build, "ROOT", root):
                builder = local_build.LocalBuild()
                with patch.object(builder, "kube", return_value=subprocess.CompletedProcess([], 0, stdout="production\n")):
                    with self.assertRaisesRegex(ValueError, "private k3d"):
                        builder.validate_cluster()

    def test_citus_local_image_overrides_remote_digest(self):
        output = subprocess.check_output([
            "helm", "template", "aat-dependencies", str(ROOT / "charts/aat-dependencies"),
            "-f", str(ROOT / "argo-apps/dev/aat/citus/image.yaml"),
            "--set-string", "image.repository=docker.io/aat-local/citus",
            "--set-string", "image.tag=" + self.plan["tag"],
            "--set-string", "image.digest=", "--set-string", "image.pullPolicy=Never",
        ], text=True)
        self.assertEqual(output.count('image: "docker.io/aat-local/citus:local-012345abcdef"'), 7)
        self.assertEqual(output.count("imagePullPolicy: Never"), 7)
        self.assertNotIn("@sha256:", output)

    def test_invalid_architecture_and_tags_are_rejected(self):
        with self.assertRaises(ValueError):
            local_build.image_plan("aat", "unknown", "local-012345abcdef")
        with self.assertRaises(ValueError):
            local_build.image_plan("aat", "arm64", "latest")

    def test_failed_import_preserves_previous_successful_images(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "infrastructure"
            source = root.parent / "apps/aat/storage/postgres"
            source.mkdir(parents=True)
            (source / "smoke-test.sh").write_text("exit 0\n")
            with patch.object(local_build, "ROOT", root):
                builder = local_build.LocalBuild()
                builder.state.mkdir(parents=True)
                state_file = builder.state / "images.json"
                state_file.write_text(json.dumps(self.plan))

                def run(command, **kwargs):
                    if command[:3] == ["k3d", "image", "import"]:
                        raise subprocess.CalledProcessError(1, command)
                    return subprocess.CompletedProcess(command, 0)

                with patch.object(builder, "validate_cluster", return_value="arm64"), \
                        patch.object(local_build.subprocess, "run", side_effect=run):
                    with self.assertRaises(subprocess.CalledProcessError):
                        builder.build()
                self.assertEqual(json.loads(state_file.read_text()), self.plan)


if __name__ == "__main__":
    unittest.main()
