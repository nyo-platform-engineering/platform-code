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
import group3_recovery  # noqa: E402
import group4_burst  # noqa: E402


@dataclass
class TestConfig:
    # Connection: the Kubernetes lab and its runtime credentials. The Aggregator
    # needs SATELLITE_GROUP_TOKEN there; the feed allows one connection per token.
    env_file: Path = INFRA / ".env.runtime"
    kubeconfig: Path = INFRA / ".state/kubeconfig"

    # Criteria 7 and 4: watch the offset move, then wait until the lag is small.
    observe_seconds: float = 15
    max_lag_events: int = 500
    catch_up_timeout: float = 300

    # Criteria 5 and 8: kill every Aggregator mid-stream and wait for the resume.
    restart_aggregators: bool = True
    resume_timeout: float = 300

    # Consumer statistics (group 1, from the Aggregator logs, see consumer_stats.py).
    # The batch and lag lines are read from Loki over the last loki_lookback_hours (they
    # survive pod restarts and log rotation); use_loki False reads only the current pods'
    # logs. Time is cut into window_seconds windows: behind = lag >= catch_up_min_lag
    # (capacity, needs min_catch_up_windows of them); current = lag <= max_lag_events
    # (live rate, busiest peak_window_seconds). satellite_batch_size must match the
    # Aggregator's SATELLITE_BATCH_SIZE (default 200).
    use_loki: bool = True
    loki_lookback_hours: float = 6
    window_seconds: float = 10
    catch_up_min_lag: int = 2000
    min_catch_up_windows: int = 6
    peak_window_seconds: float = 60
    satellite_batch_size: int = 200

    # Criterion 4 forecast (group 4, run last): simulate a burst of burst_factor x the
    # live average (realistic) and burst_factor x the busiest peak_window_seconds (worst
    # case: satellite passes back to back) for each burst_seconds, with the measured
    # capacity. The verdict: the realistic case for verdict_burst_seconds is current
    # (lag <= max_lag_events) within 60 s. A model, not the exam burst.
    simulate_burst: bool = True
    burst_factor: float = 60
    burst_seconds: tuple = (60, 120, 300, 600)
    verdict_burst_seconds: float = 120

    # Criterion 6 (partial): compare the Audit API count/checksum with the store.
    compare_audit_checksum: bool = True

    # Per-request bound.
    request_timeout: float = 15

    # None creates a fresh evidence/m2-p4-<timestamp>-<id> folder beside this file.
    output_dir: Path | None = None


CONFIG = TestConfig()

MANUAL_EVIDENCE = [
    "Criterion 4: 60x burst in the exam session; Grafana 'P4 Feed Satelit' lag and p95 panels "
    "(group 4 only forecasts it from measured numbers)",
    "Criterion 6: comparison with the assistants' reference results and an explanation of differences",
    "Criterion 7: the live feed processed on screen during the demo recording",
    "Report: event time vs processing time, allowed lateness, exactly-once evidence",
]


def validate_config(config):
    for name in ("observe_seconds", "catch_up_timeout", "resume_timeout", "request_timeout"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"CONFIG.{name} must be finite and positive")
    require(config.max_lag_events >= 0, "CONFIG.max_lag_events must not be negative")
    require(config.satellite_batch_size >= 1, "CONFIG.satellite_batch_size must be positive")
    require(config.min_catch_up_windows >= 1, "CONFIG.min_catch_up_windows must be at least 1")
    require(config.catch_up_min_lag > config.max_lag_events, "CONFIG.catch_up_min_lag must be above max_lag_events")
    for name in ("peak_window_seconds", "burst_factor", "loki_lookback_hours", "window_seconds"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"CONFIG.{name} must be finite and positive")
    require(config.burst_seconds and all(s > 0 for s in config.burst_seconds), "CONFIG.burst_seconds must be positive")
    require(config.verdict_burst_seconds in config.burst_seconds, "CONFIG.verdict_burst_seconds must be in burst_seconds")


if __name__ == "__main__":
    sys.exit(main(4, replace(CONFIG), [group1_feed, group2_fires, group3_recovery, group4_burst],
                  ("AGGREGATOR_TOKEN", "SATELLITE_GROUP_TOKEN"), MANUAL_EVIDENCE, validate_config))
