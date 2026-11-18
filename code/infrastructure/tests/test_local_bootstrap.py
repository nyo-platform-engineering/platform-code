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
        shutil.copytree(ROOT / "scripts/up", root / "scripts/up")
        shutil.copytree(ROOT / "scripts/doctor", root / "scripts/doctor")
        (root / "local").mkdir()
        argo = root / "argo-apps/platform/01-cd/argocd"
        argo.mkdir(parents=True)
        shutil.copy(ROOT / "argo-apps/platform/01-cd/argocd/app.yaml", argo / "app.yaml")
        shutil.copy(ROOT / "argo-apps/root.yaml", root / "argo-apps/root.yaml")
        (root / "scripts/up/runtime.py").write_text("print('runtime test stub')\n")
        (root / "scripts/up/gateway.py").write_text('''import os
import sys
with open(os.environ["CALL_LOG"], "a") as log:
    log.write("gateway " + " ".join(sys.argv[1:]) + "\\n")
''')
        # Startup must use the numbered step, never this legacy entry point.
        (root / "scripts/vm").mkdir()
        (root / "scripts/vm/main.sh").write_text('exit 99\n')
        (root / "bin/vagrant").write_text('''#!/bin/sh
printf 'vagrant %s\\n' "$*" >> "$CALL_LOG"
if [ "${VAGRANT_FAIL:-}" = 1 ]; then exit 1; fi
if [ "$1" = plugin ]; then printf 'vagrant-libvirt (0.12.2)\\n'; fi
if [ "$1" = ssh ]; then printf 'server: https://127.0.0.1:6443\\n'; fi
''')
        (root / "bin/kubectl").write_text('''#!/bin/sh
printf 'kubectl %s\\n' "$*" >> "$CALL_LOG"
if [ "$3 $4" = 'get deployment/traefik' ]; then printf 'deployment.apps/traefik\\n'; fi
if [ "$3 $4" = 'get gateway/local' ]; then printf 'gateway.gateway.networking.k8s.io/local\\n'; fi
if [ "$3 $4" = 'get node' ] && [ "${VM_NODES_MISSING:-}" = 1 ]; then exit 1; fi
if [ "$3 $4" = 'get --raw=/readyz' ] && [ "${CLUSTER_NOT_READY:-}" = 1 ]; then exit 1; fi
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
                   KUBECONFIG=str(root / "external.yaml"), CALL_LOG=str(root / "calls"),
                   AAT_VM_DRIVER="qemu")
        env["GATEWAY_PORT"] = "8080"
        return root, env

    def deploy(self, root, env, check=False):
        # Exercise the real numbered scripts against the selected existing cluster.
        return subprocess.run(
            ["bash", "-c", "set -e; bash scripts/up/2-cluster.sh; "
             "bash scripts/up/3-argocd.sh; bash scripts/up/4-apps.sh"],
            cwd=root, env=env, check=check, capture_output=True, text=True,
        )

    def test_deploy_seeds_argocd_then_applies_only_the_root(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            self.deploy(root, env, check=True)
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
            self.deploy(root, env, check=True)
            calls = (root / "calls").read_text()
            self.assertNotIn("helm ", calls)
            self.assertIn("apply -f argo-apps/root.yaml", calls)

    def test_lookup_failure_does_not_trigger_a_new_install(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["ARGOCD_LOOKUP_FAIL"] = "1"
            result = self.deploy(root, env)
            self.assertNotEqual(result.returncode, 0)
            calls = (root / "calls").read_text()
            self.assertNotIn("helm ", calls)
            self.assertNotIn("apply -f argo-apps/root.yaml", calls)

    def test_up_targets_its_own_lab_even_with_external_kubeconfig(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            subprocess.run(["bash", str(root / "scripts/up/main.sh")], env=env, check=True, capture_output=True)
            calls = (root / "calls").read_text()
            control = calls.index("vagrant up aat-control --provider=libvirt")
            workers = calls.index("vagrant up aat-worker-1 aat-worker-2 --provider=libvirt --no-parallel")
            self.assertLess(control, workers)
            self.assertIn("node/aat-control node/aat-worker-1 node/aat-worker-2 --timeout=300s", calls)
            self.assertIn("192.168.56.10", (root / ".state/kubeconfig").read_text())
            self.assertIn("--kubeconfig " + str(root / ".state/kubeconfig"), calls)
            self.assertNotIn(str(root / "external.yaml"), calls)
            self.assertGreater(calls.index("gateway start"), calls.index("apply -f argo-apps/root.yaml"))

    def test_up_rejects_arguments_before_starting_vms(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            for option in ("--vm=false", "--vm=true", "unexpected"):
                result = subprocess.run(
                    ["bash", str(root / "scripts/up/main.sh"), option],
                    env=env, capture_output=True,
                )
                self.assertEqual(result.returncode, 2)
                self.assertFalse((root / "calls").exists())

    def test_doctor_only_checks_vm_prerequisites(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            subprocess.run(
                ["bash", str(root / "scripts/doctor/main.sh")],
                env=env, check=True, capture_output=True,
            )
            self.assertEqual((root / "calls").read_text().splitlines(), ["vagrant plugin list"])
            self.assertFalse((root / ".state/cluster-token").exists())

    def test_cluster_preflight_stops_when_vm_nodes_are_missing(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["VM_NODES_MISSING"] = "1"
            result = subprocess.run(
                ["bash", str(root / "scripts/up/2-cluster.sh")],
                env=env, capture_output=True, text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("VM nodes are missing", result.stderr)
            calls = (root / "calls").read_text()
            self.assertNotIn("create namespace", calls)
            self.assertNotIn("helm ", calls)

    def test_argocd_preflight_stops_when_cluster_api_is_not_ready(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["CLUSTER_NOT_READY"] = "1"
            result = subprocess.run(
                ["bash", str(root / "scripts/up/3-argocd.sh")],
                env=env, capture_output=True, text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Kubernetes API is not ready", result.stderr)
            calls = (root / "calls").read_text()
            self.assertNotIn("get deployment", calls)
            self.assertNotIn("helm ", calls)

    def test_container_cluster_stops_before_creating_namespaces_or_releases(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["CONTAINER_CLUSTER"] = "1"
            result = self.deploy(root, env)
            self.assertNotEqual(result.returncode, 0)
            calls = (root / "calls").read_text()
            self.assertNotIn("helm ", calls)
            self.assertNotIn("create namespace", calls)

    def test_missing_kubeconfig_stops_without_any_cluster_command(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            (root / "external.yaml").unlink()
            result = self.deploy(root, env)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((root / "calls").exists())

    def test_gateway_uses_lab_kubeconfig_and_only_loopback(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            result = subprocess.run(
                ["bash", str(root / "scripts/up/4-apps.sh")],
                env=env, check=True, capture_output=True, text=True,
            )
            calls = (root / "calls").read_text().splitlines()
            self.assertTrue(any("rollout status deployment/traefik" in call for call in calls))
            self.assertTrue(any("--for=condition=Programmed gateway/local" in call for call in calls))
            self.assertTrue(all("--kubeconfig " + str(root / "external.yaml") in call
                                for call in calls if call.startswith("kubectl ")))
            self.assertEqual(calls[-1], "gateway start --port 8080 --host 192.168.56.10")
            self.assertIn("http://radar.localhost:8080", result.stdout)

    def test_gateway_rejects_invalid_ports_before_cluster_access(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            for port in ("0", "65536", "-1", "080", "8080:80", "abc"):
                env["GATEWAY_PORT"] = port
                with self.subTest(port=port):
                    result = subprocess.run(
                        ["bash", str(root / "scripts/up/4-apps.sh")],
                        env=env, capture_output=True,
                    )
                    self.assertEqual(result.returncode, 2)
                    self.assertFalse((root / "calls").exists())

    @unittest.skipIf(os.geteuid() == 0, "sudo is only used by non-root users")
    def test_gateway_default_port_uses_sudo_to_bind_listener(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            env["GATEWAY_PORT"] = "80"
            sudo = root / "bin/sudo"
            sudo.write_text('#!/bin/sh\nprintf "sudo invoked\\n" >> "$CALL_LOG"\nexec "$@"\n')
            sudo.chmod(0o755)
            subprocess.run(
                ["bash", str(root / "scripts/up/4-apps.sh")],
                env=env, check=True, capture_output=True,
            )
            calls = (root / "calls").read_text().splitlines()
            self.assertEqual(calls[-2], "sudo invoked")
            self.assertEqual(calls[-1], "gateway start --port 80 --host 192.168.56.10")

    def test_gateway_stop_and_status_do_not_need_cluster_access(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            (root / "external.yaml").unlink()
            for action in ("stop", "status"):
                subprocess.run(
                    ["/usr/bin/python3", str(root / "scripts/up/gateway.py"), action],
                    env=env, check=True, capture_output=True,
                )
            self.assertEqual((root / "calls").read_text().splitlines(), ["gateway stop", "gateway status"])

    def test_vm_shutdown_stops_the_gateway(self):
        with tempfile.TemporaryDirectory() as folder:
            root, env = self.make_lab(folder)
            shutil.copy(ROOT / "scripts/vm/main.sh", root / "scripts/vm/main.sh")
            subprocess.run(["bash", str(root / "scripts/vm/main.sh"), "halt"], env=env, check=True, capture_output=True)
            self.assertEqual((root / "calls").read_text().splitlines(), ["gateway stop", "vagrant halt"])
            (root / "calls").unlink()
            subprocess.run(["bash", str(root / "scripts/vm/main.sh"), "destroy"], env=env, check=True, capture_output=True)
            self.assertEqual((root / "calls").read_text().splitlines(), ["vagrant destroy", "gateway stop"])


if __name__ == "__main__":
    unittest.main()
