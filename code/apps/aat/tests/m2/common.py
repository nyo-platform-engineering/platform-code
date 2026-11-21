"""Shared M2 utilities and setup for the guided P4 and P5 tests.

P1 also reuses the paths, validation, timestamps, and console output here.
The tests run against the Kubernetes lab (VM cluster, or `task up -- --novm` for
development), read credentials from code/infrastructure/.env.runtime, reach the
services through temporary `kubectl port-forward`s, and save evidence under
tests/m2/p<n>/evidence/m2-p<n>-<timestamp>-<id>/ (like P1), including a full
console.log of the terminal session.
"""
import builtins
from contextlib import ExitStack, contextmanager, redirect_stderr, redirect_stdout
import datetime as dt
import json
from pathlib import Path
import shutil
import tempfile
import socket
import subprocess
import sys
import textwrap
import threading
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]  # code/apps/aat
INFRA = ROOT.parents[1] / "infrastructure"
NAMESPACE = "sistem-informasi-kebencanaan-bnpb"
SERVICE_PORTS = {"aggregator": 8083, "client": 8080, "auth": 8084, "nats": 4222}
# Client and Auth are also NodePorts; using them avoids pushing load tests
# through a single kubectl tunnel. The Aggregator is cluster-internal.
NODE_PORTS = {"client": 30080, "auth": 30084}


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def parse_time(text):
    """RFC 3339 from Go (nanoseconds allowed) or PostgreSQL to an aware datetime.

    Python 3.10's fromisoformat needs exactly 3 or 6 fraction digits and a +HH:MM
    offset, so pad or cut the fraction and expand PostgreSQL's short "+00".
    """
    text = text.strip().replace("Z", "+00:00")
    if len(text) > 10 and text[-3] in "+-" and text[-2:].isdigit():
        text += ":00"
    if "." in text:
        head, rest = text.split(".", 1)
        cut = next((i for i, ch in enumerate(rest) if ch in "+-"), len(rest))
        text = f"{head}.{rest[:cut][:6].ljust(6, '0')}{rest[cut:]}"
    return dt.datetime.fromisoformat(text)


def p95(values):
    ordered = sorted(values)
    if not ordered:
        return None
    return ordered[min(len(ordered) - 1, max(0, round(0.95 * len(ordered)) - 1))]


OUTPUT_LOCK = threading.Lock()


def output(kind, title, *details):
    lines = [f"[{kind}] {title}"]
    for detail in details:
        for line in str(detail).splitlines():
            if line:
                lines.append(textwrap.fill(line, width=100, initial_indent="  ", subsequent_indent="  ",
                                           break_long_words=False, break_on_hyphens=False))
    with OUTPUT_LOCK:
        # P1 can log the complete block while presenting a compact terminal view.
        writer = getattr(sys.stdout, "write_output", None)
        if writer is not None:
            writer(kind, title, details, "\n".join(lines) + "\n")
        else:
            print("\n".join(lines), flush=True)


def read_env(path):
    values = {}
    for line in Path(path).read_text().splitlines():
        if line.strip() and not line.lstrip().startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            values[key.strip()] = value.strip().strip("\"'")
    return values


def json_logs(text, *messages):
    """Structured log records, optionally only those whose msg is one of messages."""
    for line in text.splitlines():
        start = line.find("{")
        if start < 0:
            continue
        try:
            record = json.loads(line[start:])
        except ValueError:
            continue
        if not messages or record.get("msg") in messages:
            yield record


def kubectl(config, *command, json_output=False, timeout=60,
            request_timeout="20s", namespace=NAMESPACE):
    """Run kubectl without an instance; callers choose namespace and time bounds."""
    args = ["kubectl", "--kubeconfig", str(config.kubeconfig)]
    if namespace is not None:
        args.extend(["-n", namespace])
    args.extend([f"--request-timeout={request_timeout}", *command])
    completed = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    require(completed.returncode == 0, "kubectl failed: " + " ".join(command[:3]) + ": "
            + (completed.stderr or "").strip()[-300:])
    return json.loads(completed.stdout) if json_output else completed.stdout


class Cluster:
    def __init__(self, config):
        self.config = config

    def kubectl(self, *command, json_output=False, timeout=60):
        return kubectl(self.config, *command, json_output=json_output, timeout=timeout)

    def nodes(self):
        return self.kubectl("get", "nodes", "-o", "json", json_output=True)["items"]

    def logs(self, instance):
        return self.kubectl("logs", "-l", f"app.kubernetes.io/instance={instance}", "--tail=-1",
                            "--prefix=false", "--max-log-requests=10", timeout=120)

    def ready_replicas(self, deployment):
        status = self.kubectl("get", "deployment", deployment, "-o", "json", json_output=True)["status"]
        return status.get("readyReplicas", 0)


class Forwards:
    """Temporary port-forwards to Services; reopened after pods restart."""

    def __init__(self, config, folder):
        self.config, self.folder = config, folder
        self.processes = {}
        self.urls = {}

    def node_address(self):
        mode = INFRA / ".state/mode"
        if mode.is_file() and mode.read_text().strip() == "k3d":
            return "127.0.0.1"
        return "192.168.56.10"

    def open(self, service):
        self.close(service)
        if service in NODE_PORTS:
            url = f"http://{self.node_address()}:{NODE_PORTS[service]}"
            try:
                with urllib.request.urlopen(url + "/health", timeout=3) as response:
                    if response.status == 200:
                        self.urls[service] = url
                        return url
            except (urllib.error.URLError, OSError):
                pass  # not reachable from here: fall back to a port-forward
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        log = (self.folder / f"port-forward-{service}.log").open("a")
        process = subprocess.Popen(
            ["kubectl", "--kubeconfig", str(self.config.kubeconfig), "-n", NAMESPACE, "port-forward",
             f"svc/{service}", f"{port}:{SERVICE_PORTS[service]}", "--address=127.0.0.1"],
            stdout=log, stderr=log)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            require(process.poll() is None, f"port-forward to {service} exited; see port-forward-{service}.log")
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                    self.processes[service] = process
                    self.urls[service] = f"http://127.0.0.1:{port}"
                    return self.urls[service]
            except OSError:
                time.sleep(0.2)
        process.terminate()
        raise ValueError(f"port-forward to {service} did not become ready")

    def close(self, service=None):
        for name in [service] if service else list(self.processes):
            process = self.processes.pop(name, None)
            if process:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()


class Run:
    """Shared run state: config, cluster access, HTTP with evidence, report."""

    def __init__(self, problem, config, manual_evidence, folder=None, *, evidence_root=None):
        self.problem, self.config = problem, config
        if folder is not None:
            self.folder = Path(folder).resolve()
        else:
            stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S")
            base = Path(evidence_root) if evidence_root is not None else ROOT / "docs/evidence"
            self.folder = (config.output_dir or base / f"m2-p{problem}-{stamp}-{uuid.uuid4().hex[:8]}").resolve()
            self.folder.mkdir(parents=True, exist_ok=False)
        self.env = read_env(config.env_file)
        self.cluster = Cluster(config)
        self.forwards = Forwards(config, self.folder)
        self.secrets = {v for k, v in self.env.items()
                        if len(v) >= 16 and any(w in k for w in ("TOKEN", "SECRET", "PASSWORD", "KEY"))}
        self.lock = threading.Lock()
        self.report = {"problem": f"M2 P{problem}", "started_at": utc(), "mode": "guided",
                       "automated_checks": {}, "manual_evidence_required": list(manual_evidence)}

    # --- evidence -------------------------------------------------------
    def clean(self, data):
        if isinstance(data, str):
            for value in self.secrets:
                data = data.replace(value, "[REDACTED]")
            return data
        if isinstance(data, dict):
            return {k: self.clean(v) for k, v in data.items()}
        if isinstance(data, list):
            return [self.clean(v) for v in data]
        return data

    def save(self, name, data):
        (self.folder / name).write_text(json.dumps(self.clean(data), indent=2, ensure_ascii=False, allow_nan=False) + "\n")

    def stage(self, name, operation, *args, **kwargs):
        output("RUN", name)
        try:
            result = operation(*args, **kwargs)
            if isinstance(result, dict) and result.get("skipped"):
                # A check that could not be proven is recorded as skipped, never as a pass.
                self.report["automated_checks"][name] = {"status": "skipped", "reason": result.get("reason", ""),
                                                         "result": result}
                output("SKIP", name, *(f"{k}: {v}" for k, v in result.items()
                                       if k != "skipped" and not isinstance(v, (dict, list))))
                return result
            self.report["automated_checks"][name] = {"status": "pass", "result": result}
            output("PASS", name, *(f"{k}: {v}" for k, v in (result or {}).items() if not isinstance(v, (dict, list))))
            return result
        except (Exception, KeyboardInterrupt) as error:
            message = str(error) or type(error).__name__
            status = "interrupted" if isinstance(error, KeyboardInterrupt) else "fail"
            self.report["automated_checks"][name] = {"status": status, "error": self.clean(message)}
            raise
        finally:
            self.save("result.json", self.report)

    def skip(self, name, reason):
        self.report["automated_checks"][name] = {"status": "skipped", "reason": reason}
        output("SKIP", name, reason)

    # --- HTTP -----------------------------------------------------------
    def url(self, service):
        return self.forwards.urls.get(service) or self.forwards.open(service)

    def call(self, service, path, token=None, body=None, expected=(200,), timeout=None, record=True):
        """Returns (status, payload, elapsed_ms); network errors raise RuntimeError."""
        headers = {"X-Correlation-ID": f"m2-p{self.problem}-{self.folder.name}"}
        if token:
            headers["Authorization"] = "Bearer " + token
        data = None if body is None else json.dumps(body).encode()
        if data is not None:
            headers["Content-Type"] = "application/json"
        # A GET that is shed with an unexpected 429 waits for Retry-After and is retried
        # once: the lab Aggregator has a single read slot per pod, so a probe can collide
        # with another reader (e.g. the dashboard updater). Calls that expect 429 (the
        # overload test) are never retried. Both attempts are kept in http.jsonl.
        # A GET through a kubectl port-forward whose pod went away (the tunnel binds one
        # pod) reopens the tunnel through the Service and is sent once more, as in P1.
        # Writes are never resent. The reconnect is kept in http.jsonl and result.json.
        reconnected = False
        for attempt in (1, 2):
            request = urllib.request.Request(self.url(service) + path, data=data, headers=headers)
            started = time.perf_counter()
            retry_after = None
            try:
                with urllib.request.urlopen(request, timeout=timeout or self.config.request_timeout) as response:
                    status, raw = response.status, response.read()
            except urllib.error.HTTPError as error:
                status, raw, retry_after = error.code, error.read(), error.headers.get("Retry-After")
            except (urllib.error.URLError, TimeoutError, ConnectionError) as error:
                failure = f"{service} {path}: network failure ({type(error).__name__})"
                tunnelled = service in self.forwards.processes
                if attempt == 1 and data is None and tunnelled and not reconnected:
                    reconnected = True
                    with self.lock:
                        self.report.setdefault("tunnel_reconnects", []).append(
                            {"at": utc(), "service": service, "path": path, "error": type(error).__name__})
                        if record:
                            with (self.folder / "http.jsonl").open("a") as stream:
                                stream.write(json.dumps({"at": utc(), "service": service, "path": path, "status": 0,
                                                         "error": type(error).__name__, "tunnel_reopened": True}) + "\n")
                    self.forwards.open(service)
                    continue
                raise RuntimeError(failure) from None
            elapsed = (time.perf_counter() - started) * 1000
            if record:
                entry = {"at": utc(), "service": service, "path": path, "status": status, "ms": round(elapsed, 2)}
                if attempt == 2:
                    entry["retry_after_tunnel_reopen" if reconnected else "retry_after_429"] = True
                with self.lock, (self.folder / "http.jsonl").open("a") as stream:
                    stream.write(json.dumps(entry) + "\n")
            if attempt == 1 and status == 429 and 429 not in expected and data is None:
                with self.lock:
                    self.report["retried_429"] = self.report.get("retried_429", 0) + 1
                try:
                    wait = min(5.0, max(0.0, float(retry_after or 1)))
                except ValueError:
                    wait = 1.0
                time.sleep(wait)
                continue
            break
        require(status in expected, f"{service} {path}: HTTP {status}, expected {expected}")
        try:
            payload = json.loads(raw) if raw else None
        except ValueError:
            payload = None
        return status, payload, elapsed

    def aggregator(self, path):
        return self.call("aggregator", path, token=self.env["AGGREGATOR_TOKEN"])[1]

    def hazards(self, source):
        """Every stored hazard of one source, following next_after pages."""
        items, after = [], ""
        while True:
            page = self.aggregator(f"/internal/hazards?source={source}&limit=1000" + (f"&after={after}" if after else ""))
            items.extend(page.get("items") or [])
            after = page.get("next_after") or ""
            if not after:
                return items

    def login(self, identity):
        secret = self.env.get(identity.upper() + "_CLIENT_SECRET")
        require(secret, f"{identity.upper()}_CLIENT_SECRET missing from {self.config.env_file}")
        _, pair, _ = self.call("auth", "/auth/token", body={"client_id": identity, "client_secret": secret})
        self.secrets.update({pair["access_token"], pair.get("refresh_token") or ""})
        return pair["access_token"]

    def wait(self, description, check, timeout):
        """Poll check() until it returns something truthy; transient errors are retried."""
        deadline, last = time.monotonic() + timeout, None
        while time.monotonic() < deadline:
            try:
                result = check()
            except (RuntimeError, ValueError) as error:
                result, last = None, str(error)
            if result:
                return result
            time.sleep(1)
        raise ValueError("timed out waiting for " + description + (f" (last error: {self.clean(last)})" if last else ""))

    # --- lifecycle ------------------------------------------------------
    def verify_environment(self, required_keys):
        require(self.config.kubeconfig.is_file(), f"kubeconfig missing: {self.config.kubeconfig} (run task up first)")
        for key in required_keys:
            require(self.env.get(key), f"{key} missing from {self.config.env_file}")
        nodes = self.cluster.nodes()
        mode_file = INFRA / ".state/mode"
        mode = mode_file.read_text().strip() if mode_file.is_file() else "vm"
        self.report["lab_mode"] = mode
        self.report["nodes"] = [n["metadata"]["name"] for n in nodes]
        if mode == "k3d":
            output("INFO", "k3d lab: development evidence only (nodes share one kernel)")
        output("INFO", f"Cluster: {len(nodes)} nodes ({mode})", f"Evidence: {self.folder}")

    def finish(self, exit_code):
        self.forwards.close()
        self.report["finished_at"] = utc()
        incomplete = any(check["status"] == "skipped" for check in self.report["automated_checks"].values())
        self.report["status"] = (("automated_checks_incomplete" if incomplete else
                                  "automated_checks_passed_manual_evidence_pending")
                                 if exit_code == 0 else self.report.get("status", "failed"))
        self.save("result.json", self.report)
        output("RESULT", "Stage summary", *(f"{check['status'].upper()}: {name}"
                                            for name, check in self.report["automated_checks"].items()))
        if exit_code == 0 and not incomplete:
            output("PASS", f"All automated P{self.problem} checks passed")
        elif exit_code == 0:
            output("INFO", f"P{self.problem} incomplete: one or more automated checks were skipped")
        if getattr(self, "save_evidence", True):
            output("INFO", "Evidence", str(self.folder))
        else:
            output("INFO", "Evidence not saved", "this run's files are deleted when the test exits")
        output("MANUAL", "Pending evidence", *self.report["manual_evidence_required"])


class Tee:
    """Writes to the terminal and to console.log."""

    def __init__(self, terminal, log, lock):
        self.terminal, self.log, self.lock = terminal, log, lock
        self.original_input = builtins.input

    def __getattr__(self, name):
        return getattr(self.terminal, name)

    def write(self, text):
        with self.lock:
            self.log.write(text)
            self.log.flush()
            return self.terminal.write(text)

    def flush(self):
        with self.lock:
            self.log.flush()
            self.terminal.flush()

    def read_input(self, prompt=""):
        # input() on a TTY bypasses sys.stdout, so write the prompt through the tee.
        self.write(str(prompt))
        self.flush()
        try:
            answer = self.original_input()
        finally:
            with self.lock:
                self.log.write("\n")
                self.log.flush()
        return answer


def ask_save_evidence(problem, config):
    """Asks whether to keep this run's evidence; Enter keeps it. A fixed
    CONFIG.output_dir is always kept."""
    if config.output_dir:
        return True
    answer = input(f"Save evidence for this P{problem} run? [Y/n]: ").strip().lower()
    return answer not in ("n", "no")


def evidence_folder(problem, config, save=True):
    """Fresh tests/m2/p<n>/evidence/m2-p<n>-<stamp>-<id>/, offering to delete older runs first.
    When the evidence is not kept, the run writes to a temporary folder instead, which
    main() removes at the end."""
    if not save:
        return Path(tempfile.mkdtemp(prefix=f"m2-p{problem}-unsaved-")).resolve()
    root = Path(__file__).resolve().parent / f"p{problem}" / "evidence"
    require(not root.is_symlink(), "evidence directory must not be a symlink")
    older = sorted(f for f in root.glob(f"m2-p{problem}-*") if f.is_dir() and not f.is_symlink())
    if older:
        for f in older:
            print(f"  {f.name}", flush=True)
        answer = input(f"Delete {len(older)} older P{problem} evidence folders in {root}? [y/N]: ").strip().lower()
        if answer in ("y", "yes"):
            for f in older:
                shutil.rmtree(f)
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S")
    folder = (config.output_dir or root / f"m2-p{problem}-{stamp}-{uuid.uuid4().hex[:8]}").resolve()
    folder.mkdir(parents=True, exist_ok=False)
    return folder


def main(problem, config, groups, required_keys, manual_evidence, validate=None):
    """Shared entry point: one Run, the groups in order, Enter between steps."""
    if len(sys.argv) > 1:
        print(f"This test has no command-line options. Edit CONFIG in test-problem-{problem}.py.", file=sys.stderr)
        return 2
    if not sys.stdin.isatty():
        print(f"run task m2:p{problem} in an interactive terminal", file=sys.stderr)
        return 2
    save = ask_save_evidence(problem, config)
    folder = evidence_folder(problem, config, save)
    try:
        with ExitStack() as console:
            return _console_run(problem, config, groups, required_keys, manual_evidence, validate, folder, save,
                                console)
    finally:
        if not save:
            shutil.rmtree(folder, ignore_errors=True)
            print(f"Evidence not saved (temporary folder {folder} removed).", flush=True)


def _console_run(problem, config, groups, required_keys, manual_evidence, validate, folder, save, console):
    # Everything shown in the terminal (prompts included) also goes to console.log.
    log = console.enter_context((folder / "console.log").open("a", buffering=1))
    lock = threading.RLock()
    stdout = Tee(sys.stdout, log, lock)
    console.enter_context(redirect_stdout(stdout))
    console.enter_context(redirect_stderr(Tee(sys.stderr, log, lock)))
    console.callback(setattr, builtins, "input", builtins.input)
    builtins.input = stdout.read_input
    return _run(problem, config, groups, required_keys, manual_evidence, validate, folder, save)


def _run(problem, config, groups, required_keys, manual_evidence, validate, folder, save=True):
    run = Run(problem, config, manual_evidence, folder)
    run.save_evidence = save
    exit_code = 1
    try:
        if validate:
            validate(config)
        run.verify_environment(required_keys)
        print(f"\nM2 P{problem}: press Enter to continue between test steps.", flush=True)
        for group in groups:
            group.run_group(run)
        exit_code = 0
    except (Exception, KeyboardInterrupt) as error:
        run.report["status"] = "interrupted" if isinstance(error, KeyboardInterrupt) else "failed"
        message = str(error) if isinstance(error, (ValueError, RuntimeError)) else type(error).__name__ + ": " + str(error)
        run.report["error"] = run.clean(message)
        output("FAIL", f"P{problem} check failed", run.clean(message))
    finally:
        run.finish(exit_code)
    return exit_code
