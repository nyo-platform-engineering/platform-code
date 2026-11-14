"""Exercise deployment orchestration with command fakes, never the host cluster."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class BootstrapTests(unittest.TestCase):
    def make_lab(self, folder):
        root = Path(folder)
        (root / "scripts").mkdir()
        (root / ".state").mkdir()
        (root / "bin").mkdir()
        shutil.copy(ROOT / "scripts/bootstrap.sh", root / "scripts/bootstrap.sh")
        shutil.copy(ROOT / "scripts/topology.py", root / "scripts/topology.py")
        argo = root / "argo-apps/platform/01-cd/argocd"
        argo.mkdir(parents=True)
        shutil.copy(ROOT / "argo-apps/platform/01-cd/argocd/app.yaml", argo / "app.yaml")
        shutil.copy(ROOT / "argo-apps/root.yaml", root / "argo-apps/root.yaml")
        (root / "scripts/runtime.py").write_text("print('runtime test stub')\n")
        (root / "scripts/vm.sh").write_text('touch .state/kubeconfig\nprintf "vm %s\\n" "$*" >> "$CALL_LOG"\n')
        (root / "bin/kubectl").write_text('''#!/bin/sh
printf 'kubectl %s\\n' "$*" >> "$CALL_LOG"
if [ "$3 $4" = 'get deployment' ] && [ "$5" = argocd-server ]; then
  [ "${ARGOCD_LOOKUP_FAIL:-}" != 1 ] || exit 1
  if [ "${ARGOCD_EXISTS:-}" = 1 ]; then printf 'deployment.apps/argocd-server\\n'; fi
fi
if [ "$3 $4" = 'get nodes' ]; then
  if [ "${CONTAINER_CLUSTER:-}" = 1 ]; then prefix=k3d-dev-; else prefix=aat-; fi
  printf '{"items":['
  for i in 0 1 2; do
    [ "$i" = 0 ] || printf ','
    printf '{"metadata":{"name":"%s%s"},"status":{"nodeInfo":{"osImage":"Ubuntu 24.04"}}}' "$prefix" "$i"
  done
  printf ']}\\n'
fi
''')
        (root / "bin/helm").write_text('''#!/bin/sh
printf 'helm %s\\n' "$*" >> "$CALL_LOG"
''')
        for path in (root / "bin").iterdir():
            path.chmod(0o755)
        (root / "external.yaml").touch()
        env = dict(os.environ, PATH=str(root / "bin") + os.pathsep + os.environ["PATH"],
                   KUBECONFIG=str(root / "external.yaml"), CALL_LOG=str(root / "calls"))
        return root, env

    def test_deploy_seeds_argocd_then_applies_only_the_root(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            subprocess.run(["bash", str(root / "scripts/bootstrap.sh"), "deploy"], env=env, check=True, capture_output=True)
            calls = (root / "calls").read_text().splitlines()
            helm = [line for line in calls if line.startswith("helm ")]
            self.assertEqual(len(helm), 1)
            self.assertIn("upgrade --install argocd", helm[0])
            self.assertIn("--version 10.9.1", helm[0])
            self.assertTrue(all("--kubeconfig " + str(root / "external.yaml") in line for line in calls if line.startswith(("kubectl ", "helm "))))
            self.assertFalse(any("gitlab" in line or "upgrade --install aggregator" in line or "upgrade --install lgtm" in line for line in calls))
            root_apply = next(i for i, line in enumerate(calls) if "apply -f argo-apps/root.yaml" in line)
            self.assertGreater(root_apply, calls.index(helm[0]))

    def test_existing_argocd_is_not_upgraded_by_bootstrap(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["ARGOCD_EXISTS"] = "1"
            subprocess.run(["bash", str(root / "scripts/bootstrap.sh"), "deploy"], env=env, check=True, capture_output=True)
            calls = (root / "calls").read_text()
            self.assertNotIn("helm ", calls)
            self.assertIn("apply -f argo-apps/root.yaml", calls)

    def test_lookup_failure_does_not_trigger_a_new_install(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["ARGOCD_LOOKUP_FAIL"] = "1"
            result = subprocess.run(["bash", str(root / "scripts/bootstrap.sh"), "deploy"], env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            calls = (root / "calls").read_text()
            self.assertNotIn("helm ", calls)
            self.assertNotIn("apply -f argo-apps/root.yaml", calls)

    def test_up_targets_its_own_lab_even_with_external_kubeconfig(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            subprocess.run(["bash", str(root / "scripts/bootstrap.sh"), "up"], env=env, check=True, capture_output=True)
            calls = (root / "calls").read_text()
            self.assertIn("vm up", calls)
            self.assertIn("--kubeconfig " + str(root / ".state/kubeconfig"), calls)
            self.assertNotIn(str(root / "external.yaml"), calls)

    def test_container_cluster_stops_before_creating_namespaces_or_releases(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["CONTAINER_CLUSTER"] = "1"
            result = subprocess.run(["bash", str(root / "scripts/bootstrap.sh"), "deploy"], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            calls = (root / "calls").read_text()
            self.assertNotIn("helm ", calls)
            self.assertNotIn("create namespace", calls)

    def test_missing_kubeconfig_stops_without_any_cluster_command(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            (root / "external.yaml").unlink()
            result = subprocess.run(["bash", str(root / "scripts/bootstrap.sh"), "deploy"], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((root / "calls").exists())


if __name__ == "__main__":
    unittest.main()
