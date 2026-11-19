# Milestone 2 tests

These are guided, interactive runs against the Kubernetes lab (VM cluster, or
`task up -- --novm` for development). Run them from `code/infrastructure`; each one
uses `.env.runtime` and `.state/kubeconfig`, keeps its settings in `CONFIG` at the
top of its `test-problem-N.py`, waits for **Enter** between steps, and saves
evidence to `docs/evidence/m2-p<N>-<timestamp>-<id>/` (`result.json`, `http.jsonl`).

- [Problem 1: distributed storage and recovery](p1/README.md) — `task m2:p1`.
- [Problem 2: Aggregator coordination, failover and scaling](p2/README.md) — configure the run, then `task m2:p2`.
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
| [group1_alerts.py](p5/group1_alerts.py) | poller live with stations; COASTAL mapping (critical → AWAS, suspect never SIAGA, no duplicates); every critical reading since the notifier started is alerted, p95 ≤ 5 s; parked data visible to analyst only | 1, 2 |
| [group2_outage.py](p5/group2_outage.py) | scale the sensor to 0: breaker opens, requests stay ≤ 0.5/s, client answers (sensor and all sources) p95 < 1 s with the sensor marked stale; scale back to 1 and wait for recovery with no other action | 3, 4, 6 |
| [group3_overload.py](p5/group3_overload.py) | `CONFIG.media_workers` Media clients flood the Client API; Media gets 429, every Field Team request is served | 5 |

Re-sent readings are deduplicated and alerts from earlier pods left with their logs,
so the alert check only covers critical readings that arrived while the current
notifier was running; it is skipped if none arrived within `CONFIG.wait_critical_seconds`.
Contract violations cannot be injected into the assistants' image; they are covered
by the Go tests (`aggregator/internal/mapper`, `aggregator/cmd/server`).
