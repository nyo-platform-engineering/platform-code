"""P3.1, 2 and 5: schema policy, raw retention and Ops access."""
import time
import urllib.parse

from fixtures import schema_mode
from support import require, utc


def schema_case(run, mode):
    before = {row["message_id"] for row in run.unprocessed()}
    since = utc()
    state = run.mock_mode(mode)
    require(state.get("schema_mode") == ("original" if mode == "additive" else mode), "mock ignored schema mode")
    run.save("mock-" + mode + ".json", state)
    def fresh():
        _, rows, _ = run.http("pvmbg", "/volcanic-reports?" + urllib.parse.urlencode({"since": since}),
                               token=run.env["PVMBG_TOKEN"])
        return rows[-1] if isinstance(rows, list) and rows else None
    sample = run.wait("fresh mock event in " + mode, fresh, run.config.recovery_seconds)
    require(schema_mode(sample) == mode, "mock payload does not match selected mode")
    ref = sample["report_id"]
    if mode in ("original", "additive"):
        event = run.wait("canonical " + ref, lambda: run.hazard(ref), run.config.recovery_seconds)
        if mode == "additive":
            require(event["attributes"].get("confidence_level") == 0.85, "optional attribute not preserved")
        return {"mode": mode, "policy": "accepted automatically", "source_ref_id": ref}
    def parked():
        for row in run.unprocessed():
            if row["message_id"] in before or row.get("status") != "parked":
                continue
            require(row.get("retryable") is False, "parked metadata incorrectly declares automatic retry")
            detail = run.detail(row["message_id"])
            if detail and detail.get("source_ref_id") == ref:
                return detail
        return None
    detail = run.wait("parked " + mode, parked, run.config.recovery_seconds)
    import base64
    import json
    raw = base64.b64decode(detail["raw_base64"], validate=True)
    require(schema_mode(json.loads(raw)) == mode, "wrong parked schema")
    require(json.loads(raw) == sample, "captured record differs from deterministic upstream event")
    from support import validate_raw
    validate_raw(detail, raw, "record")
    require(detail["reason"].startswith("schema_incompatible:"), "schema failure is not classified")
    require(run.hazard(ref) is None, "strict mapper accepted an incompatible fresh event")
    run.save("mock-inbox-" + mode + ".json", detail)
    run.replay_ids.add(detail["message_id"])
    attempts = detail["attempts"]
    with run.phase("confirming parked message is not automatically retried", run.config.park_observe_seconds):
        time.sleep(run.config.park_observe_seconds)
    current = run.detail(detail["message_id"])
    require(current["status"] == "parked" and current["attempts"] == attempts, "parked message automatically retried")
    return {"mode": mode, "policy": "parked until explicit v2 policy/replay", "message_id": detail["message_id"],
            "raw_bytes": len(raw), "source_ref_id": ref, "attempts_unchanged": True}


def access(run):
    require(run.replay_ids, "run schema checks first to obtain a failed message")
    identity = sorted(run.replay_ids)[0]
    paths = ("/ops/unprocessed", "/ops/unprocessed/" + identity, "/ops/unprocessed/" + identity + "/replay")
    for role in ("public", "responder"):
        for path in paths:
            run.http("client", path, token=run.tokens[role], method="POST" if path.endswith("/replay") else "GET", expected=(403,))
    for path in paths:
        run.http("client", path, method="POST" if path.endswith("/replay") else "GET", expected=(401,))
    run.ops("unprocessed/" + identity)
    for token in (None, run.env["AGGREGATOR_TOKEN"], run.tokens["analyst"]):
        run.http("aggregator", "/internal/audit/unprocessed?source=PVMBG", token=token, expected=(401,))
    _, page, _ = run.http("aggregator", "/internal/audit/unprocessed?source=PVMBG",
                           token=run.env["AUDIT_TOKEN"])
    require(isinstance(page.get("items"), list), "invalid Audit unprocessed response")
    return {"public_roles": "analyst allowed; media/responder forbidden; anonymous unauthorized",
            "audit": "separate trusted Audit credential; no raw detail/replay exposed through Audit"}


def run_group(run):
    run.prompt("Group 1: change the real PVMBG mock through five schema modes; check strict policy and Ops access.")
    run.set_env(PVMBG_SCHEMA_POLICY="strict", PVMBG_URL="http://pvmbg:8082")
    for mode in ("original", "additive", "rename_field", "type_change", "missing_required"):
        run.stage("P3.1-schema-" + mode, schema_case, run, mode)
    run.stage("P3.5-ops-access", access, run)
    run.mock_mode("original")
