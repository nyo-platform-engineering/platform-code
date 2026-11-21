#!/usr/bin/env python3
"""Guided P3 tests; edit CONFIG below."""
from __future__ import annotations
from contextlib import ExitStack
from dataclasses import dataclass, replace
import datetime as dt
import math
from pathlib import Path
import sys
import uuid

import group1_schema
import group2_recovery
import group3_reports
from support import INFRA, Run, output, require
from common import console_log


@dataclass
class TestConfig:
    kubeconfig: Path = INFRA / ".state/kubeconfig"
    env_file: Path = INFRA / ".env.runtime"
    groups: tuple = (1, 2, 3)
    request_timeout: float = 8
    recovery_seconds: float = 180
    park_observe_seconds: float = 6
    retry_observe_seconds: float = 30
    report_count: int = 10
    duplicate_requests: int = 4
    # Local latency budget; the specification gives no numeric limit.
    max_accept_ms: float = 2000
    max_pages: int = 1000
    output_dir: Path | None = None


CONFIG = TestConfig()


def validate_config(config):
    require(config.groups and len(set(config.groups)) == len(config.groups)
            and set(config.groups) <= {1, 2, 3}, "groups must be unique selections from 1, 2, 3")
    for name in ("request_timeout", "recovery_seconds", "park_observe_seconds", "retry_observe_seconds", "max_accept_ms"):
        value = getattr(config, name)
        require(not isinstance(value, bool) and math.isfinite(value) and value > 0, name + " must be finite and positive")
    require(config.recovery_seconds >= 90, "allow at least 90 seconds for startup/recovery")
    require(config.park_observe_seconds >= 5, "observe parked messages for at least five seconds")
    require(20 <= config.retry_observe_seconds <= 35, "retry observation must be 20..35 seconds within the fault window")
    require(1 <= config.report_count <= 100 and type(config.report_count) is int, "report_count must be 1..100")
    require(2 <= config.duplicate_requests <= 10 and type(config.duplicate_requests) is int, "duplicate_requests must be 2..10")
    require(type(config.max_pages) is int and config.max_pages > 0, "max_pages must be positive")
    require(config.request_timeout * 1000 > config.max_accept_ms, "HTTP deadline must exceed the acceptance budget")


def main():
    if len(sys.argv) != 1:
        print("No command-line options. Edit CONFIG in test-problem-3.py.", file=sys.stderr)
        return 2
    with ExitStack() as console:
        run, code = None, 1
        try:
            require(sys.stdin.isatty(), "run task m2:p3 in an interactive terminal")
            config = replace(CONFIG)
            validate_config(config)
            stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S")
            name = f"m2-p3-{stamp}-{uuid.uuid4().hex[:8]}"
            folder = Path(config.output_dir or Path(__file__).resolve().parent / "evidence" / name).resolve()
            folder.mkdir(parents=True, exist_ok=False)
            console.enter_context(console_log(folder))
            run = Run(config, folder)
            run.stage("preflight", run.preflight)
            for number, group in ((1, group1_schema), (2, group2_recovery), (3, group3_reports)):
                if number in config.groups:
                    group.run_group(run)
            code = 0
        except (Exception, KeyboardInterrupt) as error:
            message = str(error) if isinstance(error, (ValueError, RuntimeError)) else type(error).__name__
            output("FAIL", run.clean(message) if run else message)
            if run:
                run.report["status"] = "interrupted" if isinstance(error, KeyboardInterrupt) else "failed"
                run.report["error"] = run.clean(message)
        finally:
            if run:
                code = run.finish(code)
                output("INFO", f"Evidence: {run.folder}")
                output("MANUAL", "Automated scenarios do not replace official Tester Agent, durability explanation, report and demo.")
        return code


if __name__ == "__main__":
    sys.exit(main())
