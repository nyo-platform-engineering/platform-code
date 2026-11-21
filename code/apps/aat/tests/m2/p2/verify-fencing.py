#!/usr/bin/env python3
"""Verify storage fencing against the lab database using temporary schemas."""
from dataclasses import dataclass
import datetime as dt
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import time
import urllib.parse
import uuid

from helper import INFRA, NAMESPACE, ROOT, read_credentials
from common import console_log, output, require


@dataclass
class VerificationConfig:
    kubeconfig: Path = INFRA / ".state/kubeconfig"
    env_file: Path = INFRA / ".env.runtime"
    go: str = "go"
    distributed_canonical: bool = True


CONFIG = VerificationConfig()
TESTS = (
    "TestM2PollOwnershipIsExclusiveAndIndependentPerSource",
    "TestM2TerminatedOwnerCannotCommitAfterTakeover",
    "TestM2ServerExpiresIdleOwnerWithoutClientCooperation",
    "TestM2DelayedUpstreamResponseIsFencedAfterTakeover",
    "TestM2PollCommitRollsBackHazardsOutboxAndProgressTogether",
    "TestM2FrozenProcessCannotWriteUntilOwnershipRenewed",
    "TestM2OldFenceCannotMoveToFreshDatabaseConnection",
    "TestM2StorageReceiptRequiresSuccessfulCommit",
    "TestM2InboxReceiptsCoverCaptureAndProcessing",
)


def verify(config, folder):
    require(shutil.which(config.go), "Go is required for the storage fencing verifier")
    kube = ["kubectl", "--kubeconfig", str(config.kubeconfig), "--request-timeout=10s"]
    nodes = json.loads(subprocess.check_output(kube + ["get", "nodes", "-o", "json"], text=True))["items"]
    report = {"started_at": dt.datetime.now(dt.timezone.utc).isoformat(), "status": "running",
              "scope": "database fencing and one polling process pause; not node clock skew or VM suspension",
              "node_clock_shift_tested": False, "vm_suspend_tested": False,
              "nodes": [{"name": n["metadata"]["name"], "kernel": n["status"]["nodeInfo"]["kernelVersion"]} for n in nodes],
              "tests": {}, "freeze_evidence": [], "storage_commits": [], "storage_rejections": []}
    def save():
        (folder / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    save()
    credentials = read_credentials(config.env_file)
    require(credentials.get("DATABASE_URL"), "DATABASE_URL missing from runtime configuration")
    target = urllib.parse.urlsplit(credentials["DATABASE_URL"])
    require(target.scheme in ("postgres", "postgresql") and target.username, "invalid runtime DATABASE_URL")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    output("INFO", "Using temporary schemas; application data and the capped RPS configuration are preserved")
    with (folder / "port-forward.log").open("w") as log:
        forward = subprocess.Popen(kube + ["port-forward", "--address=127.0.0.1", "-n", NAMESPACE,
                                          "svc/postgres", f"{port}:5432"], stdout=log, stderr=log)
        generator = None
        try:
            deadline = time.monotonic() + 30
            while True:
                require(forward.poll() is None, "database port-forward exited; inspect port-forward.log")
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                        break
                except OSError:
                    require(time.monotonic() < deadline, "database port-forward did not become ready")
                    time.sleep(0.2)
            userinfo = target.netloc.rsplit("@", 1)[0]
            url = urllib.parse.urlunsplit(target._replace(netloc=f"{userinfo}@127.0.0.1:{port}"))
            env = dict(os.environ, AAT_TEST_DATABASE_URL=url,
                       AAT_TEST_CITUS="1" if config.distributed_canonical else "0")
            command = [config.go, "test", "-race", "-json", "-count=1", "./aggregator/cmd/server",
                       "-run", "^(" + "|".join(TESTS) + ")$"]
            report["command"] = command
            output("INFO", f"Running {len(TESTS)} database tests; the SIGSTOP case holds one child process for 23 seconds")
            generator = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                         stderr=subprocess.STDOUT, text=True, start_new_session=True)
            secrets = [url, credentials["DATABASE_URL"]]
            if target.password:
                secrets.append(urllib.parse.unquote(target.password))
            with (folder / "go-test.jsonl").open("w") as log:
                for line in generator.stdout:
                    for secret in secrets:
                        line = line.replace(secret, "[REDACTED]")
                    log.write(line)
                    log.flush()
                    try:
                        event = json.loads(line)
                    except ValueError:
                        output("INFO", line.rstrip())
                        continue
                    name, action = event.get("Test"), event.get("Action")
                    if name in TESTS and action in ("run", "pass", "fail", "skip"):
                        report["tests"][name] = action
                        output("RUN" if action == "run" else action.upper(), name)
                    message = event.get("Output", "").strip()
                    if "FENCING_PHASE " in message:
                        output("WAIT", message.split("FENCING_PHASE ", 1)[1])
                    if "FENCING_EVIDENCE " in message:
                        evidence = json.loads(message.split("FENCING_EVIDENCE ", 1)[1])
                        report["freeze_evidence"].append(evidence)
                        output("RESULT", f"Old epoch {evidence['before']['Epoch']} rejected; takeover epoch {evidence['takeover']['Epoch']}; renewed epoch {evidence['renewed']['Epoch']}")
                    if "STORAGE_EVIDENCE " in message:
                        message = message.split("STORAGE_EVIDENCE ", 1)[1]
                    try:
                        receipt = json.loads(message)
                    except ValueError:
                        receipt = {}
                    if receipt.get("msg") == "poll storage committed":
                        report["storage_commits"].append(receipt)
                    elif receipt.get("msg") == "poll storage rejected":
                        report["storage_rejections"].append(receipt)
                    if action in ("pass", "fail", "skip"):
                        save()
            code = generator.wait(timeout=30)
            require(code == 0, "Go storage tests failed; inspect go-test.jsonl")
            require(all(report["tests"].get(name) == "pass" for name in TESTS), "some database tests did not run or were skipped")
            require(report["freeze_evidence"], "process freeze evidence missing")
            require(not config.distributed_canonical or all(x.get("distributed_canonical") for x in report["freeze_evidence"]),
                    "Citus distributed writes were not verified")
            require(report["storage_commits"] and report["storage_rejections"], "post-commit or rejection evidence missing")
            require(any(x.get("operation") == "validate_owner" and x.get("reason") in ("session_unavailable", "fence_mismatch")
                        for x in report["storage_rejections"]), "resume validation rejection evidence missing")
            for receipt in report["storage_commits"]:
                require(receipt.get("owner") and receipt.get("backend_pid", 0) > 0
                        and receipt.get("db_observed_at") and str(receipt.get("transaction_id", "")).isdigit(),
                        "commit receipt missing database/fence metadata")
            report["status"] = "passed_local_storage_verification_vm_faults_pending"
            output("PASS", f"All {len(TESTS)} storage fencing checks passed; VM clock/suspend tests remain separate")
        except BaseException as error:
            report["status"] = "failed"
            report["error_type"] = type(error).__name__
            raise
        finally:
            if generator is not None:
                try:
                    os.killpg(generator.pid, signal.SIGCONT)
                    os.killpg(generator.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                if generator.poll() is None:
                    try:
                        generator.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(generator.pid, signal.SIGKILL)
                        generator.wait()
            forward.terminate()
            try:
                forward.wait(timeout=5)
            except subprocess.TimeoutExpired:
                forward.kill()
                forward.wait()
            report["finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
            report["port_forward_closed"] = forward.poll() is not None
            save()


def main():
    name = "m2-p2-fencing-" + dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S").lower() + "-" + uuid.uuid4().hex[:8]
    folder = Path(__file__).resolve().parent / "evidence" / name
    folder.mkdir(parents=True)
    code = 0
    with console_log(folder):
        try:
            verify(CONFIG, folder)
        except (ValueError, OSError, subprocess.SubprocessError) as error:
            output("FAIL", str(error))
            code = 1
        except KeyboardInterrupt:
            output("FAIL", "Verification interrupted")
            code = 130
        output("INFO", f"Evidence: {folder}")
    return code


if __name__ == "__main__":
    raise SystemExit(main())
