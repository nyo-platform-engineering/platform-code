"""Group 2 — bounded availability, worker loss, and recovery (criteria 4–6)."""
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
import json
import re
import subprocess
import time
import threading
import uuid

from helper import INFRA, ConcurrentDataChange, analytics, catalog, checksum, require, utc, worker_action_driver, storage_plan, worker_storage, print_monitor, output
from group1_distribution import (
    availability, distribution, event_key, export, preserve, verify_checksum,
)
from group3_analytics import analytic_query, reconcile_stable_window


@dataclass
class ReadProbe:
    path: str
    token: str
    params: dict
    kind: str


class Monitor:
    def __init__(self, run, name, writes, target=None, expect_ready=None):
        self.run = run
        self.name = name
        self.writes = writes
        self.target = target
        self.expect_ready = expect_ready
        self.stop = threading.Event()

        self.errors = []
        self.reads = 0
        self.read_seconds = []
        self.partial = 0
        self.write_ok = 0
        self.write_failed = 0
        self.metric_samples = 0
        self.metric_times = {}
        self.target_seen = False
        self.target_ready = None

    def __enter__(self):
        self.started = time.monotonic()
        self.last_progress_at = self.started
        self.threads = [
            threading.Thread(target=self.sample_requests),
            threading.Thread(target=self.sample_metrics),
        ]
        for thread in self.threads:
            thread.start()
        return self

    def read_probes(self):
        probes = [
            ReadProbe("/internal/audit/events", "AUDIT_TOKEN", {"limit": 50, "source": "BMKG"}, "audit"),
            ReadProbe("/internal/audit/checksum", "AUDIT_TOKEN", {"source": "BMKG"}, "checksum"),
            ReadProbe("/internal/hazards", "AGGREGATOR_TOKEN", {"order": "recent", "limit": 50}, "recent"),
        ]
        if hasattr(self.run, "analytics_query"):
            probes.append(ReadProbe(
                "/internal/analytics", "AGGREGATOR_TOKEN", self.run.analytics_query, "analytics",
            ))
        return probes

    def probe_read(self, probe):
        started = time.monotonic()
        response = self.run.api.request(probe.path, probe.token, probe.params)
        elapsed = time.monotonic() - started
        require(elapsed <= self.run.config.max_read_seconds, f"{probe.kind}: read exceeded latency bound")
        self.read_seconds.append(elapsed)
        availability(response, probe.kind)

        if self.writes and not response["partial"]:
            self.verify_complete_read(probe.kind, response)
        return response["partial"]

    def verify_complete_read(self, kind, response):
        """A complete response must still contain the data known before the loss."""
        stable_bmkg = [event for event in self.run.previous_events if event["source"] == "BMKG"]
        if kind == "checksum":
            # Live polls can add BMKG events during recovery. The exact controlled
            # catalog checksum is checked after recovery; this probe rejects lost totals.
            require(type(response.get("count")) is int and response["count"] >= len(stable_bmkg),
                    "complete checksum lost known event count")
            require(response.get("algorithm") == "sha256" and isinstance(response.get("checksum"), str)
                    and re.fullmatch("[0-9a-f]{64}", response["checksum"]), "invalid complete checksum")
        elif kind == "audit":
            require(len(response["items"]) == min(50, len(stable_bmkg)), "complete audit page lost known data")
            keys = [event_key(event, "audit") for event in response["items"]]
            require(all(a < b for a, b in zip(keys, keys[1:])), "audit page order violated")
        elif kind == "recent":
            require(len(response["items"]) == 50, "complete latest-50 page lost known data")
            keys = [event_key(event, "recent") for event in response["items"]]
            require(all(a > b for a, b in zip(keys, keys[1:])), "latest-50 global order violated")
        elif kind == "analytics":
            total = response.get("total")
            require(
                type(total) is int and total >= self.run.baseline_analytics["total"],
                "complete analytics lost known events",
            )

    def sample_requests(self):
        with ThreadPoolExecutor(max_workers=4) as pool:
            while not self.stop.is_set():
                started = time.monotonic()
                reads = [pool.submit(self.probe_read, probe) for probe in self.read_probes()]
                write = pool.submit(self.probe_write) if self.writes else None
                for future in reads:
                    try:
                        self.partial += int(future.result())
                        self.reads += 1
                    except Exception as error:
                        message = str(error) if isinstance(error, (ValueError, RuntimeError)) else type(error).__name__
                        self.errors.append(message)
                if write:
                    write.result()
                if time.monotonic() - self.last_progress_at >= 10:
                    output("PROGRESS", self.name,
                           f"Reads: {self.reads} successful ({self.partial} partial)",
                           f"Writes: {self.write_ok} acknowledged; {self.write_failed} rejected/unknown",
                           f"Metrics samples: {self.metric_samples}",
                           f"Elapsed: {time.monotonic() - self.started:.0f}s",
                           self.action_hint())
                    self.last_progress_at = time.monotonic()
                elapsed = time.monotonic() - started
                self.stop.wait(max(0, self.run.config.sample_interval - elapsed))

    def action_hint(self):
        if not self.target:
            return ""
        state = "unknown" if self.target_ready is None else "Ready" if self.target_ready else "NotReady"
        if self.run.config.automatic_worker_actions:
            action = "automatic worker action / node-state verification in progress"
        elif self.target_ready == self.expect_ready:
            action = "required state observed; press Enter to continue"
        elif self.expect_ready:
            action = "restore the worker with empty storage, then press Enter"
        else:
            action = "stop the worker and erase its storage, then press Enter"
        return f"Worker: {self.target} ({state})\nAction: {action}"

    def probe_write(self):
        reference = f"{self.run.run_id}-continuity-{uuid.uuid4().hex}"
        report = {
            "report_id": reference,
            "volcano_id": "MERAPI",
            "reported_at": utc(),
            "alert_level": "Waspada",
            "ash_column_height_m": 100,
            "eruption_count_24h": 1,
        }
        try:
            result = self.run.api.request(
                "/internal/ingest/pvmbg", "AGGREGATOR_TOKEN",
                body={"volcanic_reports": [report]},
            )
            require(result.get("changed") == 1, "continuity write was not acknowledged as new")
            self.run.acknowledged_writes.add(reference)
            self.write_ok += 1
        except Exception:
            # Unknown outcomes are never counted as acknowledged durable writes.
            self.write_failed += 1

    def inspect_metrics(self, sample):
        require(
            bool(sample["nodes"].get("items")) and bool(sample["pods"].get("items")),
            "metrics API returned no samples",
        )
        nodes = sample["topology"]["nodes"]
        ready_nodes = {node["name"] for node in nodes if node["ready"]}
        measured_nodes = {node["metadata"]["name"] for node in sample["nodes"]["items"]}
        unaffected_nodes = ready_nodes - {self.target}
        require(unaffected_nodes <= measured_nodes, "metrics missing an unaffected Ready node")

        for metric in sample["nodes"]["items"]:
            node_name = metric["metadata"]["name"]
            timestamps = self.metric_times.setdefault(node_name, set())
            timestamps.add(metric["timestamp"])

        for node in nodes:
            if node["name"] == self.target:
                self.target_ready = node["ready"]
                if node["ready"] == self.expect_ready:
                    self.target_seen = True
        self.metric_samples += 1

    def sample_metrics(self):
        path = self.run.folder / f"{self.name}-metrics.jsonl"
        while not self.stop.is_set():
            try:
                sample = self.run.cluster.metrics()
                self.inspect_metrics(sample)
            except Exception as error:
                message = str(error) if isinstance(error, ValueError) else type(error).__name__
                sample = {"at": utc(), "error": message}
                self.errors.append("metrics: " + message)
            with path.open("a") as stream:
                stream.write(json.dumps(sample) + "\n")
            self.stop.wait(self.run.config.metrics_interval)

    def summary(self):
        return {
            "read_successes": self.reads,
            "max_read_seconds": round(max(self.read_seconds, default=0), 3),
            "partial_responses": self.partial,
            "writes_acknowledged": self.write_ok,
            "writes_rejected_or_unknown": self.write_failed,
            "metric_samples": self.metric_samples,
            "target_state_observed": self.target_seen,
            "last_observed_target_ready": self.target_ready,
            "distinct_node_metric_timestamps": {
                node: len(times) for node, times in self.metric_times.items()
            },
            "errors": self.errors,
            "seconds": getattr(self, "ended", time.monotonic()) - self.started,
        }

    def verify_window(self):
        require(not self.errors, f"{self.name}: read or metrics probe failed; see monitor evidence")
        require(self.reads >= 3 and self.metric_samples >= 2, f"{self.name}: insufficient read/metrics samples")
        require(self.metrics_are_fresh(), f"{self.name}: metrics did not refresh")
        require(not self.target or self.target_seen, f"{self.name}: target node state was never observed")
        if self.writes:
            require(self.write_ok > 0, f"{self.name}: no successful concurrent writes")
            if self.expect_ready:
                require(self.write_failed == 0, f"{self.name}: write interruption while rejoining")

    def metrics_are_fresh(self):
        return bool(self.metric_times) and all(
            len(times) >= 2 for node, times in self.metric_times.items()
            if node != self.target
        )

    def __exit__(self, kind, value, traceback):
        try:
            if kind is None:
                remaining = self.run.config.min_window_seconds - (time.monotonic() - self.started)
                if remaining > 0:
                    output("INFO", "Information", f"collecting {remaining:.0f}s more evidence for {self.name}")
                    self.stop.wait(remaining)
                while not self.errors and not self.metrics_are_fresh():
                    output("WAIT", "Waiting", f"{self.name}: waiting for a second distinct node metrics timestamp")
                    self.stop.wait(self.run.config.metrics_interval)
        finally:
            self.stop.set()
            for thread in self.threads:
                thread.join()
            self.ended = time.monotonic()
            self.run.save(f"{self.name}-monitor.json", self.summary())
            print_monitor(self.summary())
        if kind is None:
            self.verify_window()


def read_reference(path):
    reference = json.loads(path.read_text())
    valid_count = type(reference.get("count")) is int and reference["count"] > 0
    digest = reference.get("checksum")
    valid_digest = isinstance(digest, str) and re.fullmatch("[0-9a-f]{64}", digest)
    require(
        reference.get("source") == "BMKG" and valid_count and valid_digest,
        "reference must have source=BMKG, positive count and lowercase SHA-256 checksum",
    )
    scope = reference.get("scope", "source")
    require(scope in ("source", "catalog"), "reference scope must be source or catalog")
    if scope == "catalog":
        require(
            reference.get("dataset_origin") == "development_mock"
            and reference.get("official") is False
            and isinstance(reference.get("source_ref_prefix"), str)
            and bool(reference["source_ref_prefix"]),
            "catalog-scoped reference requires an explicit development mock prefix",
        )
    return reference


def verify_reference(run, events):
    if not run.config.reference:
        return
    reference = read_reference(run.config.reference)
    original_bmkg = [
        event for event in events
        if event["source"] == "BMKG" and event["source_ref_id"] not in run.surge_refs
    ]
    if reference.get("scope") == "catalog":
        expected_refs = {record["event_id"] for record in catalog.read_catalog(run.config.catalog)}
        require(
            len(expected_refs) == reference["count"] and checksum(expected_refs) == reference["checksum"],
            "configured catalog file disagrees with its development reference",
        )
        original_bmkg = [event for event in original_bmkg if event["source_ref_id"] in expected_refs]
    digest = checksum(event["source_ref_id"] for event in original_bmkg)
    require(
        len(original_bmkg) == reference["count"] and digest == reference["checksum"],
        "BMKG data disagrees with the configured reference (excluding this run's regional surge)",
    )
    run.save("reference.json", reference)
    return {"matched": True, "count": len(original_bmkg), "checksum": digest,
            "scope": reference.get("scope", "source")}


def verify_global_reads(before, after, recent, latest):
    """Verify global order and completeness for records stable across live reads."""
    before_by_id = {event["hazard_id"]: event for event in before}
    after_by_id = {event["hazard_id"]: event for event in after}
    stable = {
        hazard_id: event for hazard_id, event in before_by_id.items()
        if after_by_id.get(hazard_id) == event
    }
    recent_by_id = {event["hazard_id"]: event for event in recent}
    for hazard_id, event in stable.items():
        require(recent_by_id.get(hazard_id) == event,
                f"recent pagination omitted or changed a stable event: {hazard_id}")

    availability(latest, "recent", complete=True)
    require(len(latest["items"]) == 50, "latest-50 returned fewer than 50 events")
    keys = [event_key(event, "recent") for event in latest["items"]]
    require(all(a > b for a, b in zip(keys, keys[1:])), "latest-50 global ordering violation")
    latest_ids = {event["hazard_id"] for event in latest["items"]}
    boundary = keys[-1]
    for hazard_id, event in stable.items():
        if event_key(event, "recent") >= boundary:
            require(hazard_id in latest_ids, f"latest-50 omitted a higher-ranked stable event: {hazard_id}")
    for event in latest["items"] + recent:
        saved = after_by_id.get(event["hazard_id"])
        require(saved is not None, "recent query returned an event absent from the later audit export")
        require((saved["source"], saved["source_ref_id"]) == (event["source"], event["source_ref_id"]),
                "recent/audit event identities disagree")
        if event["hazard_id"] in stable:
            require(event == saved, "recent query changed a stable canonical event")

    unchanged = before_by_id == after_by_id
    if unchanged:
        expected = sorted(before, key=lambda event: event_key(event, "recent"), reverse=True)
        require(recent == expected and latest["items"] == expected[:50],
                "recent pagination/latest-50 disagrees with the unchanged full audit")
    return {
        "global_order_verified": True,
        "stable_events_compared": len(stable),
        "live_records_added_or_updated": len(after_by_id) - len(stable),
        "exact_whole_dataset_comparison": unchanged,
        "comparison": "unchanged dataset" if unchanged else "stable records across live exports",
    }


def read_checksums(run):
    results = {}
    for source in ("all", "BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI"):
        params = None if source == "all" else {"source": source}
        response = run.api.request("/internal/audit/checksum", "AUDIT_TOKEN", params)
        availability(response, "checksum", complete=True)
        require(type(response.get("count")) is int and response["count"] >= 0, "invalid checksum count")
        require(response.get("algorithm") == "sha256" and isinstance(response.get("checksum"), str)
                and re.fullmatch("[0-9a-f]{64}", response["checksum"]), "invalid checksum response")
        results[source] = response
    return results


def verify_sources(before_sums, after_sums, before_events, after_events):
    """An exact checksum comparison requires identical bookend source identities."""
    results = {}
    for source, first in before_sums.items():
        last = after_sums[source]
        old_rows = [e for e in before_events if source == "all" or e["source"] == source]
        new_rows = [e for e in after_events if source == "all" or e["source"] == source]
        require(first["count"] <= len(old_rows) <= len(new_rows) <= last["count"],
                f"{source}: export counts fall outside the live checksum bounds")
        unchanged = first["count"] == last["count"] and first["checksum"] == last["checksum"]
        if unchanged:
            verify_checksum(first, old_rows)
            verify_checksum(last, new_rows)
        else:
            require(last["count"] > first["count"], f"{source}: checksum changed without source growth")
            output("INFO", "Information", f"{source}: source grew {first['count']} → {last['count']}; "
                  "whole-source checksum comparison is not a shared snapshot.")
        results[source] = {
            "before": first, "after": last,
            "independent_checksum_verified": unchanged,
            "status": "exact_match" if unchanged else "concurrent_writes_observed",
            "exported_before": len(old_rows), "exported_after": len(new_rows),
        }
    return results


def verify_preservation(run, events, expected_writes):
    updates = preserve(run.previous_events, events, run.catalog_identities)
    stored_reports = {event["source_ref_id"] for event in events if event["source"] == "PVMBG"}
    require(expected_writes <= stored_reports,
            "an acknowledged continuity write is missing after recovery")
    return updates


def read_live_view(run):
    """Bracket independent API reads; never claim that live endpoints share a snapshot."""
    attempt = 0
    while True:
        attempt += 1
        output("PROGRESS", f"Live comparison window {attempt}", "Export, recent pages, analytics, re-export")
        expected_writes = set(run.acknowledged_writes)
        sums_before = read_checksums(run)
        before = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
        require(len(before) >= 51, "need at least 51 events to exercise latest-50 pagination")
        recent = export(run.api, "recent", run.config.page_size, run.config.max_pages)
        latest = run.api.request("/internal/hazards", "AGGREGATOR_TOKEN", {"order": "recent", "limit": 50})
        query = analytic_query(before, run.config.interval, run.config.analytics_end)
        output("PROGRESS", "Analytics comparison range", f"From: {query['from']}", f"To: {query['to']}")
        summary = run.api.request("/internal/analytics", "AGGREGATOR_TOKEN", query)
        after = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
        sums_after = read_checksums(run)

        preserve(before, after, run.catalog_identities)
        live_updates = verify_preservation(run, after, expected_writes)
        reference = verify_reference(run, after)
        source_checks = verify_sources(sums_before, sums_after, before, after)
        global_reads = verify_global_reads(before, after, recent, latest)
        try:
            comparison = reconcile_stable_window(before, after, summary, query)
        except ConcurrentDataChange as error:
            output("WAIT", "Waiting", f"{error}; retrying comparison window (Ctrl+C to stop)")
            time.sleep(run.config.sample_interval)
            continue
        return {
            "events": after, "checksum": sums_after["all"], "reference": reference,
            "source_checks": source_checks, "global_reads": global_reads,
            "query": query, "analytics": summary, "reconciliation": comparison,
            "live_updates_since_previous_checkpoint": live_updates,
        }


def capture_snapshot(run, name, redundant=True):
    view = read_live_view(run)
    events = view["events"]
    placement = None
    if redundant:
        # Exhaust the controlled dataset rather than querying a constantly growing
        # live feed's location for every chart reconciliation.
        placement_events = [event for event in events if
            (event["source"], event["source_ref_id"]) in run.catalog_identities
            or event["source_ref_id"] in run.surge_refs
            or event["source"] == "PVMBG" and event["source_ref_id"] in run.acknowledged_writes]
        placement = distribution(run.api, placement_events, poll_interval=run.config.sample_interval)
    topology = run.cluster.topology()
    if placement:
        cluster_nodes = {node["name"] for node in topology["nodes"]}
        require(set(placement["copies_per_node"]) <= cluster_nodes,
                "audit locations do not correspond to Kubernetes node identities")
    run.previous_events = events
    run.analytics_query = view["query"]
    run.baseline_analytics = view["analytics"]
    run.save(f"{name}.json", dict(view, placement=placement,
                                placement_scope="controlled_catalog_and_run_writes", topology=topology))
    placement_summary = None if placement is None else {
        key: value for key, value in placement.items() if key != "locations"
    }
    return {"events": len(events), "checksum": view["checksum"]["checksum"],
            "source_checks": {source: result["status"] for source, result in view["source_checks"].items()},
            "global_reads": view["global_reads"], "placement": placement_summary,
            "analytics": view["reconciliation"], "reference": view["reference"]}


def wait_for_recovery(run, name, redundant):
    started = time.monotonic()
    attempt = 0
    while True:
        attempt += 1
        try:
            output("PROGRESS", name, f"Recovery check: {attempt}", f"Elapsed: {time.monotonic() - started:.0f}s")
            return capture_snapshot(run, name, redundant)
        except (ValueError, RuntimeError, OSError, KeyError, TypeError) as error:
            last_error = str(error) if isinstance(error, (ValueError, RuntimeError)) else type(error).__name__
            output("WAIT", "Waiting", f"{name}: {last_error}")
            time.sleep(run.config.sample_interval)


def run_worker_action(run, name, worker, rejoin):
    """Run the local lab's allowlisted power action after storage preparation."""
    driver = worker_action_driver(run.config, worker)
    node = next((node for node in run.cluster.topology()["nodes"] if node["name"] == worker), None)
    require(node is not None and not node["control_plane"], "refusing to power off the control plane or an unknown node")
    action = "start-worker" if rejoin else "stop-worker"
    command = ["bash", str(INFRA / "scripts/vm/main.sh"), action, worker]
    record = {"at": utc(), "worker": worker, "action": action, "driver": driver,
              "action_scope": "power_only", "status": "started"}
    actions = run.report.setdefault("worker_actions", [])
    actions.append(record)
    log_path = run.folder / f"{name}-worker-action.log"
    process = None
    output("ACTION", "Worker action", f"{action} {worker} via {driver}")
    try:
        with log_path.open("w") as log:
            process = subprocess.Popen(command, cwd=INFRA, stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT, text=True, bufsize=1)
            for line in process.stdout:
                log.write(line)
                log.flush()
                if line.strip():
                    output("VM", "VM output", line.rstrip())
            require(process.wait() == 0, f"{action} failed; see {log_path}")
        record["status"] = "completed"
    except BaseException as error:
        record["status"] = "interrupted" if isinstance(error, KeyboardInterrupt) else "failed"
        raise
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        record["finished_at"] = utc()
        run.save("worker-actions.json", actions)


def wait_for_node_state(run, monitor, worker, ready):
    started = time.monotonic()
    expected = "Ready" if ready else "NotReady"
    last_state, last_print = None, 0
    while True:
        nodes = run.cluster.topology()["nodes"]
        node = next((node for node in nodes if node["name"] == worker), None)
        observed = "missing" if node is None else "Ready" if node["ready"] else "NotReady"
        if observed != last_state or time.monotonic() - last_print >= 10:
            output("PROGRESS", worker, f"Observed: {observed}; waiting for: {expected}",
                   f"Elapsed: {time.monotonic() - started:.0f}s")
            last_state, last_print = observed, time.monotonic()
        if node is not None and node["ready"] == ready:
            monitor.target_seen = True
            monitor.target_ready = ready
            run.save(f"{monitor.name}-observed-node.json", {"at": utc(), "node": node})
            return
        time.sleep(run.config.sample_interval)


def disrupt_worker(run, name, worker, rejoin=False):
    print(f"\n{name}: target {worker}.", flush=True)
    proof = None
    if run.config.automatic_worker_actions:
        if rejoin:
            plan = run.storage_plans[worker]
            input(f"Enter → start {worker} with its erased database volumes and verify rebuild: ")
        else:
            plan = storage_plan(run, worker)
            run.storage_plans[worker] = plan
            run.save(f"{name}-storage-plan.json", plan)
            print("The script will stop database processes, erase these AAT store volumes, "
                  "and cut worker power. OS and other services' volumes are retained.", flush=True)
            for volume in plan["volumes"]:
                print("  " + volume["path"], flush=True)
            input(f"Enter → erase {worker}'s store data and simulate worker loss: ")
        try:
            with Monitor(run, name, writes=True, target=worker, expect_ready=rejoin) as monitor:
                if not rejoin:
                    proof = worker_storage(run, name, plan, "erase")
                    require(all(v["files_before"] > 0 and v["entries_after"] == 0 for v in proof["volumes"]),
                            "storage erasure did not empty previously populated volumes")
                    output("RESULT", "Storage erasure", f"Volumes erased: {len(proof['volumes'])}", "All verified empty")
                run_worker_action(run, name, worker, rejoin)
                wait_for_node_state(run, monitor, worker, ready=rejoin)
                result = wait_for_recovery(run, name, redundant=rejoin)
                if rejoin:
                    proof = worker_storage(run, name, plan, "inspect")
                    require(all(v["rebuilt"] for v in proof["volumes"]), "a store volume has not rebuilt PostgreSQL data")
            # Include every write acknowledged through the end of the monitor.
            final_events = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
            verify_preservation(run, final_events, set(run.acknowledged_writes))
            run.previous_events = final_events
        except BaseException:
            # Leave the failed result intact, but bring this lab worker back so
            # an interrupted erase cannot strand its surviving peer indefinitely.
            try:
                output("RECOVERY", "Worker recovery", f"restarting {worker} after the failed/interrupted check")
                run_worker_action(run, name + "-cleanup", worker, rejoin=True)
                deadline = time.monotonic() + 60
                while True:
                    recovery = subprocess.run(
                        ["vagrant", "ssh", worker, "--no-tty", "-c", "sudo systemctl start k3s-agent"],
                        cwd=INFRA / "local", capture_output=True, text=True, timeout=20,
                    )
                    (run.folder / f"{name}-cleanup.log").write_text(recovery.stdout + recovery.stderr)
                    if recovery.returncode == 0:
                        output("RECOVERY", "Worker recovery", f"{worker} started; test remains failed/interrupted")
                        break
                    require(time.monotonic() < deadline, "worker SSH not ready for cleanup")
                    time.sleep(2)
            except Exception:
                output("INFO", "Information", f"Restore {worker}: bash scripts/vm/main.sh start-worker {worker}; "
                      f"then from infrastructure/local: vagrant ssh {worker} "
                      "-c 'sudo systemctl start k3s-agent'")
            raise
    else:
        print("Use the official Tester Agent to stop/erase or restore the worker.", flush=True)
        with Monitor(run, name, writes=True, target=worker, expect_ready=rejoin) as monitor:
            input(f"After performing the action for {worker}, press Enter to verify: ")
            wait_for_node_state(run, monitor, worker, ready=rejoin)
            result = wait_for_recovery(run, name, redundant=rejoin)
        final_events = export(run.api, page_size=run.config.page_size, max_pages=run.config.max_pages)
        verify_preservation(run, final_events, set(run.acknowledged_writes))
        run.previous_events = final_events
    if rejoin:
        require(result["placement"]["copies_per_node"].get(worker, 0) > 0,
                "rejoined node is Ready but does not hold canonical data")
    result.update(worker=worker, power_action_automatic=run.config.automatic_worker_actions,
                  disk_erasure_verified=proof is not None,
                  storage_loss=proof, monitor=monitor.summary(),
                  acknowledged_writes_preserved=len(run.acknowledged_writes))
    return result


def run_group(run):
    print("\nGroup 2: failure and recovery — criteria 4–6", flush=True)
    first_worker = run.config.worker
    second_worker = run.config.second_worker

    run.stage("first-loss", disrupt_worker, run, "first-loss", first_worker)
    run.stage(
        "first-worker-rejoin", disrupt_worker,
        run, "first-worker-rejoin", first_worker, rejoin=True,
    )
    # Only proceed after the previous checkpoint has verified full redundancy.
    run.stage("second-loss", disrupt_worker, run, "second-loss", second_worker)
    run.stage(
        "final-rejoin", disrupt_worker,
        run, "final-rejoin", second_worker, rejoin=True,
    )
