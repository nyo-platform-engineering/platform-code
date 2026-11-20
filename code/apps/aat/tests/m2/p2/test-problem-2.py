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
    worker: str | None = None  # Auto-select a worker without mocks/coordinator.
    source: str = "BMKG"
    groups: tuple = (1, 2, 3)
    observation_seconds: float = 30
    takeover_seconds: float = 60
    recovery_seconds: float = 180
    freeze_seconds: float = 30
    # Examples from the specification; configure other nonzero skew cases here.
    clock_offsets: tuple = (45, -30)
    request_timeout: float = 8
    poll_interval: float = 3
    # Our deterministic development mocks, not an external feed contract.
    event_intervals: dict = field(default_factory=lambda: {"BMKG": 10, "PVMBG": 10})
    load_vus: int = 10_000
    load_seconds: int = 120
    # Scaling may need a longer window than the two throughput samples.
    # None uses load_seconds for scaling.
    scaling_seconds: int | None = None
    warmup_seconds: int = 10
    load_limit: int = 50
    k6: str = "k6"
    # Use a different immutable image digest, available to the cluster.
    rolling_image: str | None = None  # None selects a previous published GitOps pin.
    rolling_seconds: int = 360
    rolling_rps: int = 30
    output_dir: Path | None = None


# Full criteria run; smaller configurations are development evidence only.
CONFIG = TestConfig()

def validate_config(config):
    require(sys.stdin.isatty(), "run task m2:p2 in an interactive terminal")
    require(config.groups and set(config.groups) <= {1, 2, 3}, "groups must contain 1, 2, and/or 3")
    require(config.source in ("BMKG", "PVMBG"), "source must be BMKG or PVMBG")
    if config.url is not None:
        url = urllib.parse.urlsplit(config.url)
        require(url.scheme in ("http", "https") and url.hostname and not url.username and not url.password
                and not url.query and not url.fragment and url.path in ("", "/"), "url must be an HTTP(S) gateway origin without credentials")
    require(config.worker in (None, "aat-worker-1", "aat-worker-2"), "choose a non-control-plane lab worker")
    for name in ("observation_seconds", "takeover_seconds", "recovery_seconds", "freeze_seconds",
                 "request_timeout", "poll_interval"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"{name} must be finite and positive")
    require(config.observation_seconds >= 15, "observe for at least 15 seconds")
    require(config.takeover_seconds <= 60, "takeover bound cannot exceed 60 seconds")
    require(config.freeze_seconds > 20, "freeze must exceed the 20-second owner session idle timeout")
    require(config.clock_offsets and all(type(v) in (int, float) and math.isfinite(v)
            and 5 < abs(v) <= 120 for v in config.clock_offsets),
            "clock_offsets must contain finite skew cases greater than 5s and at most 120s")
    require(type(config.load_vus) is int and config.load_vus > 0, "load_vus must be positive")
    require(type(config.load_seconds) is int and config.load_seconds >= 15, "load_seconds must be >=15")
    if config.scaling_seconds is not None:
        require(type(config.scaling_seconds) is int and config.scaling_seconds >= 30,
                "scaling_seconds must be >=30 seconds for replica transitions")
    if 3 in config.groups:
        require((config.scaling_seconds or config.load_seconds) >= 30,
                "set scaling_seconds >=30 for replica transitions when load_seconds is shorter")
    require(type(config.rolling_rps) is int and config.rolling_rps > 0, "rolling_rps must be positive")
    require(type(config.rolling_seconds) is int and config.rolling_seconds >= 120,
            "rolling_seconds must be at least 120")
    require(config.rolling_seconds >= math.ceil(config.load_vus / config.rolling_rps) + 10,
            "rolling window must allow every VU a paced measured request plus 10s margin")
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
            required = (
                "P2.1-placement-and-routing", "P2.2-upstream-and-logical-dedup", "P2.3-owner-failover",
                "P2.4-clock-skew", "P2.5-freeze-resume", "P2.6-scaling-load",
                "P2.7-throughput-ratio", "P2.8-rolling-update",
            )
            run.report["all_eight_checks_passed"] = all(
                run.report["checks"].get(name, {}).get("status") == "pass" for name in required)
            run.report["pdf_sustained_load_minimum_met"] = config.load_vus >= 10_000 and (config.scaling_seconds or config.load_seconds) >= 120
            run.report["automated_criteria_complete"] = (run.report["all_eight_checks_passed"]
                and run.report["pdf_sustained_load_minimum_met"] and not run.container_nodes)
            run.report["status"] = ("automated_checks_passed_manual_evidence_pending"
                if run.report["automated_criteria_complete"] else "development_checks_passed_criteria_incomplete")
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
