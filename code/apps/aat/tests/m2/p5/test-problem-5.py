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
import group5_gameday  # noqa: E402
import gameday  # noqa: E402
import helper  # noqa: E402


@dataclass
class TestConfig:
    # Connection: the Kubernetes lab and its runtime credentials (PANTAI_AUTH_TOKEN
    # is created by task up; the client secrets drive the Media/Field Team checks).
    env_file: Path = INFRA / ".env.runtime"
    kubeconfig: Path = INFRA / ".state/kubeconfig"

    # Availability probes must use the public service path, never a tunnel pinned to one Pod.
    require_nodeports: tuple = ("client",)

    # Criterion 1 runs first after resetting only SENSOR_PANTAI test data.
    # All three practice critical readings arrive around 30/90/150 s at speed 60.
    wait_critical_seconds: float = 300
    alert_p95_ms: float = 5000

    # Criteria 3, 4, 6: scale the sensor to zero for this long, then back to one.
    outage: bool = True
    outage_seconds: float = 30
    max_requests_per_second_during_outage: float = 0.5
    client_p95_ms_during_outage: float = 1000
    # Criterion 3 uses M1's latency target: single-source requests for BMKG, PVMBG and
    # SATELIT stay under this p95 while the sensor is down. Samples are per source.
    other_source_p95_ms: float = 300
    outage_samples: int = 20
    recovery_timeout: float = 240

    # Criterion 5: concurrent Media clients (above CLIENT_MAX_CONCURRENT) and the
    # Field Team requests sent while they flood the Client API.
    media_workers: int = 48
    field_requests: int = 20

    # Criterion 6 rehearsal (group 5): one combined scenario, then hands off. Faults
    # last fault_window seconds and all end together; the system must then be fully
    # healthy on its own within recovery_timeout (stage total under 4 minutes).
    # The Field Team target is the criterion 8 reliability target; change it if the
    # group defines another one. Node loss powers off `worker` (never the control plane).
    gameday: bool = True
    gameday_node_loss: bool = True
    worker: str = "aat-worker-2"
    fault_window: float = 45
    recovery_timeout: float = 150
    baseline_seconds: float = 5
    after_recovery_seconds: float = 5
    health_interval: float = 5
    probe_interval: float = 0.5
    probe_timeout: float = 5
    field_availability_slo: float = 0.99
    # "Healthy" = these Deployments ready, these sources fresh in client answers,
    # complete answers, sensor breaker closed, critical lane drained.
    deployments: tuple = ("aggregator", "client", "sensor-pantai", "field-notifier", "dashboard-updater", "bmkg", "pvmbg")
    fresh_sources: tuple = ("BMKG", "PVMBG", "SENSOR_PANTAI")

    # Per-request bound; poller/state waits use their own timeouts above.
    request_timeout: float = 15
    startup_timeout: float = 180

    # None creates a fresh evidence/m2-p5-<timestamp>-<id> folder beside this file.
    output_dir: Path | None = None


CONFIG = TestConfig()

MANUAL_EVIDENCE = [
    "Criterion 2: contract violations and broken critical readings (unit/integration tests; the image cannot be changed)",
    "Criterion 6: the real game day (exam profile + Tester Agent); group 5 is a rehearsal on the practice lab",
    "Criterion 7: Grafana P5 panels (breaker, requests to sensor, critical alert p95, shed requests) explained on screen",
    "Criterion 8: SLO and error budget used during the game day; post-mortem in the report",
]


def validate_config(config):
    for name in ("wait_critical_seconds", "alert_p95_ms", "outage_seconds", "client_p95_ms_during_outage",
                 "recovery_timeout", "request_timeout", "startup_timeout", "max_requests_per_second_during_outage",
                 "other_source_p95_ms", "fault_window", "recovery_timeout", "baseline_seconds",
                 "health_interval", "probe_interval", "probe_timeout"):
        value = getattr(config, name)
        require(math.isfinite(value) and value > 0, f"CONFIG.{name} must be finite and positive")
    require(0 < config.field_availability_slo < 1, "CONFIG.field_availability_slo must be between 0 and 1")
    require(config.baseline_seconds + config.fault_window + config.recovery_timeout + config.after_recovery_seconds < 240,
            "game day rehearsal must stay under 4 minutes")
    gameday.scenario(config).validate()
    require(config.outage_samples >= 10, "CONFIG.outage_samples must be at least 10 for a p95")
    require(config.media_workers >= 2 and config.field_requests >= 1, "CONFIG.media_workers/field_requests too small")
    require(config.wait_critical_seconds >= 180, "wait_critical_seconds must cover all 150s of the practice scenario plus recovery margin")


def run_group(run):
    input("Enter → reset SENSOR_PANTAI events, inbox/outbox and cursor for this practice run "
          "(briefly stops Aggregator; other sources and notification history are preserved): ")
    with helper.practice_run(run) as replicas:
        run.stage("sensor-practice-reset", helper.reset_sensor, run, replicas)
        # Prove the complete scenario before the outage deliberately restarts it.
        for group in (group4_critical, group1_alerts, group2_outage, group3_overload, group5_gameday):
            group.run_group(run)


if __name__ == "__main__":
    # A run killed with a fault still active (Ctrl+C is handled; kill -9 is not):
    #   task m2:p5 -- --restore <evidence folder>
    if len(sys.argv) == 3 and sys.argv[1] == "--restore":
        from common import Run
        folder = Path(sys.argv[2]).resolve()
        restore_run = Run(5, replace(CONFIG), [], folder=folder)
        errors = gameday.restore(restore_run, restore_run.config)
        if (folder / "setup-baseline.json").is_file():
            gameday.restore_argo(restore_run, folder / "setup-baseline.json")
        print("restored" if not errors else "restore incomplete: " + "; ".join(errors))
        sys.exit(1 if errors else 0)
    sys.exit(main(5, replace(CONFIG), [sys.modules[__name__]],
                  ("AGGREGATOR_TOKEN", "PANTAI_AUTH_TOKEN", "PVMBG_TOKEN", "PUBLIC_CLIENT_SECRET",
                   "RESPONDER_CLIENT_SECRET", "ANALYST_CLIENT_SECRET"), MANUAL_EVIDENCE, validate_config))
