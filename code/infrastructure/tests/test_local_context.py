"""Check kubeconfig import using temporary files; never contacts a cluster."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


def config(name, server):
    return {
        "apiVersion": "v1", "kind": "Config", "current-context": name,
        "clusters": [{"name": name, "cluster": {"server": server}}],
        "users": [{"name": name, "user": {"token": "test-only-token"}}],
        "contexts": [{"name": name, "context": {"cluster": name, "user": name}}],
    }


class ContextTests(unittest.TestCase):
    def run_import(self, source, target, check=True):
        return subprocess.run(
            [sys.executable, str(ROOT / "scripts/context/register.py"),
             "--source", str(source), "--target", str(target)],
            check=check, capture_output=True, text=True,
        )

    def test_import_preserves_other_context_and_is_repeatable(self):
        with tempfile.TemporaryDirectory() as folder:
            source, target = Path(folder) / "lab", Path(folder) / "config"
            source.write_text(json.dumps(config("default", "https://192.168.56.10:6443")))
            original = config("work", "https://work.example.test:6443")
            target.write_text(json.dumps(original))
            previous = target.read_bytes()
            self.run_import(source, target)
            merged = json.loads(target.read_text())
            self.assertEqual(merged["current-context"], "work")
            self.assertEqual({c["name"] for c in merged["contexts"]}, {"work", "aat-local"})
            self.assertIn(original["clusters"][0], merged["clusters"])
            self.assertIn(original["users"][0], merged["users"])
            self.assertIn(original["contexts"][0], merged["contexts"])
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)
            backup = target.with_name("config.before-aat-local")
            self.assertEqual(backup.read_bytes(), previous)
            self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
            first = target.read_bytes()
            self.run_import(source, target)
            self.assertEqual(target.read_bytes(), first)
            self.assertEqual(backup.read_bytes(), previous)

    def test_missing_destination_selects_lab_context(self):
        with tempfile.TemporaryDirectory() as folder:
            source = Path(folder) / "lab"
            target = Path(folder) / ".kube/config"
            source.write_text(json.dumps(config("default", "https://192.168.56.10:6443")))
            self.run_import(source, target)
            merged = json.loads(target.read_text())
            self.assertEqual(merged["current-context"], "aat-local")
            self.assertEqual(merged["contexts"][0]["context"]["cluster"], "aat-local")
            self.assertEqual(merged["contexts"][0]["context"]["user"], "aat-local")

    def test_source_cannot_be_overwritten(self):
        with tempfile.TemporaryDirectory() as folder:
            source = Path(folder) / "lab"
            source.write_text(json.dumps(config("default", "https://192.168.56.10:6443")))
            previous = source.read_bytes()
            self.assertNotEqual(self.run_import(source, source, check=False).returncode, 0)
            self.assertEqual(source.read_bytes(), previous)


if __name__ == "__main__":
    unittest.main()
