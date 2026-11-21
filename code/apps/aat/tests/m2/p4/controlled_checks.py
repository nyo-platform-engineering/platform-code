"""Criteria 2–5/8 against the existing deployed consumer, Client and Citus."""
import datetime as dt
import math
import time

from common import output, require, parse_time
from run_progress import Progress
from p4_fixtures import CASES, EXPECTED, A, B, C, key
from verification_session import Verification
import consumer_stats


def wait_offset(run, target):
    seen = {}

    def committed():
        body = run.aggregator("/internal/satellite/stats")
        feed = body["feed"]
        offset = feed.get("offset")
        seen.update(offset=offset, lag=feed.get("lag_events"), rejected=feed.get("rejected_events"))
        require(offset is None or offset <= target, "consumer advanced past the finite fixture cutoff")
        return body if offset == target else None
    return run.wait(f"deployed consumer offset {target}", committed, run.config.resume_timeout,
                    status=lambda: f"committed offset {seen.get('offset')} of {target}; "
                                   f"lag {seen.get('lag')}; rejected {seen.get('rejected')}")


def verify_cases(run, session):
    identity = None
    checkpoints = []
    for offset, expected in EXPECTED.items():
        session.feed("/admin/publish", {"count": offset + 1})
        status = wait_offset(run, offset)
        hazards = run.hazards("SATELIT")
        if expected is None:
            require(not hazards, "one isolated detection must not create a WILDFIRE")
            continue
        require(len(hazards) == 1, f"offset {offset}: expected one fire, got {len(hazards)}")
        h = hazards[0]
        severity, count = expected
        require(h["severity"] == severity and h["attributes"]["detection_count"] == count,
                f"offset {offset}: expected {severity}/{count}, got {h['severity']}/{h['attributes']['detection_count']}")
        require(h["source"] == "SATELIT" and h["hazard_type"] == "WILDFIRE", "wrong HazardEvent mapping")
        require(parse_time(h["occurred_at"]).astimezone(dt.timezone.utc).isoformat() == "2026-11-12T12:00:00+00:00", "arrival time replaced event time")
        identity = identity or h["hazard_id"]
        require(h["hazard_id"] == identity, "update/retract created a new HazardEvent identity")
        if severity == "NORMAL":
            require(h["attributes"].get("status") == "dissolved", "shrinking to one detection must dissolve the same event")
        checkpoints.append({"offset": offset, "hazard_id": identity, "severity": severity,
                            "detection_count": count, "detection_keys": h["attributes"]["detection_keys"]})
        run.save("controlled-mapping-checkpoints.json", checkpoints)
        output("INFO", f"Fixture offset {offset}: {severity}, {count} detections, same HazardEvent ID")
    status = run.aggregator("/internal/satellite/stats")
    require(status["late_detections"] == 1 and status["retracted_detections"] == 2
            and status["low_confidence_detections"] == 1, "wrong retained/excluded counts")
    snapshot = session.snapshot()
    rows = {d["detection_key"]: d for d in snapshot["detections"]}
    boundary = key("N21", 1200, "0.00000", "110.00200")
    beyond = key("N21", 1159, "0.00000", "110.00300")
    require(not rows[boundary]["late"] and rows[boundary]["fire_ref"], "six-hour boundary detection was not included")
    require(rows[beyond]["late"] and rows[beyond]["fire_ref"] is None, "beyond-boundary detection was lost or clustered")
    result = {"origin": "controlled_input_on_running_deployment", "criteria": [1, 2, 3],
              "allowed_lateness_minutes": 360, "late_retained": 1, "same_hazard_id": identity,
              "update_shrink_retract_dissolve_revive_verified": True, "checkpoints": checkpoints}
    run.save("controlled-event-time-corrections.json", result)
    return {k: v for k, v in result.items() if k != "checkpoints"}


def verify_pair_oracle(snapshot, total):
    """Independent expected membership for separated fixture pairs, not self-checksum."""
    burst_count = total - len(CASES)
    require(len(snapshot["detections"]) == burst_count + 7, "not every unique fixture detection is retained")
    require(len(snapshot["hazards"]) == burst_count // 2 + 1, "missing/duplicate fires after finite burst")
    expected = {}
    for pair in range(burst_count // 2):
        lat = -3 + (pair % 100) * .03
        lon = f"{100 + (pair // 100) * .03:.5f}"
        keys = [key("N20", 1200, f"{lat:.5f}", lon, "2026-11-13"),
                key("N21", 1200, f"{lat + .001:.5f}", lon, "2026-11-13")]
        expected[keys[0]] = {"keys": keys, "latitude": round(lat + .0005, 5), "longitude": float(lon)}
    differences = []
    seen = set()
    for h in snapshot["hazards"]:
        if not str(h["occurred_at"]).startswith("2026-11-13"):
            continue
        attrs = h["attributes"]
        keys = sorted(attrs.get("detection_keys", []))
        want = expected.get(keys[0]) if keys else None
        if (not want or keys != want["keys"] or keys[0] in seen or attrs.get("detection_count") != 2
                or h["source"] != "SATELIT" or h["hazard_type"] != "WILDFIRE"
                or h["severity"] != "WASPADA" or attrs.get("total_frp_mw") != 10
                or sorted(attrs.get("satellites", [])) != ["N20", "N21"]
                or abs(h["latitude"] - want["latitude"]) > .000001
                or abs(h["longitude"] - want["longitude"]) > .000001):
            differences.append(h["source_ref_id"])
        if keys:
            seen.add(keys[0])
    require(seen == set(expected), "finite stream has missing or unexpected pair identities")
    return differences


def verify_burst(run, session):
    rate, duration = run.config.verification_normal_rate, run.config.verification_burst_seconds
    source = session.feed("/admin/burst", {"normal_rate": rate, "seconds": duration})
    samples = []
    recovered = None
    saw_fast = saw_end = False
    started = time.monotonic()
    total = source["total_events"]
    with Progress(run.folder, "controlled-60x-burst", clean=run.clean,
                  writer=lambda text: output("WAIT", text)) as progress:
        with progress.phase("actual SSE publication and deployed consumer recovery", duration + 65):
            while time.monotonic() - started < duration + 65:
                before = session.feed("/v1/session")
                status = run.aggregator("/internal/satellite/stats")
                after = session.feed("/v1/session")
                feed = status["feed"]
                require(status.get("verification_schema") == session.schema, "read escaped verification schema")
                require(feed["rejected_events"] == 0, "controlled feed events were rejected")
                offset = feed.get("offset")
                lag = None if offset is None else max(after["latest_offset"] - offset, 0)
                saw_fast |= before["speed"] == 60
                ended = after["elapsed_seconds"] >= after["burst_end_elapsed_seconds"]
                saw_end |= ended
                # Bound recovery in the source's monotonic clock, not the laptop/VM clock.
                if ended and lag == 0 and recovered is None:
                    recovered = after["elapsed_seconds"] - after["burst_end_elapsed_seconds"]
                sample = {**after, "offset": offset, "lag_events": lag}
                samples.append(sample)
                run.save("controlled-burst-progress.json", samples)
                progress.detail(f"speed={after['speed']}x; offset={offset}; latest={after['latest_offset']}; lag={lag}")
                _, dashboard, _ = run.call("client", "/satellite/stats", token=session.client_token)
                require(dashboard.get("verification_schema") == session.schema and "lag_events" in dashboard["feed"],
                        "deployed Client dashboard is not reading the tested consumer")
                require(not ended or recovered is not None or after["elapsed_seconds"] - after["burst_end_elapsed_seconds"] <= 60,
                        "deployed consumer did not recover within 60s after actual burst end")
                if offset == total - 1 and ended:
                    break
                time.sleep(.5)
    require(saw_fast and saw_end and recovered is not None and recovered <= 60,
            "actual 60x burst and recovery were not proven")
    wait_offset(run, total - 1)
    snapshot = session.snapshot()
    differences = verify_pair_oracle(snapshot, total)
    run.save("controlled-burst-oracle.json", {"expected_pair_count": (total - len(CASES)) // 2,
                                             "actual_hazards": snapshot["hazards"], "differences": differences})
    require(not differences, "finite burst differs from independent pair oracle")
    # Collect current Pod logs, not a six-hour mixture of live and fixture runs.
    batches, _ = consumer_stats.pod_logs(run)
    selected = [b for b in batches if b.get("verification_schema") == session.schema
                and b.get("offset", -1) >= len(CASES)]
    require(selected and all(isinstance(b.get("ingest_latency_samples_ms"), list) for b in selected),
            "deployed image must emit per-event latency samples")
    latencies = [n for b in selected for n in b["ingest_latency_samples_ms"]]
    require(len(latencies) == total - len(CASES), "per-event latency coverage does not match finite burst input")
    require(all(isinstance(n, (float, int)) and math.isfinite(n) and n >= 0 for n in latencies), "invalid latencies")
    ordered = sorted(latencies)
    result = {"origin": "controlled_input_on_running_deployment", "normal_events_per_second": rate,
              "burst_events_per_second": rate * 60, "burst_seconds": duration,
              "recovery_seconds": recovered, "recovery_limit_seconds": 60,
              "published_events": total, "committed_offset": total - 1,
              "unique_detections": len(snapshot["detections"]), "oracle_differences": 0,
              "latency_samples": len(latencies), "p95_ingest_to_commit_ms": ordered[math.ceil(.95 * len(ordered)) - 1],
              "deployed_client_lag_verified": True, "image": session.baseline["image"]}
    run.save("controlled-burst-latencies.json", latencies)
    run.save("controlled-burst.json", result)
    run.save("uninterrupted-state.json", snapshot)
    run.p4_baseline_state, run.p4_total = snapshot, total
    return result


def container_status(run, pod):
    body = run.cluster.kubectl("get", "pod", pod, "-o", "json", json_output=True)
    return next((c for c in body.get("status", {}).get("containerStatuses", [])
                 if c.get("name") == "aggregator"), {})


def capture_crash_exit(run, pod, prior):
    """Capture the original container's exit before startup crashes overwrite lastState."""
    prior_id = prior["containerID"]
    def terminated():
        current = container_status(run, pod)
        for state in (current.get("state", {}), current.get("lastState", {})):
            exit_state = state.get("terminated", {})
            if exit_state.get("containerID") == prior_id:
                return {"termination": exit_state, "container_status": current}
        if current.get("ready") and current.get("containerID") != prior_id:
            # A ready replacement cannot recreate lost history by waiting longer.
            return {"termination": None, "container_status": current}
        return None
    evidence = run.wait("original Aggregator container termination", terminated, run.config.resume_timeout)
    run.save("controlled-crash-exit.json", {"pod": pod, "original_container": prior, **evidence})
    # Check outside wait(): ValueError inside its callback is treated as transient.
    exit_state = evidence["termination"]
    require(exit_state is not None,
            "original consumer exit evidence was overwritten or missing; see controlled-crash-exit.json")
    require(exit_state.get("exitCode") == 137,
            f"owner exited with code {exit_state.get('exitCode')}, expected 137 from the verification checkpoint; "
            "see controlled-crash-exit.json")
    return evidence


def verify_crash(run, session):
    session.activate("crash")
    session.feed("/admin/publish", {"count": 1})
    wait_offset(run, 0)
    before = session.snapshot()
    session.hold_commit()
    session.feed("/admin/publish", {"count": 2})
    def checkpoint():
        return session.sql(f'''SELECT row_to_json(a) FROM (
 SELECT l.pid, host(a.client_addr) AS address FROM pg_locks l JOIN pg_stat_activity a USING(pid)
 JOIN "{session.schema}".poll_owners p ON p.backend_pid=l.pid AND p.source='SATELIT'
 WHERE l.locktype='advisory' AND l.objid={session.lock_key} AND NOT l.granted LIMIT 1) a;''')
    blocked = run.wait("consumer after HazardEvent writes, before offset commit", checkpoint, 30)
    session.feed("/admin/pause", {})  # Stop new owners taking more fixture input after the kill.
    # PostgreSQL inet::text includes a /32 or /128 mask; host() above returns
    # the bare address used by Kubernetes. Check all addresses for dual-stack Pods.
    pods = session.pods()
    owners = [p for p in pods if blocked["address"] in
              {p.get("status", {}).get("podIP"),
               *(entry.get("ip") for entry in p.get("status", {}).get("podIPs", []))}]
    run.save("controlled-crash-owner.json", {"backend_pid": blocked["pid"], "address": blocked["address"],
             "pods": [{"name": p["metadata"]["name"], "addresses": p.get("status", {}).get("podIPs", [])}
                      for p in pods], "matched_pods": [p["metadata"]["name"] for p in owners]})
    require(len(owners) == 1, f"cannot identify the blocked consumer Pod safely: address={blocked['address']}, matches={len(owners)}")
    pod = owners[0]["metadata"]["name"]
    prior = next(c for c in owners[0]["status"]["containerStatuses"] if c["name"] == "aggregator")
    prior_id = prior.get("containerID")
    require(prior_id, "cannot crash an owner without its original container ID")
    # Invoke the fault endpoint on the identified owner Pod, never through a load-balanced Service.
    script = ('exec wget -qO- --timeout=10 --post-data="" '
              '--header="Authorization: Bearer ${AGGREGATOR_TOKEN}" '
              'http://127.0.0.1:8083/internal/satellite/verification/crash')
    try:
        run.cluster.kubectl("exec", pod, "-c", "aggregator", "--", "sh", "-c", script)
    except ValueError:
        pass  # The owner exits before sending a response; verify DB and container state below.
    exit_evidence = capture_crash_exit(run, pod, prior)
    run.wait("killed consumer database backend to exit",
             lambda: session.sql(f"SELECT to_json(NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid={int(blocked['pid'])}));"), 30)
    require(session.snapshot() == before, "partial detection/HazardEvent/offset survived the crash")
    session.release_lock()
    session.sql(f'DROP TRIGGER p4_checkpoint ON "{session.schema}".satellite_feeds; SELECT to_json(true);')
    def restarted():
        current = container_status(run, pod)
        return current if current.get("ready") and current.get("containerID") != prior_id else None
    def restart_status():
        current = container_status(run, pod)
        last_exit = current.get("lastState", {}).get("terminated", {})
        return (f"pod {pod}: ready={current.get('ready')}, restarts={current.get('restartCount')}, "
                f"state={list((current.get('state') or {}).keys())}, last_exit={last_exit.get('exitCode')}, "
                f"reason={last_exit.get('reason')}")
    replacement = run.wait("killed Aggregator container restart", restarted, run.config.resume_timeout, status=restart_status)
    run.save("controlled-crash-restart.json", {"pod": pod, "original_container": prior,
             "verified_termination": exit_evidence["termination"], "replacement_container": replacement})
    run.forwards.close("aggregator"); run.forwards.urls.pop("aggregator", None)
    session.feed("/admin/resume-all", {"count": run.p4_total})
    wait_offset(run, run.p4_total - 1)
    recovered = session.snapshot()
    run.save("recovered-state.json", recovered)
    equal = recovered == run.p4_baseline_state
    run.save("controlled-crash-equivalence.json", {"same_prefix": run.p4_total - 1, "equal": equal,
             "fault": "abrupt deployed owner exit 137, bypassing defers after HazardEvent writes before offset commit", "pod": pod,
             "database_backend_pid": blocked["pid"], "old_container_id": prior_id})
    require(equal, "crashed and uninterrupted full business states differ at the same input cutoff")
    connections = session.feed("/v1/session")["stream_connections"]
    session.feed("/admin/replay", {})
    def reconnected():
        source = session.feed("/v1/session")
        if source["stream_connections"] <= connections:
            return None
        require(source["last_from"] == run.p4_total - 1, "SSE reconnect did not resume from the committed offset")
        return source
    replay = run.wait("actual SSE reconnect at committed offset", reconnected, run.config.resume_timeout,
                      status=lambda: "feed connections {stream_connections}, last from {last_from}".format(**session.feed("/v1/session")))
    time.sleep(2)
    require(session.snapshot() == recovered, "inclusive reconnect/replay changed committed business state")
    result = {"origin": "running_deployment", "hard_crash_before_commit": True,
              "same_prefix": run.p4_total - 1, "full_state_equal": True, "inclusive_replay_unchanged": True,
              "reconnect_from": replay["last_from"], "stream_connections": replay["stream_connections"]}
    run.save("controlled-reconnect.json", result)
    return result


def run_group(run):
    input("Enter → run controlled SSE cases on this Aggregator/Client/Citus deployment; temporary schemas and env are restored on exit: ")
    with Verification(run) as session:
        session.activate("baseline")
        run.stage("controlled-event-time-update-retract", verify_cases, run, session)
        run.stage("deployed-60x-burst-no-loss-latency", verify_burst, run, session)
        run.stage("deployed-hard-crash-same-prefix-reconnect", verify_crash, run, session)
    run.controlled_p4_verified = True  # Only set after restoration succeeds.
