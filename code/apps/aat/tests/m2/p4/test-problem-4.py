#!/usr/bin/env python3
"""Configure and run M2 P4 (Feed Satelit); press Enter between the test steps."""
from dataclasses import dataclass, replace
import math
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from common import INFRA, main, require  # noqa: E402

import group1_feed  # noqa: E402
import group2_fires  # noqa: E402
import controlled_checks  # noqa: E402
import deployment_checks  # noqa: E402


@dataclass
class TestConfig:
    # Connection: the Kubernetes lab and its runtime credentials. The Aggregator
    # needs SATELLITE_GROUP_TOKEN there; the feed allows one connection per token.
    env_file: Path = INFRA / ".env.runtime"
    kubeconfig: Path = INFRA / ".state/kubeconfig"

    # Criterion 7: observe activity; initial backlog is reported without blocking the suite.
    observe_seconds: float = 180
    lag_observe_seconds: float = 30
    max_lag_events: int = 500
    catch_up_timeout: float = 300  # Only waits for the first committed offset.

    # Criteria 5 and 8: crash the active consumer owner and wait for recovery.
    resume_timeout: float = 300

    # Consumer statistics (group 1, from the Aggregator logs, see consumer_stats.py).
    # The batch and lag lines are read from Loki over the last loki_lookback_hours (they
    # survive pod restarts and log rotation); use_loki False reads only the current pods'
    # logs. Time is cut into window_seconds windows: behind = lag >= catch_up_min_lag
    # (capacity of the most recent catch-up: min_catch_up_windows of them, or for a short
    # one min_catch_up_batches full batches in a row); current = lag <= max_lag_events
    # (live rate, busiest peak_window_seconds). satellite_batch_size must match the
    # Aggregator's SATELLITE_BATCH_SIZE (default 200).
    use_loki: bool = True
    loki_lookback_hours: float = 6
    window_seconds: float = 10
    catch_up_min_lag: int = 2000
    min_catch_up_windows: int = 6
    min_catch_up_batches: int = 10
    peak_window_seconds: float = 60
    satellite_batch_size: int = 200

    # Controlled source served in cluster; existing Aggregator/Client/Citus are the test targets.
    verification_normal_rate: int = 20
    verification_burst_seconds: int = 10

    # Internal consistency smoke check; not the official criterion 6 reference.
    compare_audit_checksum: bool = True

    # No alternate application or database is started.
    reference_file: Path | None = None
    reference_difference_explanation: str = ""
    observe_external_burst: bool = False
    burst_start_timeout: float = 60
    burst_observation_timeout: float = 900
    burst_sample_seconds: float = 5

    # Waits have no time limit: each one logs what it is waiting for (and the current
    # value, e.g. the committed offset) every wait_log_seconds until it is done or you
    # press Ctrl+C, which still restores the deployment. The 60 s recovery limit of
    # criterion 4 is a requirement, not a wait, and stays.
    wait_timeouts: bool = False
    wait_log_seconds: float = 15

    # Per-request bound.
    request_timeout: float = 15

    # None creates a fresh evidence/m2-p4-<timestamp>-<id> folder beside this file.
    output_dir: Path | None = None


CONFIG = TestConfig()

MANUAL_EVIDENCE = [
    "Criterion 6: assistants reference comparison when their results become available",
    "Criterion 7: record the deployed Client dashboard during the live demo",
    "Report: event time vs processing time, allowed lateness, exactly-once evidence",
]


def validate_config(config):
    for name in ("observe_seconds", "lag_observe_seconds", "catch_up_timeout", "resume_timeout", "request_timeout",
                 "burst_start_timeout", "burst_observation_timeout", "burst_sample_seconds"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"CONFIG.{name} must be finite and positive")
    require(config.max_lag_events >= 0, "CONFIG.max_lag_events must not be negative")
    require(config.satellite_batch_size >= 1, "CONFIG.satellite_batch_size must be positive")
    require(config.min_catch_up_windows >= 1, "CONFIG.min_catch_up_windows must be at least 1")
    require(config.min_catch_up_batches >= 2, "CONFIG.min_catch_up_batches must be at least 2")
    require(config.catch_up_min_lag > config.max_lag_events, "CONFIG.catch_up_min_lag must be above max_lag_events")
    for name in ("peak_window_seconds", "loki_lookback_hours", "window_seconds"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"CONFIG.{name} must be finite and positive")
    require(type(config.verification_normal_rate) is int and config.verification_normal_rate > 0,
            "CONFIG.verification_normal_rate must be a positive integer")
    require(type(config.verification_burst_seconds) is int and config.verification_burst_seconds > 0,
            "CONFIG.verification_burst_seconds must be a positive integer")
    require(config.verification_normal_rate * config.verification_burst_seconds * 60 <= 1000000,
            "controlled burst is too large for the fixture source")



if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--restore", type=Path, help="restore a saved controlled-run journal")
    args = parser.parse_args()
    if args.restore:
        import json
        from common import Run
        from verification_session import Verification
        baseline = json.loads(args.restore.read_text())
        run = Run(4, replace(CONFIG), [], folder=args.restore.resolve().parent)
        verification = Verification(run, baseline=baseline)
        verification.restore()
        print("P4 deployment restored; see restore-result.json")
        sys.exit(0)
    sys.exit(main(4, replace(CONFIG), [deployment_checks.PREFLIGHT, group1_feed, group2_fires, controlled_checks, deployment_checks.FINAL],
                  ("AGGREGATOR_TOKEN", "SATELLITE_GROUP_TOKEN", "ANALYST_CLIENT_SECRET"), MANUAL_EVIDENCE, validate_config))
