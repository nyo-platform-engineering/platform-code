# Milestone 2 tests

These are guided, interactive runs against the Kubernetes lab (VM cluster, or
`task up -- --novm` for development). Run them from `code/infrastructure`; each one
uses `.env.runtime` and `.state/kubeconfig`, keeps its settings in `CONFIG` at the
top of its `test-problem-N.py`, waits for **Enter** between steps, and saves
evidence to `tests/m2/p<N>/evidence/m2-p<N>-<timestamp>-<id>/`
(`result.json`, `http.jsonl`). P1, P4 and P5 also save the full terminal session
in `console.log` and ask before each run whether to delete older folders
(**Enter** keeps them). P2/P3 print wait progress every five seconds and save
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

Needs `SATELLITE_GROUP_TOKEN` in `.env.runtime` and the Aggregators restarted after
adding it. The feed accepts **one connection per group token**: if another stack
holds it, the run stops with `429 too_many_connections`.

| Group | Steps | Criteria |
| --- | --- | --- |
| [group1_feed.py](p4/group1_feed.py) | offset advances while watched; lag falls to `CONFIG.max_lag_events`; 0 rejected events | 7, 4, payload format |
| [group2_fires.py](p4/group2_fires.py) | every WILDFIRE: ≥ 2 detections, valid keys, satellites, no low confidence, severity rules, `occurred_at` = earliest detection, each detection in one fire, unique `source_ref_id`; NORMAL events carry `dissolved`/`merged`; excluded detections counted; Audit API checksum vs store | 1, 2, 3, 6 (partial) |
| [group3_recovery.py](p4/group3_recovery.py) | delete every Aggregator pod; the offset resumes past the pre-crash point, no fire lost or duplicated | 5, 8 |

## Problem 5 — `task m2:p5`

Needs `PANTAI_AUTH_TOKEN` (created by `task up`) and the client secrets.

| Group | Steps | Criteria |
| --- | --- | --- |
| [group1_alerts.py](p5/group1_alerts.py) | poller live with stations; COASTAL mapping (critical → AWAS, suspect never SIAGA, no duplicates); parked data visible to analyst only | 2 |
| [group2_outage.py](p5/group2_outage.py) | scale the sensor to 0: breaker opens, requests stay ≤ 0.5/s, client answers (sensor and all sources) p95 < 1 s with the sensor marked stale; scale back to 1 and wait for recovery with no other action | 3, 4, 6 |
| [group3_overload.py](p5/group3_overload.py) | `CONFIG.media_workers` Media clients flood the Client API; Media gets 429, every Field Team request is served | 5 |
| [group4_critical.py](p5/group4_critical.py) | runs last: database check, then (if needed) restart the sensor; every new critical reading is alerted, p95 ≤ 5 s | 1 |

The practice scenario sends its critical readings in the first ~150 s after the
sensor container starts (speed 60), and every restart replays the same readings,
which are deduplicated and alert only once. Group 4 therefore first checks the
database: criticals ingested while the current notifier was logging are measured at
once (a fresh lab). Otherwise it restarts the sensor and waits up to
`CONFIG.wait_critical_seconds` (300) for new ones. If the restart replays only stored
readings, the check is **skipped**, not passed, with evidence that no duplicate events
or alerts were created; proving the p95 then needs a store that has not seen the
scenario (a fresh lab, or the first run on the VM cluster).
Contract violations cannot be injected into the assistants' image; they are covered
by the Go tests (`aggregator/internal/mapper`, `aggregator/cmd/server`).
