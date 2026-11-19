"""Progress must stay bounded, redact output, and stop after each stage."""
import json
from pathlib import Path
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent / "m2"))
from run_progress import Progress
from common import Run as CommonRun


class ProgressTests(unittest.TestCase):
    def test_wait_detail_budget_and_redaction_are_saved(self):
        with tempfile.TemporaryDirectory() as folder:
            clock, lines = [0.0], []
            with patch("run_progress.time.monotonic", side_effect=lambda: clock[0]):
                progress = Progress(folder, "takeover", writer=lines.append,
                                    clean=lambda value: value.replace("secret", "[REDACTED]"))
                with progress.phase("waiting for new owner", 60):
                    progress.detail("secret; Ready=2/3")
                    clock[0] = 7
                    progress.pulse()
            self.assertIn("7s / 60s", lines[-1])
            self.assertIn("Ready=2/3", lines[-1])
            text = (Path(folder) / "progress.jsonl").read_text()
            self.assertNotIn("secret", text)
            row = json.loads(text.splitlines()[-1])
            self.assertEqual(row["elapsed_seconds"], 7)
            self.assertEqual(row["timeout_seconds"], 60)

    def test_prompt_is_quiet_and_nested_phase_restores_parent(self):
        with tempfile.TemporaryDirectory() as folder:
            lines = []
            progress = Progress(folder, "stage", writer=lines.append)
            with progress.phase("replica recovery", 180):
                progress.detail("Ready=2/3")
                count = len(lines)
                with progress.phase("waiting for Enter", quiet=True):
                    progress.pulse()
                    self.assertEqual(len(lines), count)
                progress.pulse()
                self.assertIn("replica recovery", lines[-1])
                self.assertIn("Ready=2/3", lines[-1])

    def test_periodic_reporter_stops_when_stage_exits(self):
        with tempfile.TemporaryDirectory() as folder:
            emitted = threading.Event()
            with Progress(folder, "stage", interval=0.01, writer=lambda _: emitted.set()) as progress:
                self.assertTrue(emitted.wait(1))
            self.assertFalse(progress.thread.is_alive())

    def test_phase_exit_after_failure_restores_outer_context(self):
        with tempfile.TemporaryDirectory() as folder:
            progress = Progress(folder, "stage", writer=lambda _: None)
            before = progress.state
            with self.assertRaises(ValueError):
                with progress.phase("failing operation", 10):
                    raise ValueError("failure")
            self.assertIs(progress.state, before)


class EvidenceRootTests(unittest.TestCase):
    def test_prepared_folder_from_shared_runner_is_reused(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            env = root / "runtime.env"
            env.write_text("")
            prepared = root / "prepared"
            prepared.mkdir()
            (prepared / "console.log").write_text("existing terminal log")
            config = SimpleNamespace(output_dir=None, env_file=env)
            run = CommonRun(4, config, [], prepared)
            self.assertEqual(run.folder, prepared)
            self.assertEqual((prepared / "console.log").read_text(), "existing terminal log")

    def test_custom_root_and_explicit_output_preserve_other_suites(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder).resolve()
            env = root / "runtime.env"
            env.write_text("")
            config = SimpleNamespace(output_dir=None, env_file=env)
            run = CommonRun(3, config, [], evidence_root=root / "p3/evidence")
            self.assertEqual(run.folder.parent, root / "p3/evidence")
            with patch("common.ROOT", root):
                other = CommonRun(4, config, [])
                self.assertEqual(other.folder.parent, root / "docs/evidence")
            config.output_dir = root / "explicit"
            explicit = CommonRun(3, config, [], evidence_root=root / "p3/evidence")
            self.assertEqual(explicit.folder, root / "explicit")


if __name__ == "__main__":
    unittest.main()
