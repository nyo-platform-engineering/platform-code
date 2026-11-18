"""Check that the summary distinguishes synchronization from UTC uncertainty."""
import importlib.util
import os
from pathlib import Path
import pty
import shutil
import subprocess
import sys
import tempfile
import termios
import unittest

PATH = Path(__file__).resolve().parents[1] / "scripts/vm/clock_status.py"
spec = importlib.util.spec_from_file_location("clock_status", PATH)
clock = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = clock
spec.loader.exec_module(clock)


def report(source="192.168.56.10", correction="0.000000103", leap="Normal", age="1", poll="0", selected=True):
    tracking = f"C0A8380A,{source},4,1795014663,{correction},0.026716849,0.026716849,-109,0,0.399,0.259,0.303,1,{leap}\n"
    sources = f"^,*,{source},3,{poll},377,{age},-0.000046,-0.000046,0.540\n" if selected else ""
    return tracking + sources


class ClockSummaryTests(unittest.TestCase):
    def parse(self, **kwargs):
        return clock.parse("aat-worker-1", report(**kwargs), "192.168.56.10")

    def test_historical_offset_and_large_utc_uncertainty_are_not_pending_correction(self):
        result = self.parse()
        self.assertEqual(result.warnings, [])
        self.assertAlmostEqual(result.correction, 0.000000103)
        self.assertAlmostEqual(result.uncertainty, 0.4325)
        self.assertEqual(clock.duration(result.correction), "0.1 us")

    def test_fallback_is_explicit_even_when_ntp_is_synchronized(self):
        result = self.parse(source="185.125.190.58")
        self.assertTrue(any("fallback" in warning for warning in result.warnings))

    def test_no_selected_source_or_unsynchronized_leap_is_not_healthy(self):
        for values in ({"selected": False}, {"leap": "Not synchronised"}):
            with self.subTest(values=values):
                self.assertTrue(any("not synchronized" in warning for warning in self.parse(**values).warnings))

    def test_stale_sample_and_pending_correction_need_attention(self):
        result = self.parse(age="40", correction="0.004")
        self.assertEqual(len(result.warnings), 2)
        self.assertEqual(clock.duration(result.correction), "4.0 ms")

    def test_scheduled_leap_second_still_counts_as_synchronized(self):
        self.assertEqual(self.parse(leap="Insert second").warnings, [])
        self.assertEqual(self.parse(leap="Delete second").warnings, [])

    def test_public_source_age_uses_its_poll_interval(self):
        result = clock.parse("aat-control", report(source="185.125.190.58", age="217", poll="6"), "192.168.56.10")
        self.assertEqual(result.warnings, [])
        result = clock.parse("aat-control", report(age="300", poll="6"), "192.168.56.10")
        self.assertTrue(any("old" in warning for warning in result.warnings))

    def test_invalid_report_cannot_appear_healthy(self):
        for output in ("", "unexpected output", report(correction="nan")):
            with self.subTest(output=output):
                with self.assertRaises(ValueError):
                    clock.parse("aat-worker-1", output, "192.168.56.10")

    def test_parallel_ssh_cannot_change_the_callers_terminal(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / "scripts/vm").mkdir(parents=True)
            (root / "local").mkdir()
            (root / "bin").mkdir()
            script = root / "scripts/vm/clock_status.py"
            shutil.copy(PATH, script)
            # Model an SSH child entering raw output mode if it inherits a TTY.
            fake = root / "bin/vagrant"
            fake.write_text(f'''#!{sys.executable}
import os
import termios
if os.isatty(0):
    state = termios.tcgetattr(0)
    state[1] &= ~termios.OPOST
    termios.tcsetattr(0, termios.TCSANOW, state)
print({report()!r}, end="")
''')
            fake.chmod(0o755)
            master, slave = pty.openpty()
            try:
                before = termios.tcgetattr(slave)
                result = subprocess.run(
                    [sys.executable, str(script)], stdin=slave,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                    env=dict(os.environ, PATH=str(root / "bin") + os.pathsep + os.environ["PATH"]),
                    text=True, timeout=15, check=True,
                )
                self.assertEqual(before, termios.tcgetattr(slave))
                self.assertIn("OK: both workers use control", result.stdout)
            finally:
                os.close(master)
                os.close(slave)


if __name__ == "__main__":
    unittest.main()
