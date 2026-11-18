"""Summarize chrony reports without treating local correction as UTC accuracy."""
from concurrent.futures import ThreadPoolExecutor
import csv
from dataclasses import dataclass
import io
import math
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
NODES = ("aat-control", "aat-worker-1", "aat-worker-2")


@dataclass
class Clock:
    node: str
    source: str
    correction: float
    age: int
    uncertainty: float
    warnings: list[str]


def duration(seconds):
    value = abs(seconds)
    if value < 0.001:
        return f"{value * 1e6:.1f} us"
    if value < 1:
        return f"{value * 1000:.1f} ms"
    return f"{value:.2f} s"


def parse(node, output, control_ip):
    rows = list(csv.reader(io.StringIO(output.replace("\r", ""))))
    rows = [row for row in rows if row]
    if not rows or len(rows[0]) != 14:
        raise ValueError("unrecognized chrony tracking response")
    tracking = rows[0]
    selected = next((row for row in rows[1:] if len(row) == 10 and row[1] == "*"), None)
    correction = abs(float(tracking[4]))
    uncertainty = float(tracking[10]) / 2 + float(tracking[11])
    if not math.isfinite(correction) or not math.isfinite(uncertainty) or uncertainty < 0:
        raise ValueError("invalid clock correction or uncertainty")
    warnings = []
    if tracking[13] not in {"Normal", "Insert second", "Delete second"} or selected is None:
        warnings.append("not synchronized to a usable NTP source")
    source = selected[2] if selected else "none"
    age = int(selected[6]) if selected else 0
    if selected:
        poll = int(selected[4])
        if age > max(30, 4 * 2 ** poll):
            warnings.append(f"last good NTP sample is {age}s old")
        if node != "aat-control" and source != control_ip:
            warnings.append("using fallback; shared control reference not selected")
    if correction > 0.001:
        warnings.append("clock is still correcting by more than 1 ms")
    return Clock(node, source, correction, age, uncertainty, warnings)


def inspect(node, control_ip):
    try:
        result = subprocess.run(
            ["vagrant", "ssh", node, "-c", "chronyc -c -n tracking && chronyc -c -n sources"],
            cwd=ROOT / "local", capture_output=True, text=True, timeout=30,
        )
        # Hide only the known provider warning; preserve other diagnostics.
        diagnostics = "\n".join(line for line in result.stderr.splitlines()
                                if line != "[fog][WARNING] Unrecognized arguments: libvirt_ip_command")
        if result.returncode:
            return node, None, diagnostics or "SSH or chrony command failed"
        clock = parse(node, result.stdout, control_ip)
        return node, clock, diagnostics
    except (OSError, subprocess.TimeoutExpired, ValueError, OverflowError) as error:
        return node, None, str(error)


def main():
    control_ip = os.environ.get("AAT_NETWORK_PREFIX", "192.168.56") + ".10"
    with ThreadPoolExecutor(max_workers=3) as pool:
        reports = list(pool.map(lambda node: inspect(node, control_ip), NODES))
    print(f"{'VM':<15} {'STATUS':<8} {'TIME SOURCE':<24} {'CORRECTION LEFT':>16} {'LAST SAMPLE':>13}")
    issues = []
    clocks = []
    failed = False
    for node, clock, diagnostic in reports:
        if clock is None:
            print(f"{node:<15} {'ERROR':<8} {'unavailable':<24} {'-':>16} {'-':>13}")
            issues.append(f"{node}: {diagnostic}")
            failed = True
            continue
        clocks.append(clock)
        source = "control (" + clock.source + ")" if clock.source == control_ip else clock.source
        status = "CHECK" if clock.warnings else "SYNCED"
        age = f"{clock.age}s ago" if clock.source != "none" else "-"
        print(f"{node:<15} {status:<8} {source:<24} {duration(clock.correction):>16} {age:>13}")
        issues.extend(f"{node}: {warning}" for warning in clock.warnings)
        if diagnostic:
            issues.append(f"{node}: {diagnostic}")
    if issues:
        print("\nNeeds attention:")
        for issue in issues:
            print("  - " + issue)
    else:
        print("\nOK: both workers use control; all pending clock corrections are below 1 ms.")
    control = next((clock for clock in clocks if clock.node == "aat-control"), None)
    if control and control.source != "none":
        print(f"UTC uncertainty: +/- {duration(control.uncertainty)} (internet reference, not worker-to-control drift).")
    print("Correction left = adjustment still pending on each VM; closer to zero is better.")
    print("Full details: task clock:status -- --verbose")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
