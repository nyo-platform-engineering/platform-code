#!/usr/bin/env python3
"""Configure and run M2 P5 (Sensor Pantai); press Enter between the test steps."""
from dataclasses import dataclass, replace
import math
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from common import INFRA, main, require  # noqa: E402

import group1_alerts  # noqa: E402
import group2_outage  # noqa: E402
import group3_overload  # noqa: E402
import group4_critical  # noqa: E402


@dataclass
class TestConfig:
    # Connection: the Kubernetes lab and its runtime credentials (PANTAI_AUTH_TOKEN
    # is created by task up; the client secrets drive the Media/Field Team checks).
    env_file: Path = INFRA / ".env.runtime"
    kubeconfig: Path = INFRA / ".state/kubeconfig"

    # Criterion 1 (Group 4, run last): after restarting the sensor, wait this long for
    # its scenario's critical readings (about 30/90/150 s in at speed 60). Readings the
    # store already has are deduplicated and do not alert twice; then it is skipped.
    wait_critical_seconds: float = 300
    alert_p95_ms: float = 5000

    # Criteria 3, 4, 6: scale the sensor to zero for this long, then back to one.
    outage: bool = True
    outage_seconds: float = 30
    max_requests_per_second_during_outage: float = 0.5
    client_p95_ms_during_outage: float = 1000
    recovery_timeout: float = 240

    # Criterion 5: concurrent Media clients (above CLIENT_MAX_CONCURRENT) and the
    # Field Team requests sent while they flood the Client API.
    media_workers: int = 48
    field_requests: int = 20

    # Per-request bound; poller/state waits use their own timeouts above.
    request_timeout: float = 15
    startup_timeout: float = 180

    # None creates a fresh evidence/m2-p5-<timestamp>-<id> folder beside this file.
    output_dir: Path | None = None


CONFIG = TestConfig()

MANUAL_EVIDENCE = [
    "Criterion 2: contract violations and broken critical readings (unit/integration tests; the image cannot be changed)",
    "Criterion 6: game day with the exam profile and Tester Agent, no intervention during the recovery window",
    "Criterion 7: Grafana P5 panels (breaker, requests to sensor, critical alert p95, shed requests) explained on screen",
    "Criterion 8: SLO and error budget used during the game day; post-mortem in the report",
]


def validate_config(config):
    for name in ("wait_critical_seconds", "alert_p95_ms", "outage_seconds", "client_p95_ms_during_outage",
                 "recovery_timeout", "request_timeout", "startup_timeout", "max_requests_per_second_during_outage"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"CONFIG.{name} must be finite and positive")
    require(config.media_workers >= 2 and config.field_requests >= 1, "CONFIG.media_workers/field_requests too small")


if __name__ == "__main__":
    sys.exit(main(5, replace(CONFIG), [group1_alerts, group2_outage, group3_overload, group4_critical],
                  ("AGGREGATOR_TOKEN", "PANTAI_AUTH_TOKEN", "PUBLIC_CLIENT_SECRET",
                   "RESPONDER_CLIENT_SECRET", "ANALYST_CLIENT_SECRET"), MANUAL_EVIDENCE, validate_config))
