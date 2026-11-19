#!/usr/bin/env python3
"""Guided M2 P2 checks; edit CONFIG and press Enter between steps."""
from __future__ import annotations
from dataclasses import dataclass, field, replace
import datetime as dt
import math
from pathlib import Path
import sys
import uuid
import urllib.parse

import group1_coordination
import group2_clock_freeze
import group3_load
from helper import INFRA, ROOT, Run, output, require, utc


@dataclass
class TestConfig:
    kubeconfig: Path = INFRA / ".state/kubeconfig"
    env_file: Path = INFRA / ".env.runtime"
    # Auto: VM control-node IP or k3d's published loopback NodePort.
    # Set an origin explicitly when using another Traefik gateway address.
    url: str | None = None
    worker: str = "aat-worker-1"
    source: str = "BMKG"
    groups: tuple = (1, 2, 3)
    observation_seconds: float = 30
    takeover_seconds: float = 60
    recovery_seconds: float = 180
    freeze_seconds: float = 30
    request_timeout: float = 8
    poll_interval: float = 3
    # Our deterministic development mocks, not an external feed contract.
    event_intervals: dict = field(default_factory=lambda: {"BMKG": 10, "PVMBG": 10})
    load_vus: int = 10_000
    load_seconds: int = 120
    warmup_seconds: int = 10
    load_limit: int = 50
    k6: str = "k6"
    # Use a different immutable image digest, available to the cluster.
    rolling_image: str | None = None
    output_dir: Path | None = None


# CONFIG = TestConfig()
CONFIG = TestConfig(
    groups=(3,),
    load_vus=100,
    load_seconds=120,
)

def validate_config(config):
    require(sys.stdin.isatty(), "run task m2:p2 in an interactive terminal")
    require(config.groups and set(config.groups) <= {1, 2, 3}, "groups must contain 1, 2, and/or 3")
    require(config.source in ("BMKG", "PVMBG"), "source must be BMKG or PVMBG")
    if config.url is not None:
        url = urllib.parse.urlsplit(config.url)
        require(url.scheme in ("http", "https") and url.hostname and not url.username and not url.password
                and not url.query and not url.fragment and url.path in ("", "/"), "url must be an HTTP(S) gateway origin without credentials")
    require(config.worker in ("aat-worker-1", "aat-worker-2"), "choose a non-control-plane lab worker")
    for name in ("observation_seconds", "takeover_seconds", "recovery_seconds", "freeze_seconds",
                 "request_timeout", "poll_interval"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"{name} must be finite and positive")
    require(config.observation_seconds >= 15, "observe for at least 15 seconds")
    require(config.takeover_seconds <= 60, "takeover bound cannot exceed 60 seconds")
    require(config.freeze_seconds > 20, "freeze must exceed the 20-second owner session idle timeout")
    require(type(config.load_vus) is int and config.load_vus > 0, "load_vus must be positive")
    require(type(config.load_seconds) is int and config.load_seconds >= 15, "load_seconds must be >=15")
    if 3 in config.groups:
        require(config.load_seconds >= 120,
                "Group 3 requires >=120 seconds for real replica transitions; reduce load_vus for local debugging")
    require(type(config.warmup_seconds) is int and config.warmup_seconds >= 1, "warmup_seconds must be positive")
    require(1 <= config.load_limit <= 1000, "load_limit must be 1..1000")
    require(all(type(v) is int and v > 0 for v in config.event_intervals.values()), "invalid event intervals")
    require(set(config.event_intervals) == {"BMKG", "PVMBG"}, "configure both mock event intervals")
    require(config.observation_seconds >= 3 * max(config.event_intervals.values()),
            "observation_seconds must cover at least three mock event intervals")


def main():
    if len(sys.argv) > 1:
        print("No command-line options. Edit CONFIG in test-problem-2.py.", file=sys.stderr)
        return 2
    config = replace(CONFIG)
    name = "m2-p2-" + dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S").lower() + "-" + uuid.uuid4().hex[:8]
    folder = Path(config.output_dir or Path(__file__).resolve().parent / "evidence" / name).resolve()
    folder.mkdir(parents=True, exist_ok=False)
    run = Run(config, folder, name)
    code = 1
    try:
        validate_config(config)
        with run:
            run.stage("preflight", run.preflight)
            for number, group in ((1, group1_coordination), (2, group2_clock_freeze), (3, group3_load)):
                if number in config.groups:
                    group.run_group(run)
            run.report["status"] = "automated_checks_passed_manual_evidence_pending"
            code = 0
    except (Exception, KeyboardInterrupt) as error:
        run.report["status"] = "interrupted" if isinstance(error, KeyboardInterrupt) else "failed"
        # Do not persist subprocess output, URLs containing credentials, or tokens.
        run.report["error"] = str(error) if isinstance(error, ValueError) else type(error).__name__
        output("FAIL", run.report["error"])
    finally:
        run.report["finished_at"] = utc()
        run.save("result.json", run.report)
        for name, result in run.report["checks"].items():
            output(result["status"].upper(), name)
        output("INFO", f"Evidence: {folder}")
        output("MANUAL", "Official Tester Agent, clock/fencing proof, generator capacity, and report remain separate evidence.")
    return code


if __name__ == "__main__":
    sys.exit(main())
