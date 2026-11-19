"""Criteria 1–3: placement, real load balancing, upstream ownership and takeover."""
import time

from helper import NAMESPACE, ready, require


def topology(run):
    pods = [p for p in run.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"
            and not p["metadata"].get("deletionTimestamp")]
    require(len(pods) >= 3 and all(ready(p) for p in pods), "need >=3 Ready Aggregators")
    require(len({p["spec"]["nodeName"] for p in pods}) >= 3, "Aggregator replicas must span three nodes")
    slices = run.kube("get", "endpointslices", "-n", NAMESPACE, "-l", "kubernetes.io/service-name=aggregator", "-o", "json")
    routed = {e.get("targetRef", {}).get("uid") for s in slices["items"] for e in s["endpoints"]
              if e.get("conditions", {}).get("ready") is True}
    require({p["metadata"]["uid"] for p in pods} <= routed, "Ready replicas missing from Service endpoints")
    before = dict(run.logs.traffic)
    for _ in range(60):
        page = run.request("/internal/hazards", params={"limit": 1})
        require(isinstance(page.get("items"), list), "invalid routed response")
    run.wait(lambda: len([uid for uid, count in run.logs.traffic.items() if count > before.get(uid, 0)]) >= 3, timeout=15)
    return {"pods": [{"name": p["metadata"]["name"], "uid": p["metadata"]["uid"], "node": p["spec"]["nodeName"]} for p in pods],
            "three_backends_observed": True, "ready_endpoints": sorted(routed)}


def takeover(run):
    old = run.owner(run.config.source)
    old_epoch = int(old["log"]["poll_epoch"])
    run.prompt(f"P2.3: delete active {run.config.source} owner pod {old['pod']} (epoch {old_epoch}); no node/data erasure.")
    run.audit()
    wall, started = time.time(), time.monotonic()
    run.kube("delete", "pod", old["pod"], "-n", NAMESPACE, "--grace-period=0", "--force", "--wait=false", json_output=False)

    def new_success():
        matches = [r for r in run.logs.snapshot() if r["observed"] >= started and not r.get("initial")
                   and r["log"].get("msg") == "poll succeeded" and r["log"].get("source") == run.config.source
                   and int(r["log"].get("epoch", 0)) > old_epoch and r["pod_uid"] != old["pod_uid"]]
        return min(matches, key=lambda r: r["observed"]) if matches else False

    success = run.wait(new_success, timeout=run.config.takeover_seconds)
    elapsed = success["observed"] - started
    require(elapsed <= 60, "takeover exceeded 60 seconds")
    def removed():
        slices = run.kube("get", "endpointslices", "-n", NAMESPACE, "-l", "kubernetes.io/service-name=aggregator", "-o", "json")
        return not any(e.get("targetRef", {}).get("uid") == old["pod_uid"] and e.get("conditions", {}).get("ready") is True
                       for s in slices["items"] for e in s.get("endpoints", []))
    run.wait(removed, timeout=30)
    for _ in range(10):
        run.request("/internal/hazards", params={"limit": 1})
    run.replicas(3)
    time.sleep(max(0, 3 * max(run.config.event_intervals.values()) - (time.time() - wall)))
    audit = run.checkpoint("takeover", wall, time.time())
    return {"old_pod": old["pod"], "old_epoch": old_epoch, "new_pod": success["pod"],
            "new_epoch": success["log"]["epoch"], "takeover_seconds_upper_bound": elapsed,
            "old_ready_endpoint_removed": True, "audit": audit}


def run_group(run):
    run.prompt("Group 1 — placement, upstream polling, and active-instance failover.")
    run.stage("P2.1-placement-and-routing", lambda: topology(run))
    run.stage("P2.2-upstream-and-logical-dedup", run.observe)
    run.stage("P2.3-owner-failover", lambda: takeover(run))
