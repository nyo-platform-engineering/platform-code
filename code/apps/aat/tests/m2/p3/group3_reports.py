"""P3.6: durable field reports across receiver crashes."""
from concurrent.futures import ThreadPoolExecutor
import datetime as dt
import hashlib

from fixtures import changed_report
from support import require, sql_literal


def receipt_id(key):
    return "fr-" + hashlib.sha256(("responder\0" + key).encode()).hexdigest()


def submit(run, key, body, expected=(202,)):
    status, value, elapsed = run.http("client", "/field-reports", token=run.tokens["responder"], body=body,
                                      headers={"Idempotency-Key": key}, expected=expected)
    if status == 202:
        require(value and value.get("report_id") == receipt_id(key) and value.get("status") == "accepted"
                and value.get("accepted_at"), "invalid acceptance receipt")
        require(elapsed <= run.config.max_accept_ms, f"202 took {elapsed:.0f}ms, exceeds configured {run.config.max_accept_ms}ms")
    return value, elapsed


def counts(run, identities):
    values = ",".join(sql_literal(identity) for identity in identities)
    return run.sql("SELECT json_build_object('submissions',(SELECT count(*) FROM field_report_submissions WHERE id IN (" + values + ")),"
                   "'pending',(SELECT count(*) FROM field_report_submissions WHERE processed_at IS NULL AND id IN (" + values + ")),"
                   "'results',(SELECT count(*) FROM field_reports WHERE report_id IN (" + values + "))); ")


def check(run):
    run.set_env(FIELD_REPORT_PROCESSOR_ENABLED="false")
    receipts, bodies, latencies = {}, {}, []
    observed = dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    base = {"area_name": "P3 test area", "description": "Durable field report", "observed_at": observed}
    for role in ("analyst", "public"):
        run.http("client", "/field-reports", token=run.tokens[role], body=base,
                 headers={"Idempotency-Key": run.name + "-forbidden"}, expected=(403,))
    for i in range(run.config.report_count):
        key = f"{run.name}-report-{i}"
        body = dict(base, description=f"{run.name}: report {i}")
        receipt, elapsed = submit(run, key, body)
        receipts[key], bodies[key] = receipt, body
        latencies.append(elapsed)
        run.status(f"durable receipts accepted={i + 1}/{run.config.report_count}; last 202={elapsed:.0f}ms")
        # Persist each receipt before crashing receivers.
        run.save("accepted-receipts.json", {"receipts": receipts, "latencies_ms": latencies})
    key = next(iter(receipts))
    with ThreadPoolExecutor(max_workers=run.config.duplicate_requests) as pool:
        repeated = list(pool.map(lambda _: submit(run, key, bodies[key])[0], range(run.config.duplicate_requests)))
    require(all(value == receipts[key] for value in repeated), "same-key duplicate returned a different receipt")
    submit(run, key, changed_report(bodies[key]), expected=(409,))
    identities = [value["report_id"] for value in receipts.values()]
    expected_pending = {"submissions": len(identities), "pending": len(identities), "results": 0}
    require(counts(run, identities) == expected_pending, "reports were not durably queued with processing disabled")
    for identity in identities:
        require(run.ops("field-reports/" + identity)["status"] == "accepted", "pending Ops state differs")
    old = {p["metadata"]["uid"] for p in run.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"}
    require(old, "no receiver pods to crash")
    with run.phase("force-deleting all Aggregator receiver pods after 202", 60):
        run.cluster.kubectl("delete", "pod", "-l", "role=aggregator", "--grace-period=0", "--force", "--wait=false")
    run.settled()
    current = {p["metadata"]["uid"] for p in run.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"}
    require(current and not old & current, "not every possible receiving pod was replaced")
    require(counts(run, identities) == expected_pending, "202 report lost or processed while worker remained disabled")
    run.save("reports-after-crash.json", {"old_pods": sorted(old), "new_pods": sorted(current), "counts": expected_pending})
    run.set_env(FIELD_REPORT_PROCESSOR_ENABLED="true")
    def complete():
        state = counts(run, identities)
        run.status(f"accepted={state['submissions']}; pending={state['pending']}; results={state['results']}/{len(identities)}")
        return state if state == {"submissions": len(identities), "pending": 0, "results": len(identities)} else None
    run.wait("all accepted reports processed once", complete, run.config.recovery_seconds)
    processed = {}
    for key, receipt in receipts.items():
        view = run.ops("field-reports/" + receipt["report_id"])
        require(view.get("status") == "processed" and view.get("report") == bodies[key], "processed report document differs")
        require(view["accepted_at"] == receipt["accepted_at"] and view.get("processed_at"), "receipt/result identity changed")
        processed[key] = view
        require(submit(run, key, bodies[key])[0] == receipt, "retry after crash returned a different receipt")
    run.wait("duplicate retries drained", complete, run.config.recovery_seconds)
    for key, receipt in receipts.items():
        require(run.ops("field-reports/" + receipt["report_id"]) == processed[key], "duplicate retry reprocessed a report")
    run.save("processed-reports.json", processed)
    return {"accepted": len(identities), "processed": len(identities), "duplicate_results": 0,
            "max_accept_ms": round(max(latencies), 2), "configured_accept_limit_ms": run.config.max_accept_ms,
            "scope": "durable database processing; no claim of exactly-once external notification"}


def run_group(run):
    run.prompt("Group 3: disable every field-report processor, submit reports and duplicate keys, then FORCE DELETE ALL "
               "Aggregator pods after 202. Verify pending receipts survive before re-enabling processing.")
    run.stage("P3.6-durable-field-reports", check, run)
