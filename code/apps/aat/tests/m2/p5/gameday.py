"""P5 criterion 6 rehearsal: unannounced combinations of failures, then hands off.

A scenario injects several faults on a schedule (the role the Tester Agent plays in
the exam), ends them all at the end of its fault window, and from then on the test
only observes: no restart, scale, or other action, so the system has to recover on
its own. Throughout, a background probe sends Field Team requests and records every
answer; the verdict covers what clients saw during the faults and how long the system
took to be fully healthy again afterwards.

Faults are generic (power, network, storage, load, upstream outage); none depends on
IDs, seeds, or the order of the practice profile. The exam profile itself cannot be
replayed here (its key is shared only for the exam), so these are rehearsals of the
same kinds of failure, not the exam scenario.
"""
from dataclasses import dataclass, field
import json
import math
from pathlib import Path
import subprocess
import threading
import time
import urllib.parse

from common import INFRA, NAMESPACE, output, p95, require, utc
from helper import nats_request, sql

LABEL = "aat-gameday"
WORKERS = ("aat-worker-1", "aat-worker-2")  # never the control plane
# JetStream durable name, not the FIELD_NOTIFIER_CRITICAL receipt-bucket prefix.
CRITICAL_CONSUMERS = ("field-notifier-critical",)


# ---------------------------------------------------------------- faults

class Fault:
    """One failure. inject() starts it; revert() ends it and is safe to repeat."""
    name = "fault"
    storage = False  # touches data placement: M1 latency targets are not asserted

    def inject(self, run):
        raise NotImplementedError

    def revert(self, run):
        pass

    def describe(self):
        return self.name


class PvmbgOutage(Fault):
    name = "pvmbg-outage"

    def inject(self, run):
        run.call("pvmbg", "/admin/outage", token=run.env["PVMBG_TOKEN"], body={"enabled": True})

    def revert(self, run):
        run.call("pvmbg", "/admin/outage", token=run.env["PVMBG_TOKEN"], body={"enabled": False})




class NetworkDrop(Fault):
    """Packets to (ingress) or from (egress) the selected pods are dropped, so
    connections hang until the caller's timeout instead of failing fast."""

    def __init__(self, name, selector, direction="ingress"):
        require(direction in ("ingress", "egress"), "direction is ingress or egress")
        self.name, self.selector, self.direction = name, dict(selector), direction

    def policy(self):
        spec = {"podSelector": {"matchLabels": self.selector}}
        if self.direction == "ingress":
            spec.update(policyTypes=["Ingress"], ingress=[])
        else:
            # Keep DNS so the failure is the partition itself, not name resolution.
            spec.update(policyTypes=["Egress"], egress=[{"to": [{"namespaceSelector": {}}],
                                                         "ports": [{"protocol": "UDP", "port": 53},
                                                                   {"protocol": "TCP", "port": 53}]}])
        return {"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
                "metadata": {"name": "gameday-" + self.name, "labels": {LABEL: "true"}}, "spec": spec}

    def inject(self, run):
        apply(run, self.policy())

    def revert(self, run):
        run.cluster.kubectl("delete", "networkpolicy", "gameday-" + self.name, "--ignore-not-found")


class WorkerDown(Fault):
    """A worker node loses power (k3d: container stopped; VM: domain powered off)."""
    storage = True

    def __init__(self, worker):
        require(worker in WORKERS, f"only {WORKERS} may be stopped, never the control plane")
        self.worker, self.name = worker, "node-down-" + worker

    def inject(self, run):
        node_action(run, "stop-worker", self.worker)

    def revert(self, run):
        node_action(run, "start-worker", self.worker)



class MediaFlood(Fault):
    """Media clients far above capacity, until the fault ends."""
    name = "media-flood"

    def __init__(self, workers=48):
        self.workers, self.stop, self.threads, self.codes = workers, threading.Event(), [], []

    def inject(self, run):
        token = run.login("public")
        self.stop.clear()

        def flood():
            while not self.stop.is_set():
                try:
                    status = run.call("client", "/hazards?limit=1000", token=token, expected=(200, 429, 503),
                                      timeout=30, record=False)[0]
                except (RuntimeError, ValueError):
                    status = 0
                self.codes.append(status)
        self.threads = [threading.Thread(target=flood, daemon=True) for _ in range(self.workers)]
        for t in self.threads:
            t.start()

    def revert(self, run):
        self.stop.set()
        for t in self.threads:
            t.join(timeout=35)
        self.threads = []


def apply(run, manifest):
    command = ["kubectl", "--kubeconfig", str(run.config.kubeconfig), "-n", NAMESPACE, "apply", "-f", "-"]
    result = subprocess.run(command, input=json.dumps(manifest), text=True, capture_output=True, timeout=60)
    require(result.returncode == 0, "kubectl apply failed: " + run.clean(result.stderr[-300:]))


def node_action(run, action, worker):
    require(worker in WORKERS, "refusing to act on " + worker)
    result = subprocess.run(["bash", str(INFRA / "scripts/vm/main.sh"), action, worker], cwd=INFRA,
                            text=True, capture_output=True, timeout=300)
    require(result.returncode == 0, f"{action} {worker} failed: " + run.clean((result.stderr or result.stdout)[-300:]))


# ---------------------------------------------------------------- scenarios

@dataclass
class Step:
    at: float  # seconds after the fault window opens
    fault: Fault


@dataclass
class Scenario:
    name: str
    story: str
    window: float  # seconds the faults last; all end together at the end
    steps: list = field(default_factory=list)

    @property
    def storage(self):
        return any(s.fault.storage for s in self.steps)

    def validate(self):
        require(self.steps, f"{self.name}: no faults")
        require(all(0 <= s.at < self.window for s in self.steps), f"{self.name}: a fault starts outside its window")
        names = [s.fault.name for s in self.steps]
        require(len(names) == len(set(names)), f"{self.name}: a fault is listed twice")


def scenario(config):
    """One combined scenario, short enough for the regular P5 run: load, an upstream
    outage, a hung sensor, the notifier cut off from NATS, and a node losing power
    (which also leaves part of the data unavailable until its replicas take over)."""
    steps = [Step(0, MediaFlood(config.media_workers)), Step(5, PvmbgOutage()),
             Step(10, NetworkDrop("sensor-unreachable", {"role": "sensor"})),
             Step(15, NetworkDrop("notifier-partition", {"role": "consumer"}, "egress"))]
    if config.gameday_node_loss:
        steps.append(Step(20, WorkerDown(config.worker)))
    return Scenario("combined", "Media spike, PVMBG outage, Sensor Pantai unreachable (calls hang), "
                    "notifier cut off from NATS" + (", a worker node loses power" if config.gameday_node_loss else ""),
                    config.fault_window, steps)


# ---------------------------------------------------------------- probe

class Probe:
    """Field Team requests every interval; each answer is tagged with the phase."""

    def __init__(self, run, interval, timeout):
        self.run, self.interval, self.timeout = run, interval, timeout
        self.samples, self.phase, self.stop = [], "baseline", threading.Event()
        self.lock = threading.Lock()
        self.thread = None

    def request(self, token, path):
        started = time.perf_counter()
        try:
            status, body, ms = self.run.call("client", path, token=token, expected=(200, 401, 429, 500, 502, 503, 504),
                                             timeout=self.timeout, record=False)
        except (RuntimeError, ValueError):  # no answer at all: refused, reset, or hung past the timeout
            return {"status": 0, "ms": round((time.perf_counter() - started) * 1000, 1)}
        sample = {"status": status, "ms": round(ms, 1)}
        if isinstance(body, dict):
            sample["partial"] = bool(body.get("partial"))
            sample["stale"] = sorted(s["source"] for s in body.get("sources") or [] if s.get("stale"))
            sample["explicit_error"] = status >= 400 and isinstance(body.get("error"), dict)
        return sample

    def loop(self):
        token = self.run.login("responder")
        while not self.stop.is_set():
            for kind, path in (("field", "/hazards?limit=5"), ("bmkg_only", "/hazards?source=BMKG&limit=5")):
                sample = self.request(token, path)
                if sample["status"] == 401:  # access token expired: log in again, not a failure
                    try:
                        token = self.run.login("responder")
                    except (RuntimeError, ValueError):
                        pass
                    continue
                with self.lock:
                    self.samples.append({"t": time.monotonic(), "at": utc(), "phase": self.phase, "kind": kind, **sample})
            self.stop.wait(self.interval)

    def start(self):
        self.thread = threading.Thread(target=self.loop, daemon=True)
        self.thread.start()

    def set_phase(self, phase):
        with self.lock:
            self.phase = phase

    def finish(self):
        self.stop.set()
        if self.thread:
            self.thread.join(timeout=self.timeout + 5)
        with self.lock:
            return list(self.samples)


# ---------------------------------------------------------------- verdicts

def answered(sample):
    """A client got an answer: data (possibly partial or stale), shed with 429, or an
    explicit unavailability error. Not answered: no response or a bare 5xx."""
    s = sample["status"]
    return s == 200 or s == 429 or (s == 503 and sample.get("explicit_error", False))


def slo(samples, target):
    """Field Team availability: share of requests answered with data (HTTP 200).
    Error budget used = observed failure rate / allowed failure rate."""
    field_samples = [s for s in samples if s["kind"] == "field"]
    if not field_samples:
        return {"requests": 0, "good": 0, "availability": None, "error_budget_used": None}
    good = sum(1 for s in field_samples if s["status"] == 200)
    availability = good / len(field_samples)
    allowed = 1 - target
    used = (1 - availability) / allowed if allowed > 0 else math.inf
    return {"requests": len(field_samples), "good": good, "availability": round(availability, 4),
            "target": target, "error_budget_used": round(used, 3)}


def summarize(samples, phase):
    chosen = [s for s in samples if s["phase"] == phase]
    field_samples = [s for s in chosen if s["kind"] == "field"]
    bmkg = [s["ms"] for s in chosen if s["kind"] == "bmkg_only" and s["status"] == 200]
    unanswered = [s for s in chosen if not answered(s)]
    return {"requests": len(chosen), "unanswered": len(unanswered),
            "unanswered_statuses": sorted({s["status"] for s in unanswered}),
            "field_p95_ms": round(p95([s["ms"] for s in field_samples]), 1) if field_samples else None,
            "bmkg_only_p95_ms": round(p95(bmkg), 1) if bmkg else None,
            "partial_answers": sum(1 for s in chosen if s.get("partial")),
            "stale_sources_seen": sorted({src for s in chosen for src in s.get("stale", [])})}


def verdict(scenario, samples, recovered_after, config):
    """Pass/fail per scenario from the probe samples and the recovery time."""
    during = summarize(samples, "faults")
    after = summarize(samples, "recovery")
    budget = slo([s for s in samples if s["phase"] in ("faults", "recovery")], config.field_availability_slo)
    failures = []
    if during["unanswered"] or after["unanswered"]:
        failures.append(f"{during['unanswered'] + after['unanswered']} Field Team requests got no answer "
                        f"(statuses {sorted(set(during['unanswered_statuses'] + after['unanswered_statuses']))})")
    if recovered_after is None:
        failures.append(f"not fully healthy within {config.recovery_timeout:.0f}s after the faults ended")
    if budget["error_budget_used"] is not None and budget["error_budget_used"] > 1:
        failures.append(f"Field Team availability {budget['availability']:.2%} is below the "
                        f"{config.field_availability_slo:.2%} target (budget used {budget['error_budget_used']:.0%})")
    if (not scenario.storage and during["bmkg_only_p95_ms"] is not None
            and during["bmkg_only_p95_ms"] >= config.other_source_p95_ms):
        failures.append(f"BMKG-only p95 {during['bmkg_only_p95_ms']} ms during the faults "
                        f"(M1 target < {config.other_source_p95_ms:.0f} ms)")
    return {"scenario": scenario.name, "story": scenario.story, "storage_faults": scenario.storage,
            "during_faults": during, "after_faults": after, "slo": budget,
            "recovered_after_s": recovered_after, "passed": not failures, "failures": failures}


# ---------------------------------------------------------------- health

def health(run, config):
    """Every condition that "recovered" means. Returns (healthy, details)."""
    checks = {}
    nodes = run.cluster.kubectl("get", "nodes", "-o", "json", json_output=True)["items"]
    checks["nodes_ready"] = all(any(c["type"] == "Ready" and c["status"] == "True" for c in n["status"]["conditions"])
                                for n in nodes)
    for name in config.deployments:
        dep = run.cluster.kubectl("get", "deployment", name, "-o", "json", json_output=True)
        want = dep["spec"].get("replicas", 1)
        checks[f"{name}_ready"] = dep.get("status", {}).get("readyReplicas", 0) >= want
    sensor = run.aggregator("/internal/sensor/status")
    checks["sensor_available"] = bool(sensor.get("available")) and sensor["sensor"].get("breaker") == "closed"
    token = run.login("responder")
    _, page, _ = run.call("client", "/hazards?limit=5", token=token)
    stale = sorted(s["source"] for s in page.get("sources") or []
                   if s.get("stale") and s["source"] in config.fresh_sources)
    checks["no_stale_sources"] = not stale
    checks["complete_answers"] = not page.get("partial")
    pending = sql(run, "SELECT to_json(count(*)) FROM hazard_outboxes "
                       "WHERE published_at IS NULL AND lane IN ('critical','critical_only');")
    checks["critical_outbox_drained"] = pending == [0]
    address = urllib.parse.urlsplit(run.url("nats"))
    consumers = {}
    for consumer in CRITICAL_CONSUMERS:
        info = nats_request((address.hostname, address.port),
                            f"$JS.API.CONSUMER.INFO.HAZARDS_STREAM.{consumer}", {})
        consumers[consumer] = {key: info.get(key) for key in ("num_pending", "num_ack_pending", "error")}
        checks[f"{consumer.replace('-', '_')}_drained"] = ("error" not in info and info.get("num_pending", 1) == 0
                                                        and info.get("num_ack_pending", 1) == 0)
    return all(checks.values()), {"checks": checks, "stale_sources": stale, "critical_consumers": consumers}


def wait_healthy(run, config, timeout, label):
    """Seconds until health() first passes, or None. Read-only: no corrective action."""
    started, last = time.monotonic(), None
    while time.monotonic() - started < timeout:
        try:
            ok, last = health(run, config)
        except (RuntimeError, ValueError, KeyError, TypeError, OSError) as error:
            ok, last = False, {"error": run.clean(str(error))}
            # A tunnel bound to a pod on a lost node stays dead; reopen it through the Service.
            for service in ("aggregator", "nats"):
                run.forwards.close(service)
                run.forwards.urls.pop(service, None)
        if ok:
            return round(time.monotonic() - started, 1), last
        detail = ", ".join(k for k, v in (last.get("checks") or {}).items() if not v) or last.get("error", "")
        if last.get("critical_consumers"):
            detail += "; critical consumers=" + json.dumps(last["critical_consumers"], sort_keys=True)
        output("WAIT", label, detail)
        time.sleep(config.health_interval)
    return None, last


# ---------------------------------------------------------------- runner

def inject_all(run, scenario, probe, started, log):
    """Start each fault at its time. Stops early (without raising) if one fails, so the
    caller still reverts whatever was started."""
    active = []
    for step in sorted(scenario.steps, key=lambda s: s.at):
        delay = started + step.at - time.monotonic()
        if delay > 0:
            time.sleep(delay)
        try:
            step.fault.inject(run)
        except (RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
            log.append({"at": utc(), "fault": step.fault.name, "action": "inject", "error": run.clean(str(error))})
            return active, f"{step.fault.name}: {error}"
        active.append(step.fault)
        log.append({"at": utc(), "fault": step.fault.name, "action": "inject", "after_s": round(time.monotonic() - started, 1)})
        output("ACTION", scenario.name, f"+{time.monotonic() - started:.0f}s {step.fault.describe()}")
    return active, None


def revert_all(run, faults, log):
    """End every started fault, newest first; collect errors instead of stopping."""
    errors = []
    for fault in reversed(faults):
        try:
            fault.revert(run)
            log.append({"at": utc(), "fault": fault.name, "action": "revert"})
        except (RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
            errors.append(f"{fault.name}: {error}")
            log.append({"at": utc(), "fault": fault.name, "action": "revert", "error": run.clean(str(error))})
    return errors


def play(run, scenario, config):
    scenario.validate()
    log = []
    output("INFO", scenario.name, scenario.story)
    baseline, details = wait_healthy(run, config, config.recovery_timeout, f"{scenario.name}: waiting for a healthy start")
    require(baseline is not None, f"{scenario.name}: system not healthy before the scenario: {details}")
    probe = Probe(run, config.probe_interval, config.probe_timeout)
    probe.start()
    time.sleep(config.baseline_seconds)
    probe.set_phase("faults")
    started = time.monotonic()
    active, inject_error = [], None
    try:
        active, inject_error = inject_all(run, scenario, probe, started, log)
        remaining = started + scenario.window - time.monotonic()
        if remaining > 0 and inject_error is None:
            time.sleep(remaining)
    finally:
        # The faults end; from here on the test does nothing but observe.
        revert_errors = revert_all(run, active, log)
        probe.set_phase("recovery")
        log.append({"at": utc(), "action": "hands-off", "note": "no further actions until the verdict"})
    recovered_after, last = wait_healthy(run, config, config.recovery_timeout, f"{scenario.name}: recovering on its own")
    time.sleep(config.after_recovery_seconds)
    samples = probe.finish()
    result = verdict(scenario, samples, recovered_after, config)
    flood = next((s.fault for s in scenario.steps if isinstance(s.fault, MediaFlood)), None)
    if flood is not None:
        result["media"] = {"requests": len(flood.codes), "shed_429": flood.codes.count(429)}
    result["faults"] = log
    result["last_health"] = last
    if inject_error:
        result["passed"] = False
        result["failures"].append("fault injection failed: " + inject_error)
    if revert_errors:
        result["passed"] = False
        result["failures"].append("fault did not end cleanly: " + "; ".join(revert_errors))
    run.save(f"gameday-{scenario.name}.json", result)
    run.save(f"gameday-{scenario.name}-probe.jsonl.json", samples)
    return result


def restore(run, config):
    """Undo every game-day fault that can outlive a crashed run. Idempotent."""
    errors = []
    for action in (lambda: run.cluster.kubectl("delete", "networkpolicy", "-l", f"{LABEL}=true", "--ignore-not-found"),
                   lambda: PvmbgOutage().revert(run),
                   lambda: WorkerDown(config.worker).revert(run)):
        try:
            action()
        except (RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
            errors.append(run.clean(str(error)))
    return errors


def restore_argo(run, baseline_file):
    """Re-enable Argo reconciliation from a crashed run's setup-baseline.json."""
    from helper import argo
    baseline = json.loads(Path(baseline_file).read_text())
    for app in reversed(baseline.get("applications", [])):
        argo(run, "patch", "application", app["name"], "--type=merge", "-p", json.dumps({
            "metadata": {"annotations": {"argocd.argoproj.io/skip-reconcile": app["skip"]}},
            "spec": {"syncPolicy": {"automated": app["automated"]}}}))
