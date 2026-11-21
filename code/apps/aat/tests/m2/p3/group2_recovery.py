"""P3.2–4: poison isolation, retries and ordered replay."""
import base64
import json

from fixtures import encode, incompatible_rows, message_id, poison_cases, report, schema_mode
from support import require


def poison(run):
    before = run.restart_snapshot()
    bmkg_before = run.sql("SELECT to_json(max(ingested_at)) FROM hazard_events WHERE source='BMKG';")
    results = {}
    for name, (raw, kind, reason) in poison_cases(run.name).items():
        upstream = b"[" + raw + b"]" if kind == "record" else raw
        run.serve(upstream)
        detail = run.captured(raw, kind)
        require(reason in detail["reason"], name + ": wrong failure classification")
        results[name] = {"message_id": detail["message_id"], "raw_bytes": len(raw), "reason": detail["reason"]}
        good = report(run.name + "-after-" + name)
        run.serve(encode([good]))
        run.wait("healthy message after " + name, lambda: run.hazard(good["report_id"]), run.config.recovery_seconds)
        run.check_restarts(before)
    def bmkg_progress():
        current = run.sql("SELECT to_json(max(ingested_at)) FROM hazard_events WHERE source='BMKG';")
        return current if current and (not bmkg_before or current > bmkg_before) else None
    run.wait("BMKG continues while PVMBG poison is parked", bmkg_progress, run.config.recovery_seconds)
    run.save("poison-restarts.json", {"before": before, "after": run.check_restarts(before)})
    return {"cases": results, "healthy_PVMBG_and_BMKG_progress": True,
            "capture_scope": "original bytes <=8 MiB; oversized record >2 MiB; not unlimited payload capture"}


def prepare_replay(run):
    first, second, missing = incompatible_rows(run.name)
    seed = report(first["report_id"])
    run.serve(encode([seed]))
    run.wait("initial row for ordered updates", lambda: run.hazard(seed["report_id"]), run.config.recovery_seconds)
    run.serve(encode([first]))
    first_raw = encode(first)
    run.captured(first_raw, "record")
    run.serve(encode([second]))
    second_raw = encode(second)
    identity = message_id(second_raw)
    detail = run.wait("second version captured behind parked first", lambda: run.detail(identity), run.config.recovery_seconds)
    require(detail["status"] == "retrying" and detail["attempts"] == 0,
            "later version bypassed its parked predecessor")
    require(base64.b64decode(detail["raw_base64"], validate=True) == second_raw, "ordered raw bytes changed")
    require(run.hazard(seed["report_id"])["severity"] == "WASPADA", "parked version changed canonical data")
    run.save("ordering-blocked.json", detail)
    run.serve(encode([missing]))
    run.captured(encode(missing), "record")
    run.replay_ids.update((message_id(first_raw), identity, message_id(encode(missing))))
    run.ordering = (first, second)
    # Replay only schema failures created during this run.
    for row in run.unprocessed():
        if row["message_id"] in run.baseline_unprocessed:
            continue
        detail = run.detail(row["message_id"])
        if not detail or detail.get("kind") != "record" or detail["status"] == "processed":
            continue
        try:
            value = json.loads(base64.b64decode(detail["raw_base64"], validate=True))
        except (ValueError, TypeError):
            continue
        if isinstance(value, dict) and schema_mode(value) in ("rename_field", "type_change", "missing_required"):
            run.replay_ids.add(row["message_id"])
    return {"schema_messages": len(run.replay_ids), "later_version_blocked": True}


def replay(run):
    first, second = run.ordering
    first_id, second_id = message_id(encode(first)), message_id(encode(second))
    refs = set()
    for identity in run.replay_ids:
        detail = run.detail(identity)
        if detail and detail.get("source_ref_id"):
            refs.add(detail["source_ref_id"])
    run.set_env(PVMBG_SCHEMA_POLICY="v2")
    with run.locked_row(first["report_id"]) as locker:
        run.ops("unprocessed/" + first_id + "/replay", method="POST")
        def retrying():
            value = run.detail(first_id)
            run.save("transient-retry-progress.json", {"message": value})
            require(not value or value.get("status") != "processed", "synthetic row lock released before retry observation")
            return value if value and value.get("status") == "retrying" and value.get("reason") == "processing_unavailable" else None
        first_retry = run.wait("transient database failure classified retrying", retrying, 20)
        metadata = next((row for row in run.unprocessed() if row["message_id"] == first_id), None)
        require(metadata and metadata.get("retryable") is True, "transient metadata is not retryable")
        from support import validate_raw
        validate_raw(first_retry, encode(first), "record", status="retrying")
        run.save("transient-retry-first.json", first_retry)
        def retried():
            value = retrying()
            return value if value and value["attempts"] > first_retry["attempts"] else None
        next_retry = run.wait("automatic retry with increasing attempts", retried, run.config.retry_observe_seconds)
        later = run.detail(second_id)
        require(later["status"] != "processed" and later["attempts"] == 0, "same-key order violated during retry")
        healthy = report(run.name + "-while-retrying")
        run.serve(encode([healthy]))
        run.wait("different key progresses during transient retry", lambda: run.hazard(healthy["report_id"]), 15)
        require(locker.poll() is None, "lock expired before retry/order observations completed")
        require(run.detail(second_id)["attempts"] == 0, "successor advanced while predecessor remained blocked")
        run.save("transient-retry.json", {"first": first_retry, "later_attempt": next_retry, "blocked_successor": later})
    for identity in sorted(run.replay_ids - {first_id}):
        status, _, _ = run.http("client", "/ops/unprocessed/" + identity + "/replay", token=run.tokens["analyst"],
                                 method="POST", expected=(200, 409))
        if status == 409:
            require(run.detail(identity)["status"] == "processed", "409 did not mean already processed")
    def complete():
        states = {identity: run.detail(identity) for identity in sorted(run.replay_ids)}
        return states if all(value and value["status"] == "processed" for value in states.values()) else None
    states = run.wait("all selected schema messages recovered", complete, run.config.recovery_seconds)
    final = run.hazard(first["report_id"])
    require(final and final["severity"] == "AWAS" and final["attributes"].get("eruption_count_24h") == 4,
            "final same-key state does not match the second version")
    for ref in refs:
        require(run.hazard(ref), "replayed reference missing from canonical store")
    missing = run.hazard(run.name + "-missing")
    require(missing and "eruption_count_24h" not in missing["attributes"], "v2 invented a value for missing count")
    require(all(not item.get("raw_base64") for item in states.values()), "processed raw was not compacted as documented")
    for identity in sorted(run.replay_ids):
        run.http("client", "/ops/unprocessed/" + identity + "/replay", token=run.tokens["analyst"], method="POST", expected=(409,))
    require(run.hazard(first["report_id"]) == final, "repeated replay changed canonical state")
    run.save("replay-recovered.json", {"messages": states, "final_ordered_event": final})
    return {"recovered_messages": len(states), "canonical_refs": len(refs), "duplicate_canonical_rows": 0,
            "ordering": "second version blocked while first failed; final state matches second; bounded scenario proof"}


def run_group(run):
    run.prompt("Group 2: attach a temporary upstream using the existing Citus image's Python; test four poison payloads, "
               "then v2 replay. One synthetic hazard row is locked briefly to force a retryable SQL timeout.")
    run.create_fixture()
    run.stage("P3.3-poison-isolation", poison, run)
    run.stage("P3.4-prepare-ordered-replay", prepare_replay, run)
    run.stage("P3.2-and-P3.4-retry-and-replay", replay, run)
