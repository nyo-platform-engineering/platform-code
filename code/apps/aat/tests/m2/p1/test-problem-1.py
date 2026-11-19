#!/usr/bin/env python3
"""Configure and run M2 P1; press Enter between the three groups' test steps."""
from dataclasses import dataclass, replace
import datetime as dt
import math
from pathlib import Path
import sys
import uuid

import group1_distribution
import group2_recovery
import group3_analytics
from helper import output, INFRA, ROOT, Run, endpoint, require, utc, verify_environment


@dataclass
class TestConfig:
    # Development data based on our BMKG mock; replace with official inputs later.
    catalog: Path = Path(__file__).resolve().parent / "data/mock-catalog.ndjson"
    reference: Path = Path(__file__).resolve().parent / "data/mock-reference.json"

    # Criteria 4–6: lose one worker, rebuild it, then lose the other worker.
    worker: str = "aat-worker-1"
    second_worker: str = "aat-worker-2"
    # Enter triggers local worker storage erasure/power loss, then empty-volume rebuild.
    automatic_worker_actions: bool = True

    # Connection: None opens a temporary port-forward to a surviving Aggregator.
    url: str | None = None
    env_file: Path = INFRA / ".env.runtime"
    kubeconfig: Path = INFRA / ".state/kubeconfig"

    # Criteria 1–3: sustained catalog/regional load and latest-50 pagination.
    batch_size: int = 100
    surge_events: int = 100
    load_seconds: float = 20
    # Export in larger pages; the separate latest-50 check still uses exactly 50.
    page_size: int = 200
    max_pages: int = 10000

    # Criterion 7: UTC calendar buckets covering the full catalog time range.
    interval: str = "month"
    # None ends the exact chart comparison after the historical catalog's last day.
    # Set an RFC3339 end date to check another window; changing data is retried.
    analytics_end: str | None = None

    # Per-request bounds and sampling; replica/recovery waits continue until ready.
    request_timeout: float = 8
    max_read_seconds: float = 7
    sample_interval: float = 2
    metrics_interval: float = 5
    min_window_seconds: float = 20

    # None creates a fresh docs/evidence/m2-p1-<timestamp>-<id> directory per run.
    output_dir: Path | None = None


CONFIG = TestConfig()


def validate_config(config):
    require(sys.stdin.isatty(), "run task m2:p1 in an interactive terminal")
    for name in ("catalog", "reference"):
        value = getattr(config, name)
        require(value is not None, f"set CONFIG.{name} in test-problem-1.py to the dataset file")
        path = Path(value).expanduser().resolve()
        require(path.is_file(), f"CONFIG.{name} must point to an existing file")
        setattr(config, name, path)

    require(1 <= config.page_size <= 1000, "CONFIG.page_size must be 1..1000")
    require(1 <= config.batch_size <= 200, "CONFIG.batch_size must be 1..200")
    require(config.max_pages > 0, "CONFIG.max_pages must be positive")
    require(config.surge_events >= 51, "CONFIG.surge_events must be at least 51")
    require(config.interval in ("hour", "day", "week", "month", "year"), "invalid CONFIG.interval")
    for name in (
        "request_timeout", "max_read_seconds", "sample_interval",
        "metrics_interval", "min_window_seconds", "load_seconds",
    ):
        duration = getattr(config, name)
        require(math.isfinite(duration) and duration > 0, f"CONFIG.{name} must be finite and positive")


def create_evidence_folder(config):
    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S")
    run_name = f"m2-p1-{timestamp}-{uuid.uuid4().hex[:8]}"
    folder = (config.output_dir or ROOT / "docs/evidence" / run_name).resolve()
    folder.mkdir(parents=True, exist_ok=False)
    return folder


def main():
    if len(sys.argv) > 1:
        print("This test has no command-line options. Edit CONFIG in test-problem-1.py.", file=sys.stderr)
        return 2

    config = replace(CONFIG)
    folder = create_evidence_folder(config)
    run = Run(config, folder)
    exit_code = 1

    try:
        validate_config(config)
        verify_environment(run)
        with endpoint(config, run.cluster, folder):
            group1_distribution.run_group(run)  # Criteria 1–3: data, load, global reads.
            group2_recovery.run_group(run)      # Criteria 4–6: loss, rejoin, another loss.
            group3_analytics.run_group(run)     # Dashboard correctness and partial results.
        run.report["status"] = "automated_checks_passed_manual_evidence_pending"
        exit_code = 0
    except (Exception, KeyboardInterrupt) as error:
        run.report["status"] = "interrupted" if isinstance(error, KeyboardInterrupt) else "failed"
        message = str(error) if isinstance(error, (ValueError, RuntimeError)) else type(error).__name__
        run.report["error"] = message
        output("FAIL", "P1 check failed", message)
    finally:
        checks = run.report["automated_checks"]
        run.report["disk_erasure_verified"] = all(
            checks.get(name, {}).get("result", {}).get("disk_erasure_verified", False)
            for name in ("first-loss", "first-worker-rejoin", "second-loss", "final-rejoin")
        )
        run.report["finished_at"] = utc()
        run.save("result.json", run.report)
        output("RESULT", "Stage summary", *(
            f"{check['status'].upper()}: {name}"
            for name, check in run.report["automated_checks"].items()
        ))
        if exit_code == 0:
            output("PASS", "All automated P1 checks passed")
        output("INFO", "Evidence", str(folder))
        output("MANUAL", "Pending evidence",
               "Official dataset/Tester Agent, browser demo, and report/IO measurements")
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
