"""Group 2 — bounded availability, worker loss, and recovery (criteria 4–6)."""
# 4. Bila satu node worker dimatikan dan seluruh datanya dihapus, tunjukkan bahwa tidak ada HazardEvent yang hilang (diverifikasi melalui Audit API terhadap checksum asisten). Selama sistem memulihkan diri, setiap permintaan tetap dijawab: lengkap bila datanya tersedia, atau sebagian dengan penanda eksplisit bahwa jawabannya tidak lengkap, bukan error 500 dan bukan permintaan yang menggantung.
# 5. Tambahkan node baru (atau node yang sama dengan disk kosong). Tunjukkan node tersebut ikut menanggung data dan beban tanpa menghentikan penulisan maupun pembacaan.
# 6. Setelah kriteria 4 dan 5, sistem kembali pada kondisi yang mampu bertahan dari kehilangan satu node berikutnya.
from helper.monitor import Monitor
from helper.data import export, verify_preservation
from helper.cluster import (
    restore_worker_on_failure, run_worker_action, storage_plan, wait_for_node_state,
    wait_for_recovery, worker_storage,
)
from common import output, require


# 4. Bila satu node worker dimatikan dan seluruh datanya dihapus, tunjukkan bahwa tidak ada HazardEvent yang hilang (diverifikasi melalui Audit API terhadap checksum asisten). Selama sistem memulihkan diri, setiap permintaan tetap dijawab: lengkap bila datanya tersedia, atau sebagian dengan penanda eksplisit bahwa jawabannya tidak lengkap, bukan error 500 dan bukan permintaan yang menggantung.
def lose_worker(run, name, worker):
    print(f"\n{name}: target {worker}.", flush=True)
    automatic = run.config.automatic_worker_actions
    proof = None

    if automatic:
        # Resolve and record the exact store volumes before erasing anything.
        plan = storage_plan(run, worker)
        run.storage_plans[worker] = plan
        run.save(f"{name}-storage-plan.json", plan)

        print("The script will stop database processes, erase these AAT store volumes, "
              "and cut worker power. OS and other services' volumes are retained.", flush=True)
        for volume in plan["volumes"]:
            print("  " + volume["path"], flush=True)
        input(f"Enter → erase {worker}'s store data and simulate worker loss: ")
    else:
        print("Use the official Tester Agent to stop/erase the worker.", flush=True)

    # Keep read/write probes running through the action and recovery checks.
    # If the automatic check fails, restart the worker before propagating it.
    with restore_worker_on_failure(run, name, worker):
        with Monitor(run, name, writes=True, target=worker, expect_ready=False) as monitor:
            if automatic:
                proof = worker_storage(run, name, plan, "erase")

                for volume in proof["volumes"]:
                    require(
                        volume["files_before"] > 0 and volume["entries_after"] == 0,
                        "storage erasure did not empty previously populated volumes",
                    )

                output("RESULT", "Storage erasure",
                       f"Volumes erased: {len(proof['volumes'])}", "All verified empty")
                run_worker_action(run, name, worker, rejoin=False)
            else:
                input(f"After performing the action for {worker}, press Enter to verify: ")

            # Verify survival on the remaining nodes; full redundancy comes later.
            wait_for_node_state(run, monitor, worker, ready=False)
            result = wait_for_recovery(run, name, redundant=False)

        # Include writes acknowledged right up to the moment the monitor stopped.
        final_events = export(
            run.api,
            page_size=run.config.page_size,
            max_pages=run.config.max_pages,
        )
        verify_preservation(run, final_events, set(run.acknowledged_writes))
        run.previous_events = final_events

    result.update(
        worker=worker,
        power_action_automatic=automatic,
        disk_erasure_verified=proof is not None,
        storage_loss=proof,
        monitor=monitor.summary(),
        acknowledged_writes_preserved=len(run.acknowledged_writes),
    )
    return result


# 5. Tambahkan node baru (atau node yang sama dengan disk kosong). Tunjukkan node tersebut ikut menanggung data dan beban tanpa menghentikan penulisan maupun pembacaan.
# 6. Setelah kriteria 4 dan 5, sistem kembali pada kondisi yang mampu bertahan dari kehilangan satu node berikutnya.
def rejoin_worker(run, name, worker):
    print(f"\n{name}: target {worker}.", flush=True)
    automatic = run.config.automatic_worker_actions
    proof = None

    if automatic:
        plan = run.storage_plans[worker]
        input(f"Enter → start {worker} with its erased database volumes and verify rebuild: ")
    else:
        print("Use the official Tester Agent to restore the worker.", flush=True)

    # Keep read/write probes running through the action and recovery checks.
    # If the automatic check fails, restart the worker before propagating it.
    with restore_worker_on_failure(run, name, worker):
        with Monitor(run, name, writes=True, target=worker, expect_ready=True) as monitor:
            if automatic:
                run_worker_action(run, name, worker, rejoin=True)
            else:
                input(f"After performing the action for {worker}, press Enter to verify: ")

            # Ready alone is not enough: wait for full replica placement too.
            wait_for_node_state(run, monitor, worker, ready=True)
            result = wait_for_recovery(run, name, redundant=True)

            if automatic:
                # Check that PostgreSQL actually rebuilt the erased volumes.
                proof = worker_storage(run, name, plan, "inspect")
                require(all(volume["rebuilt"] for volume in proof["volumes"]),
                        "a store volume has not rebuilt PostgreSQL data")

        # Include writes acknowledged right up to the moment the monitor stopped.
        final_events = export(
            run.api,
            page_size=run.config.page_size,
            max_pages=run.config.max_pages,
        )
        verify_preservation(run, final_events, set(run.acknowledged_writes))
        run.previous_events = final_events

    require(result["placement"]["copies_per_node"].get(worker, 0) > 0,
            "rejoined node is Ready but does not hold canonical data")
    result.update(
        worker=worker,
        power_action_automatic=automatic,
        disk_erasure_verified=proof is not None,
        storage_loss=proof,
        monitor=monitor.summary(),
        acknowledged_writes_preserved=len(run.acknowledged_writes),
    )
    return result


# 6. Setelah kriteria 4 dan 5, sistem kembali pada kondisi yang mampu bertahan dari kehilangan satu node berikutnya.
def restored_resilience(run):
    # The first worker has rejoined with full replicas. Lose the other one now.
    worker = run.config.second_worker
    run.stage("second-loss", lose_worker, run, "second-loss", worker)

    # Restore redundancy again before finishing the recovery group.
    return run.stage("final-rejoin", rejoin_worker, run, "final-rejoin", worker)


def run_group(run):
    print("\nGroup 2: failure and recovery — criteria 4–6", flush=True)
    worker = run.config.worker

    # 4. Bila satu node worker dimatikan dan seluruh datanya dihapus, tunjukkan bahwa tidak ada HazardEvent yang hilang (diverifikasi melalui Audit API terhadap checksum asisten). Selama sistem memulihkan diri, setiap permintaan tetap dijawab: lengkap bila datanya tersedia, atau sebagian dengan penanda eksplisit bahwa jawabannya tidak lengkap, bukan error 500 dan bukan permintaan yang menggantung.
    run.stage("first-loss", lose_worker, run, "first-loss", worker)

    # 5. Tambahkan node baru (atau node yang sama dengan disk kosong). Tunjukkan node tersebut ikut menanggung data dan beban tanpa menghentikan penulisan maupun pembacaan.
    run.stage("first-worker-rejoin", rejoin_worker, run, "first-worker-rejoin", worker)

    # 6. Setelah kriteria 4 dan 5, sistem kembali pada kondisi yang mampu bertahan dari kehilangan satu node berikutnya.
    run.stage("restored-resilience", restored_resilience, run)
