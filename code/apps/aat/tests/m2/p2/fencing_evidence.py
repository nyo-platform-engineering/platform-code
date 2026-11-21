"""Commit and stale-session evidence for P2.4–5."""
from collections import Counter

from common import parse_time, require


def storage_evidence(records, start, end):
    commits = [r for r in records if not r.get("initial") and start <= r["observed"] <= end
               and r["log"].get("msg") == "poll storage committed"
               and r["log"].get("source") in ("BMKG", "PVMBG")]
    owners, transactions, counts = {}, set(), Counter()
    for record in commits:
        log = record["log"]
        require(log.get("operation") and log.get("owner") and log.get("backend_pid", 0) > 0
                and int(log.get("epoch", 0)) > 0 and str(log.get("transaction_id", "")).isdigit(),
                "storage commit lacks fence/transaction identity; deploy the latest Aggregator")
        require(parse_time(log["db_observed_at"]).utcoffset() is not None,
                "storage evidence needs a timezone-qualified database timestamp")
        identity = (log["source"], int(log["epoch"]))
        authority = (log["owner"], log["backend_pid"], record["pod_uid"])
        require(identity not in owners or owners[identity] == authority,
                "one source epoch has storage commits from different sessions/instances")
        owners[identity] = authority
        transaction = (log["source"], log["transaction_id"])
        require(transaction not in transactions, "storage transaction logged more than once")
        transactions.add(transaction)
        counts[log["source"]] += 1
    latest = {}
    for record in sorted(commits, key=lambda r: parse_time(r["log"]["db_observed_at"])):
        log = record["log"]
        source, epoch = log["source"], int(log["epoch"])
        require(epoch >= latest.get(source, epoch), "old epoch stored after a newer epoch's database fence check")
        latest[source] = epoch
    require(all(counts[source] > 0 for source in ("BMKG", "PVMBG")),
            "need successful storage commits for BMKG and PVMBG; deploy the latest Aggregator and check polling")
    return {"commits_per_source": dict(counts), "owners": [
        {"source": source, "epoch": epoch, "owner": owner, "backend_pid": pid, "pod_uid": uid}
        for (source, epoch), (owner, pid, uid) in sorted(owners.items())],
        "commits": [{"pod_uid": r["pod_uid"], "node": r["node"], **r["log"]} for r in commits],
        "scope": "acknowledged commits; db_observed_at is the locked fence-check time, not the commit timestamp"}


def freeze_evidence(records, old, source, old_epoch, new_epoch, resumed):
    old_uid = old["pod_uid"]
    commits = [r for r in records if r["log"].get("msg") == "poll storage committed"
               and r["log"].get("source") == source]
    successor = [r for r in commits if r["pod_uid"] != old_uid and int(r["log"]["epoch"]) == new_epoch]
    require(successor, "takeover has no storage commit evidence")
    boundary = min(parse_time(r["log"]["db_observed_at"]) for r in successor)
    stale = [r for r in commits if r["pod_uid"] == old_uid and int(r["log"]["epoch"]) <= old_epoch
             and parse_time(r["log"]["db_observed_at"]) >= boundary]
    require(not stale, "old session stored after the successor's database fence check")
    rejections = [r for r in records if r["observed"] >= resumed and r["pod_uid"] == old_uid
                  and r["log"].get("msg") == "poll storage rejected"
                  and r["log"].get("source") == source and int(r["log"].get("epoch", 0)) == old_epoch
                  and r["log"].get("reason") in ("session_unavailable", "fence_mismatch")]
    require(rejections, "resumed owner has no old-session rejection evidence; check container restarts and log continuity")
    acquired, renewed_commits = set(), []
    for record in records:
        if record["pod_uid"] != old_uid or record["log"].get("source") != source:
            continue
        log = record["log"]
        identity = (int(log.get("epoch", 0)), log.get("owner"), log.get("backend_pid"))
        if log.get("msg") == "poll ownership acquired":
            acquired.add(identity)
        if log.get("msg") == "poll storage committed" and parse_time(log["db_observed_at"]) >= boundary:
            require(identity[0] > old_epoch and identity in acquired,
                    "resumed instance stored without a matching fresh ownership acquisition")
            renewed_commits.append(log)
    return {"old_epoch": old_epoch, "takeover_epoch": new_epoch,
            "successor_db_fence_time": boundary.isoformat(),
            "old_session_rejections": [r["log"] for r in rejections],
            "commits_after_fresh_acquisition": renewed_commits,
            "scope": "observed post-commit logs and old-session rejection; retained pre-freeze logs are compared by database time"}
