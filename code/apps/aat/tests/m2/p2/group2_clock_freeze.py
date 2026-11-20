"""Criteria 4–5: measured clock offsets and real worker freeze/resume."""
import json
import shlex
import subprocess
import time

from helper import INFRA, NAMESPACE, output, ready, require


def vm_python(run, source):
    """Run only on the selected local VM, never the generator/shared host clock."""
    require(not run.container_nodes, "clock injection requires a separate-kernel VM")
    require(run.config.worker in ("aat-worker-1", "aat-worker-2"), "invalid clock worker")
    require(run.config.kubeconfig.resolve() == (INFRA / ".state/kubeconfig").resolve(),
            "automatic clock injection only supports the local Vagrant lab")
    identity = INFRA / "local/.vagrant/machines" / run.config.worker / "libvirt/id"
    require(identity.is_file(), "local Vagrant identity missing for clock injection")
    with run.phase("VM clock command: " + run.config.worker, 60):
        result = subprocess.run(["vagrant", "ssh", run.config.worker, "--no-tty", "-c",
                                 "sudo -n python3 -c " + shlex.quote(source)],
                                cwd=INFRA / "local", capture_output=True, text=True, timeout=60)
    require(result.returncode == 0, "VM clock command failed; inspect worker SSH, sudo and time services")
    return json.loads(result.stdout)


def shift_clock(run, target_offset):
    # Measure through SSH so cleanup also works if the Aggregator pod disappears.
    before = time.time()
    remote = vm_python(run, "import json,time; print(json.dumps(time.time()))")
    after = time.time()
    require(after - before <= 5, "VM clock measurement RTT exceeds 5 seconds")
    correction = target_offset - (remote - (before + after) / 2)
    vm_python(run, "import json,time; "
              f"time.clock_settime(time.CLOCK_REALTIME, time.time() + {correction!r}); "
              "print(json.dumps({'clock_set': True}))")


def restore_clock(run, services):
    shift_clock(run, 0)
    vm_python(run, "import json,subprocess; "
              f"services={services!r}; "
              "[subprocess.run(['systemctl','start',s],check=True) for s in services]; "
              "subprocess.run(['chronyc','makestep'],check=True,stdout=subprocess.DEVNULL) "
              "if 'chrony.service' in services else None; "
              "subprocess.run(['chronyc','waitsync','30','0.1','0','1'],check=True,stdout=subprocess.DEVNULL) "
              "if 'chrony.service' in services else None; "
              "assert all(subprocess.run(['systemctl','is-active','--quiet',s]).returncode==0 for s in services); "
              "print(json.dumps({'services_restored':services}))")
    before = time.time()
    remote = vm_python(run, "import json,time; print(json.dumps(time.time()))")
    after = time.time()
    require(after - before <= 5 and abs(remote - (before + after) / 2) <= 5,
            "worker clock was not restored")
    run.save("clock-recovery.json", {"worker": run.config.worker, "services": services, "restored": True})
    output("RESULT", f"P2.4 — clock restored on {run.config.worker}; time services running")
    return {"offset_seconds": remote - (before + after) / 2, "services_restored": services}


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
    run.prompt(f"P2.4: prepare the actual {run.config.source} owner on {run.config.worker} before the clock test. Owner pods may be restarted.")
    owner, moves = prepare_owner(run)
    pod = worker_pod(run)
    require(pod == owner["pod"], "clock target is not the observed source owner")
    # Keep the mock clock and the database authority out of this worker-only test.
    require(not any(p["metadata"]["name"].startswith(("bmkg-", "pvmbg-", "citus-g0-"))
                    and p["spec"].get("nodeName") == run.config.worker for p in run.pods()),
            "choose a worker without BMKG/PVMBG/coordinator for this clock-isolation scenario")
    baseline = clock_offset(run, pod)
    require(abs(baseline["offset_seconds"]) <= 5, "synchronize worker/generator clocks before testing")
    services = vm_python(run, "import json,subprocess; "
        "services=['chrony.service','systemd-timesyncd.service','ntp.service','ntpsec.service']; "
        "print(json.dumps([s for s in services if subprocess.run("
        "['systemctl','is-active','--quiet',s]).returncode==0]))")
    require(services, "no active worker time service; run task clock:sync before testing")
    run.prompt(f"P2.4: automatically stop time sync on {run.config.worker}, apply clock offsets {run.config.clock_offsets}, check polling, then restore time sync.")
    results = []
    restore = lambda: restore_clock(run, services)
    run.save("clock-recovery.json", {"worker": run.config.worker, "services": services, "restored": False})
    run.cleanup.append(restore)
    try:
        vm_python(run, "import json,subprocess; "
                  f"services={services!r}; "
                  "[subprocess.run(['systemctl','stop',s],check=True) for s in services]; "
                  "print(json.dumps({'services_stopped':services}))")
        for offset in run.config.clock_offsets:
            shift_clock(run, baseline["offset_seconds"] + offset)
            measured = clock_offset(run, pod)
            delta = measured["offset_seconds"] - baseline["offset_seconds"]
            require(abs(delta - offset) <= 5, f"clock injection failed: measured {delta:+.1f}s, requested {offset:+g}s")
            output("RESULT", f"P2.4 — {run.config.worker}\n"
                   f"    Requested skew: {offset:+g}s; measured change: {delta:+.1f}s; time sync stopped")
            polling = run.observe()
            results.append({"requested_offset": offset, "measurement": measured, "polling": polling})
            run.save("clock-skew-observations.json", results)
            output("RESULT", "P2.4 — no competing successful polling owners observed; Audit reconciliation passed")
    finally:
        restored = restore()
        run.cleanup.remove(restore)
    return {"owner_preparation": moves, "owner_pod": pod,
            "baseline": baseline, "offsets": results, "clock_restored": restored,
            "stale_write_fencing": "log observations only; retain official fault/storage evidence"}


def prepare_owner(run):
    """Place the real source owner on the safe worker before a worker fault."""
    deadline = time.monotonic() + run.config.recovery_seconds
    moves = []
    while True:
        old = run.owner(run.config.source)
        if old["node"] == run.config.worker:
            return old, moves
        require(time.monotonic() < deadline, "could not place source ownership on the safe worker")
        epoch = int(old["log"]["poll_epoch"])
        started = time.monotonic()
        run.status(f"preparing owner on {run.config.worker}; current={old['node']}; epoch={epoch}")
        run.kube("delete", "pod", old["pod"], "-n", NAMESPACE,
                 "--grace-period=0", "--force", "--wait=false", json_output=False)
        def successor():
            return next((r for r in run.logs.snapshot() if r["observed"] >= started and not r.get("initial")
                         and r["log"].get("msg") == "poll succeeded"
                         and r["log"].get("source") == run.config.source
                         and r["pod_uid"] != old["pod_uid"]
                         and int(r["log"].get("epoch", 0)) > epoch), False)
        run.wait(successor, timeout=min(run.config.takeover_seconds, max(1, deadline-time.monotonic())),
                 description="preparing actual polling owner on safe worker")
        moves.append({"deleted_owner": old["pod"], "old_epoch": epoch})
        run.save("worker-owner-preparation.json", moves)
        run.replicas(3, timeout=max(1, deadline-time.monotonic()))


def freeze(run):
    require(run.config.kubeconfig.resolve() == (INFRA / ".state/kubeconfig").resolve(), "automatic freeze only supports the local Vagrant lab")
    identity = INFRA / "local/.vagrant/machines" / run.config.worker / "libvirt/id"
    require(identity.is_file(), "local Vagrant identity missing; use the official external freeze procedure")
    run.prompt(f"P2.5: prepare the actual {run.config.source} owner on {run.config.worker}, then suspend that worker. Owner pods may be restarted during preparation.")
    old, moves = prepare_owner(run)
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
    result.update(owner_preparation=moves, old_epoch=epoch, takeover_epoch=new["log"]["epoch"], audit=run.checkpoint("freeze", wall, time.time()),
                  limitation="No stale successful poll observed; this does not inject a pause exactly inside a storage commit.")
    return result


def run_group(run):
    require(not run.container_nodes, "clock/freeze checks require separate-kernel VM nodes")
    run.stage("P2.4-clock-skew", lambda: clock(run))
    run.stage("P2.5-freeze-resume", lambda: freeze(run))
