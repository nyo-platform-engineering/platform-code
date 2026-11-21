"""Evidence from the running deployment. No replacement services or data resets."""
import json
import math
from pathlib import Path
import time
from types import SimpleNamespace
import urllib.request

from common import output, require, parse_time
from run_progress import Progress
import consumer_stats


def preflight(run):
    def inspect():
        deployment = run.cluster.kubectl("get", "deployment", "aggregator", "-o", "json", json_output=True)
        config = run.cluster.kubectl("get", "configmap", "aggregator", "-o", "json", json_output=True)["data"]
        run.satellite_feed_url = config.get("SATELLITE_FEED_URL", "https://satellite.naufarrel.dev").rstrip("/")
        container = next(c for c in deployment["spec"]["template"]["spec"]["containers"] if c["name"] == "aggregator")
        for entry in container.get("env", []):
            if entry["name"] == "SATELLITE_FEED_URL" and "value" in entry:
                run.satellite_feed_url = entry["value"].rstrip("/")
        evidence = {"origin": "running_cluster", "kubeconfig": str(run.config.kubeconfig),
                    "deployment": deployment["metadata"]["name"], "image": container["image"],
                    "feed_url": run.satellite_feed_url, "ready_replicas": deployment.get("status", {}).get("readyReplicas", 0),
                    "allowed_lateness_minutes": config.get("SATELLITE_ALLOWED_LATENESS_MINUTES"),
                    "official_reference_verified": False}
        require(evidence["ready_replicas"] > 0, "no ready Aggregator replicas")
        code, capability, _ = run.call("aggregator", "/internal/satellite/capabilities", expected=(200, 404))
        require(code == 200 and capability.get("controlled_verification_version") == 1,
                "deploy this PR's Aggregator image before P4; no deployment changes were made")
        require(not capability.get("verification_schema"), "restore the active controlled P4 run first")
        run.save("deployment.json", evidence)
        return evidence
    run.stage("running-deployment", inspect)
    run.report["evidence_origin"] = "running_cluster"
    run.report["full_p4_certified"] = False


def feed_session(run):
    # Only /session: never open a second SSE connection with the group's token.
    # Use the deployed Pod's network and its existing Secret/env. The host may
    # have different upstream routing, and tokens never enter kubectl arguments.
    script = ('exec wget -qO- --timeout=15 '
              '--header="Authorization: Bearer ${SATELLITE_GROUP_TOKEN}" '
              '"${SATELLITE_FEED_URL:-https://satellite.naufarrel.dev}/v1/session"')
    session = run.cluster.kubectl("exec", "deployment/aggregator", "-c", "aggregator", "--", "sh", "-c", script,
                                  json_output=True, timeout=run.config.request_timeout + 10)
    require(type(session.get("latest_offset")) is int, "feed session has no valid latest_offset")
    speed = float(session.get("speed", 0))
    require(math.isfinite(speed) and speed > 0, "feed session has no valid speed")
    return {"latest_offset": session["latest_offset"], "speed": speed, "profile": session.get("profile")}


class BurstTracker:
    """Conservative recovery bound: burst ends between the last 60x and first 1x sample."""
    def __init__(self):
        self.started = False
        self.last_fast = self.end_observed = self.recovery_upper = None
        self.baseline_observed = False
        self.whole_burst_observed = False
        self.previous_latest = None

    def observe(self, now, speed, latest, offset):
        require(self.previous_latest is None or latest >= self.previous_latest,
                "feed session offset moved backwards/reset during measurement")
        self.previous_latest = latest
        require(not (self.end_observed is not None and speed == 60),
                "a second burst began before the first recovery was verified")
        if speed == 60:
            if not self.started:
                self.whole_burst_observed = self.baseline_observed
            self.started = True
            self.last_fast = now
        elif not self.started:
            self.baseline_observed = speed == 1
        elif speed == 1 and self.end_observed is None:
            self.end_observed = now
        if self.end_observed is not None and offset is not None and offset >= latest:
            self.recovery_upper = now - self.last_fast
        return self.recovery_upper


def real_burst(run):
    tracker = BurstTracker()
    started, wall_started = time.monotonic(), time.time()
    samples = []
    with Progress(run.folder, "real-60x-burst", clean=run.clean,
                  writer=lambda message: output("WAIT", message)) as progress:
        with progress.phase("observe the deployed consumer and actual feed speed", run.config.burst_observation_timeout):
            while time.monotonic() - started < run.config.burst_observation_timeout:
                session = feed_session(run)
                stats = run.aggregator("/internal/satellite/stats")["feed"]
                require(stats.get("rejected_events") == 0, "deployed consumer rejected feed events during burst observation")
                elapsed = time.monotonic() - started
                offset = stats.get("offset")
                recovery = tracker.observe(elapsed, session["speed"], session["latest_offset"], offset)
                lag = None if offset is None else max(session["latest_offset"] - offset, 0)
                sample = {"elapsed_seconds": round(elapsed, 3), **session, "offset": offset, "lag_events": lag}
                samples.append(sample)
                run.save("real-burst-progress.json", samples)
                progress.detail(f"speed={session['speed']:g}x; offset={offset}; latest={session['latest_offset']}; lag={lag}")
                if recovery is not None:
                    result = {"origin": "running_cluster", "recovery_upper_bound_seconds": round(recovery, 3),
                              "limit_seconds": 60, "whole_burst_observed": tracker.whole_burst_observed,
                              "burst_end_observed_seconds": tracker.end_observed,
                              "last_fast_sample_seconds": tracker.last_fast,
                              "offset": offset, "latest_offset": session["latest_offset"],
                              "no_loss_verified_by_offset_alone": False,
                              "window_start_unix": wall_started, "window_end_unix": time.time()}
                    run.save("real-burst.json", result)
                    require(recovery <= 60, f"actual burst recovery upper bound {recovery:.1f}s exceeds 60s")
                    run.burst_window = result
                    return result
                if tracker.end_observed is not None and elapsed - tracker.last_fast > 60:
                    raise ValueError(f"deployed consumer is still {lag} events behind more than 60s after burst end")
                if not tracker.started and elapsed >= run.config.burst_start_timeout:
                    return {"skipped": True, "reason": "no actual 60x burst observed; no simulation substituted"}
                time.sleep(run.config.burst_sample_seconds)
    return {"skipped": True, "reason": "burst end/recovery not observed within the configured observation budget"}


def burst_latency(run):
    if not getattr(run, "burst_window", None):
        return {"skipped": True, "reason": "no actual burst/recovery window recorded"}
    batches, _, source = consumer_stats.collect(run)
    window = run.burst_window
    selected = [b for b in batches if window["window_start_unix"] <= parse_time(b["time"]).timestamp() <= window["window_end_unix"]]
    if not selected or any(not isinstance(b.get("ingest_latency_samples_ms"), list) for b in selected):
        return {"skipped": True, "reason": "deployed logs lack per-event latency samples for this burst; rebuild/deploy Aggregator"}
    samples = [n for b in selected for n in b["ingest_latency_samples_ms"]]
    require(samples and all(isinstance(n, (int, float)) and math.isfinite(n) and n >= 0 for n in samples),
            "missing/invalid per-event latency measurements")
    ordered = sorted(samples)
    result = {"origin": "running_cluster", "source": source, "sample_count": len(samples),
              "p95_ingest_to_commit_ms": ordered[math.ceil(0.95 * len(ordered)) - 1],
              "definition": "individual non-heartbeat input receipt to durable result commit; batches committed in observed burst/recovery window",
              "selection": "commit wall time; includes resulting HazardEvents, excludes upstream waiting before SSE receipt"}
    run.save("real-burst-latencies.json", samples)
    run.save("real-burst-latency-summary.json", result)
    return result


def dashboard(run):
    if not run.env.get("ANALYST_CLIENT_SECRET"):
        return {"skipped": True, "reason": "ANALYST_CLIENT_SECRET required to verify deployed Client dashboard"}
    token = run.login("analyst")
    status, body, _ = run.call("client", "/satellite/stats", token=token, expected=(200, 404))
    if status == 404:
        return {"skipped": True, "reason": "deployed Client has no satellite panel endpoint yet; rebuild/deploy Client"}
    require(isinstance(body, dict) and isinstance(body.get("feed"), dict), "deployed Client returned invalid satellite status")
    with urllib.request.urlopen(run.url("client") + "/", timeout=run.config.request_timeout) as response:
        html = response.read(2 << 20).decode()
    require('id="satellite-lag"' in html, "deployed Client HTML has no satellite lag panel")
    run.save("deployed-dashboard.json", {"origin": "running_cluster", "feed": body["feed"],
                                         "api": "/satellite/stats", "panel_present": True})
    return {"origin": "running_cluster", "panel_present": True, "lag_events": body["feed"].get("lag_events"),
            "visual_demo_pending": True}


def reference(run):
    path = run.config.reference_file
    if path is None:
        return {"skipped": True, "reason": "assistants reference unavailable; internal Audit checksum is not an independent reference"}
    expected = json.loads(Path(path).read_text())
    require(expected.get("origin") == "assistant" and expected.get("source") == "SATELIT",
            "reference adapter must declare origin=assistant and source=SATELIT")
    require(type(expected.get("through_offset")) is int and type(expected.get("count")) is int,
            "reference adapter must include through_offset and count")
    require(expected.get("canonicalization") == "aggregator-audit-checksum-v1",
            "reference canonicalization must explicitly match the Audit checksum; convert the supplied reference first")
    checksum = expected.get("checksum", "")
    require(isinstance(checksum, str) and len(checksum) == 64 and all(c in "0123456789abcdef" for c in checksum),
            "reference checksum must be lowercase SHA-256")
    before = run.aggregator("/internal/satellite/stats")["feed"]["offset"]
    if before != expected["through_offset"]:
        return {"skipped": True, "reason": f"reference cutoff {expected['through_offset']} differs from deployed offset {before}; cannot compare different prefixes"}
    require(run.env.get("AUDIT_TOKEN"), "AUDIT_TOKEN required for reference comparison")
    _, actual, _ = run.call("aggregator", "/internal/audit/checksum?source=SATELIT", token=run.env["AUDIT_TOKEN"])
    after = run.aggregator("/internal/satellite/stats")["feed"]["offset"]
    if before != after or actual.get("partial"):
        return {"skipped": True, "reason": "input advanced or storage was partial during comparison; no consistent reference cutoff"}
    result = {"origin": "running_cluster", "reference": expected, "actual": actual,
              "count_difference": actual["count"] - expected["count"], "checksum_matches": actual["checksum"] == checksum,
              "explanation": run.config.reference_difference_explanation or "No cause asserted; investigate any mismatch against the supplied reference."}
    run.save("assistant-reference-comparison.json", result)
    require(result["count_difference"] == 0 and result["checksum_matches"],
            "deployed output differs from assistants reference; see assistant-reference-comparison.json")
    return {"through_offset": before, "count_difference": 0, "checksum_matches": True}


def final_checks(run):
    run.stage("deployed-client-satellite-dashboard", dashboard, run)
    run.stage("assistants-reference-comparison", reference, run)
    if run.config.observe_external_burst:
        run.stage("external-60x-burst-recovery", real_burst, run)
        run.stage("external-burst-per-event-latency", burst_latency, run)
    require(getattr(run, "controlled_p4_verified", False), "controlled deployment checks did not complete and restore")
    run.report["full_p4_certified"] = False
    run.save("deployment-coverage.json", {"origin": "running_cluster", "full_p4_certified": False,
                                           "checks": run.report["automated_checks"],
                                           "pending": run.report["manual_evidence_required"]})


PREFLIGHT = SimpleNamespace(run_group=preflight)
FINAL = SimpleNamespace(run_group=final_checks)
