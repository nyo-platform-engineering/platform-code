# Milestone 2 tests

These are guided, interactive runs against the Kubernetes lab (VM cluster, or
`task up -- --novm` for development). Run them from `code/infrastructure`; each one
uses `.env.runtime` and `.state/kubeconfig`, keeps its settings in `CONFIG` at the
top of its `test-problem-N.py`, waits for **Enter** between steps, and saves
evidence to `tests/m2/p<N>/evidence/m2-p<N>-<timestamp>-<id>/`
(`result.json`, `http.jsonl`). P1, P4 and P5 also save the full terminal session
in `console.log` and ask before each run whether to delete older folders
(**Enter** keeps them). P4 and P5 first ask **"Save evidence for this run? [Y/n]"**:
**Enter** saves it, `n` runs in a temporary folder that is deleted at the end
(useful for practice runs). P2/P3 print wait progress every five seconds and save
`progress.jsonl`.

- [Problem 1: distributed storage and recovery](p1/README.md) — `task m2:p1`.
- [Problem 2: Aggregator coordination, failover and scaling](p2/README.md) — configure the run, then `task m2:p2`.
- [Problem 3: schema, poison messages, replay and durable field reports](p3/README.md) — `task m2:p3` on an isolated lab.
- Problem 4: Feed Satelit — `task m2:p4` ([p4/](p4/)).
- Problem 5: Sensor Pantai — `task m2:p5` ([p5/](p5/)).

P4 and P5 share [common.py](common.py): run state, `kubectl` access, port-forwards
(the Aggregator; Client and Auth use their NodePorts when reachable), evidence
files, and the step runner.

## Problem 4 — `task m2:p4`

Tes memakai deployment Aggregator, Client, dan Citus yang sedang berjalan:
180 detik feed live, lalu input SSE terkontrol untuk event time/late arrival,
update/retract, burst nyata 60×, crash sebelum commit, dan reconnect.
Data live dipertahankan; env dan Argo dipulihkan pada exit. Jangan jalankan
bersamaan dengan tes lain. Referensi asisten tetap pending sampai tersedia.

Lihat [cara menjalankan, cakupan, dan pemulihan P4](p4/README.md).

## Problem 5 — `task m2:p5`

Needs `PANTAI_AUTH_TOKEN` (created by `task up`) and the client secrets.
This guided run resets Sensor Pantai's practice data on the existing cluster.
It requires profile `practice`, seed `42`, speed `60`; it refuses the exam profile.

| Group | Steps | Criteria |
| --- | --- | --- |
| [helper.py](p5/helper.py) | pause Argo reconciliation, stop the sensor and Aggregator, clear only SENSOR_PANTAI events/inbox/outbox/cursor, restart processing and then the sensor; restore replicas and Argo settings on exit | setup |
| [group4_critical.py](p5/group4_critical.py) | runs first: wait for all three new critical readings and their durable Field Team receipts; 0 lost and p95 ≤ 5 s | 1 |
| [group1_alerts.py](p5/group1_alerts.py) | poller live with stations; COASTAL mapping (critical → AWAS, suspect never SIAGA, no duplicates); parked data visible to analyst only | 2 |
| [group2_outage.py](p5/group2_outage.py) | scale the sensor to 0: breaker opens, requests stay ≤ 0.5/s, client answers (sensor and all sources) p95 < 1 s with the sensor marked stale; scale back to 1 and wait for recovery with no other action | 3, 4, 6 |
| [group3_overload.py](p5/group3_overload.py) | `CONFIG.media_workers` Media clients flood the Client API; Media gets 429, every Field Team request is served | 5 |

The practice scenario emits three critical readings around 30, 90 and 150 seconds.
Each run starts that scenario over after deleting its old events and resetting its
polling cursor and captured batches. The Aggregator briefly stops during setup so
in-flight processing cannot recreate deleted state. Other sources' data remains.

Notification history and NATS deduplication are preserved: the new ingestion time
creates a new delivery ID. The critical check matches each new delivery to its
durable `critical_field_alert` effect in `FIELD_NOTIFIER_CRITICAL_EFFECTS`.
Latency uses the original NATS persistence time minus event ingestion time, not
the time the test reads the receipt. These are the simulated Field Team receiver's
durable records, rather than an external messaging provider's delivery receipt.
Pod log retention and when the test is started no longer determine the result.
Missing readings or receipts fail after `CONFIG.wait_critical_seconds` (300),
instead of skipping criterion 1. The setup reset happens before fault testing;
the outage recovery window still allows no intervention.
Contract violations cannot be injected into the assistants' image; they are covered
by the Go tests (`aggregator/internal/mapper`, `aggregator/cmd/server`).
