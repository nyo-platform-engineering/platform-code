"""Criteria 4–5: measured clock offsets and real worker freeze/resume."""
import subprocess
import time

from helper import INFRA, NAMESPACE, ready, require


def worker_pod(run):
    pods = [p for p in run.pods() if p["metadata"].get("labels", {}).get("role") == "aggregator"
            and p["spec"].get("nodeName") == run.config.worker and not p["metadata"].get("deletionTimestamp")]
    require(len(pods) == 1, "need exactly one Aggregator on configured worker")
    return pods[0]["metadata"]["name"]


def clock_offset(run, pod):
    before = time.time()
    raw = run.kube("exec", pod, "-n", NAMESPACE, "--", "date", "+%s", json_output=False)
    after = time.time()
    require(after - before <= 3, "clock measurement RTT exceeds 3 seconds")
    return {"offset_seconds": int(raw.strip()) - (before + after) / 2,
            "rtt_seconds": after - before}


def clock(run):
    pod = worker_pod(run)
    # Keep the mock clock and the database authority out of this worker-only test.
    require(not any(p["metadata"]["name"].startswith(("bmkg-", "pvmbg-", "citus-g0-"))
                    and p["spec"].get("nodeName") == run.config.worker for p in run.pods()),
            "choose a worker without BMKG/PVMBG/coordinator for this clock-isolation scenario")
    baseline = clock_offset(run, pod)
    results = []
    try:
        for offset in (45, -30):
            run.prompt(f"P2.4: externally stop time synchronization on {run.config.worker} and set its clock to baseline {offset:+d}s. Save operator/Tester Agent commands, then continue.")
            measured = clock_offset(run, pod)
            delta = measured["offset_seconds"] - baseline["offset_seconds"]
            require(abs(delta - offset) <= 5, f"measured clock change {delta:.1f}s does not match {offset:+d}s")
            results.append({"requested_offset": offset, "measurement": measured, "polling": run.observe()})
    finally:
        run.prompt(f"Restore clock synchronization and correct time on {run.config.worker}, including after a failed check. Continue only after restoration.")
        restored = clock_offset(run, pod)
        require(abs(restored["offset_seconds"] - baseline["offset_seconds"]) <= 5, "worker clock was not restored")
    return {"baseline": baseline, "offsets": results, "clock_restored": restored,
            "stale_write_fencing": "log observations only; retain official fault/storage evidence"}


def freeze(run):
    require(run.config.kubeconfig.resolve() == (INFRA / ".state/kubeconfig").resolve(), "automatic freeze only supports the local Vagrant lab")
    identity = INFRA / "local/.vagrant/machines" / run.config.worker / "libvirt/id"
    require(identity.is_file(), "local Vagrant identity missing; use the official external freeze procedure")
    old = run.owner(run.config.source)
    require(old["node"] == run.config.worker, "configured source owner is not on worker; select its actual worker/source in CONFIG")
    epoch = int(old["log"]["poll_epoch"])
    run.prompt(f"P2.5: suspend {run.config.worker} for {run.config.freeze_seconds}s, then resume it. This freezes every workload on that worker.")
    started, wall = time.monotonic(), time.time()
    def action(command):
        with run.phase(command + ": " + run.config.worker, 90):
            subprocess.run(["bash", "scripts/vm/main.sh", command, run.config.worker], cwd=INFRA, check=True, timeout=90)
    run.cleanup.append(lambda: action("resume-worker"))
    try:
        action("suspend-worker")
        run.pause(run.config.freeze_seconds, "holding worker frozen")
        new = run.wait(lambda: next((r for r in run.logs.snapshot() if r["observed"] >= started
                           and r["node"] != run.config.worker and r["log"].get("msg") == "poll succeeded"
                           and r["log"].get("source") == run.config.source
                           and int(r["log"].get("epoch", 0)) > epoch), False), timeout=run.config.takeover_seconds,
                       description="successful takeover while worker is frozen")
    finally:
        action("resume-worker")
        run.cleanup.pop()
    resumed = time.monotonic()
    run.wait(lambda: any(p["metadata"]["name"] == old["pod"] and ready(p) for p in run.pods()),
             description="original pod Ready after worker resume")
    run.checkpoint("freeze-recovered")
    result = run.observe()
    stale = [r for r in run.logs.snapshot() if r["observed"] >= resumed and r["pod_uid"] == old["pod_uid"]
             and r["log"].get("msg") == "poll succeeded" and r["log"].get("source") == run.config.source
             and int(r["log"].get("epoch", 0)) <= epoch]
    require(not stale, "resumed instance logged a successful poll using the old epoch")
    result.update(old_epoch=epoch, takeover_epoch=new["log"]["epoch"], audit=run.checkpoint("freeze", wall, time.time()),
                  limitation="No stale successful poll observed; this does not inject a pause exactly inside a storage commit.")
    return result


def run_group(run):
    if run.container_nodes:
        for name in ("P2.4-clock-skew", "P2.5-freeze-resume"):
            run.skip(name, "k3d shares a kernel; real clock/VM freeze evidence requires the VM/native demo cluster")
        return
    run.stage("P2.4-clock-skew", lambda: clock(run))
    run.stage("P2.5-freeze-resume", lambda: freeze(run))
