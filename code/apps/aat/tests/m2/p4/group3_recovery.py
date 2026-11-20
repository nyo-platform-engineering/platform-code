"""Group 3 — crash and reconnect: criteria 5 (a processor crash leaves the same
result, nothing lost or doubled) and 8 (resume from the right position)."""
from common import output, require


def offset(run):
    return run.aggregator("/internal/satellite/stats")["feed"]["offset"]


def aggregator_pods(run):
    return run.cluster.kubectl("get", "pods", "-l", "app.kubernetes.io/instance=aggregator", "-o", "json",
                               json_output=True)["items"]


def replacements_ready(run, old):
    pods = aggregator_pods(run)
    if any(p["metadata"]["uid"] in old for p in pods):
        return None
    ready = [p for p in pods if not p["metadata"].get("deletionTimestamp") and any(
        c["type"] == "Ready" and c["status"] == "True" for c in p.get("status", {}).get("conditions", []))]
    want = run.cluster.kubectl("get", "deployment", "aggregator", "-o", "json", json_output=True)["spec"]["replicas"]
    return len(ready) if len(ready) >= want else None


def restart(run):
    before = offset(run)
    old = {p["metadata"]["uid"] for p in aggregator_pods(run)}
    refs_before = getattr(run, "fire_refs", None) or {h["source_ref_id"] for h in run.hazards("SATELIT")}
    run.cluster.kubectl("delete", "pod", "-l", "app.kubernetes.io/instance=aggregator", "--wait=false")
    output("INFO", f"Deleted every Aggregator pod at feed offset {before}")
    run.forwards.close("aggregator")  # its pod is gone; reopen through the Service
    # Deleted pods keep serving while they terminate, and a port-forward binds one pod:
    # reopen it only when every old pod is gone and the replacements are Ready.
    replaced = run.wait("the old Aggregator pods to be gone and the new ones Ready",
                        lambda: replacements_ready(run, old), run.config.resume_timeout)
    output("INFO", f"{replaced} new Aggregator pods Ready; old pods gone")

    def resumed():
        run.forwards.open("aggregator")
        value = offset(run)
        return value if value is not None and value > before else None
    after = run.wait("the feed to resume past the pre-crash offset", resumed, run.config.resume_timeout)
    fires = run.hazards("SATELIT")
    refs = [h["source_ref_id"] for h in fires]
    require(len(refs) == len(set(refs)), "duplicate fires after the restart")
    missing = refs_before - set(refs)
    require(not missing, f"{len(missing)} fires disappeared after the restart")
    stats = run.aggregator("/internal/satellite/stats")
    require(stats["feed"]["rejected_events"] == 0, "events were rejected after the restart")
    return {"offset_before_kill": before, "offset_after_resume": after, "fires_before": len(refs_before),
            "fires_after": len(refs), "duplicates": 0, "lost": 0}


def run_group(run):
    if not run.config.restart_aggregators:
        run.skip("resume-after-killing-all-aggregators", "CONFIG.restart_aggregators is False")
        return
    input("Enter → delete every Aggregator pod mid-stream and verify the exact resume: ")
    result = run.stage("resume-after-killing-all-aggregators", restart, run)
    output("RESULT", "Resume", f"Offset {result['offset_before_kill']} → {result['offset_after_resume']}",
           f"Fires {result['fires_before']} → {result['fires_after']}; none lost or duplicated")
