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

    # Criterion 6 (partial): compare the Audit API count/checksum with the store.
    compare_audit_checksum: bool = True

    # Per-request bound.
    request_timeout: float = 15

    # None creates a fresh docs/evidence/m2-p4-<timestamp>-<id> directory per run.
    output_dir: Path | None = None


CONFIG = TestConfig()

MANUAL_EVIDENCE = [
    "Criterion 4: 60x burst in the exam session; Grafana 'P4 Feed Satelit' lag and p95 panels",
    "Criterion 6: comparison with the assistants' reference results and an explanation of differences",
    "Criterion 7: the live feed processed on screen during the demo recording",
    "Report: event time vs processing time, allowed lateness, exactly-once evidence",
]


def validate_config(config):
    for name in ("observe_seconds", "catch_up_timeout", "resume_timeout", "request_timeout"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"CONFIG.{name} must be finite and positive")
    require(config.max_lag_events >= 0, "CONFIG.max_lag_events must not be negative")


if __name__ == "__main__":
    sys.exit(main(4, replace(CONFIG), [group1_feed, group2_fires, group3_recovery],
                  ("AGGREGATOR_TOKEN", "SATELLITE_GROUP_TOKEN"), MANUAL_EVIDENCE, validate_config))
