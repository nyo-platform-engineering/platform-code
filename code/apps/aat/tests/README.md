# Integration test scripts (Problem 1, 4 & 5)

## Problem 1 — demo Python

Prerequisite: stack sudah berjalan lewat `docker compose up`.
Dari `code/apps/aat`:

```sh
python3 tests/test-problem-1.py
```

## Problem 4 & 5

Two runnable, repeatable integration tests against the existing Dockerized system.
They demonstrate assignment requirements; they do not rewrite the application.

- `test-problem-4.sh` — independent deployment, single-service rebuild,
  flexible `HazardEvent` storage, Canonical Store isolation.
- `test-problem-5.sh` — NATS JetStream fan-out to two consumers, failure
  isolation + durable recovery, third consumer via the second compose file,
  delivery semantics / idempotency.

## Actual filenames (discovered, not invented)

- Main compose file: `compose.yaml`
  services: `postgres`, `aggregator`, `bmkg`, `pvmbg`, `nats`,
  `dashboard-updater`, `field-notifier`.
  Networks: `storage` (internal, postgres + aggregator only),
  `services` (everything except postgres).
- Second compose file: `compose.consumer.yaml`
  service: `test-consumer` only. It joins the existing `services` network and
  dials the existing broker (`NATS_URL=nats://nats:4222` by default).
  Always use it chained with the main file under the **same** project name.

Other overlays in the repo (`compose.client.yaml`, `compose.scale.yaml`) are
not used by these scripts.

## Prerequisites

- Docker Engine + Compose v2 (`docker compose version`), `curl`, and
  `python3` (or `python`) for JSON handling. `jq` is optional.
- `.env` present (run `python scripts/setup-env.py` once; keep `START_TIME` stable).
- Enough disk for one extra image build (`test-consumer`, `--no-deps` only).

## Start the main stack

From `code/apps/aat`:

```sh
docker compose -p aat-part1 --env-file .env -f compose.yaml up -d --build
docker compose -p aat-part1 --env-file .env -f compose.yaml ps
curl -fsS http://127.0.0.1:8083/health        # aggregator (AGGREGATOR_PORT)
curl -fsS http://127.0.0.1:8222/healthz       # NATS monitoring
curl -fsS "http://127.0.0.1:8222/jsz?streams=true" | grep HAZARDS_STREAM
```

Host ports (defaults from `.env`): BMKG `8081`, PVMBG `8082`, Aggregator
`8083`, NATS `4222`/`8222`. Consumers (`dashboard-updater`, `field-notifier`,
`test-consumer`) intentionally expose **no** host ports; the scripts check them
via `docker inspect` health + `docker compose logs` (JSON slog lines carrying
`hazard_id`).

## How to run

```sh
chmod +x tests/test-problem-4.sh tests/test-problem-5.sh
./tests/test-problem-4.sh
./tests/test-problem-5.sh
```

Both must run from `code/apps/aat` (tests `cd` to their parent dir).
Each prints `[PASS]` / `[FAIL]` / `[INFO]` lines plus a summary, and exits
nonzero if any required check fails.

## How test-consumer is started/stopped (Problem 5, step E)

The script never starts a second NATS/Postgres and never restarts the producer
or original consumers to add the third one:

```sh
# start (only if not already running):
docker compose -p aat-part1 --env-file .env \
  -f compose.yaml -f compose.consumer.yaml up -d --build --no-deps test-consumer
# inspect:
docker compose -p aat-part1 --env-file .env \
  -f compose.yaml -f compose.consumer.yaml ps
docker compose -p aat-part1 --env-file .env \
  -f compose.yaml -f compose.consumer.yaml logs --since 5m test-consumer | grep hazard_id
```

`--no-deps` + the shared `-p aat-part1` keep the existing `services` network
and broker. By default the script leaves `test-consumer` running
(`CLEANUP_TEST_CONSUMER=0`); set `CLEANUP_TEST_CONSUMER=1` to stop it only when
the script itself started it. Streams, KV buckets, and durable consumers are
never deleted.

If the second file ever fails to join the main network (e.g. wrong `-p`, or
only `-f compose.consumer.yaml` without `-f compose.yaml`, which would create
a detached project + second NATS), the script fails step E with the exact
cause. Smallest fix: always chain `-f compose.yaml -f compose.consumer.yaml`
with the same `-p` and `--env-file`.

## Configurable timeouts

| Var                                | Default     | Meaning                                                        |
| ---------------------------------- | ----------- | -------------------------------------------------------------- |
| `PROJECT` / `COMPOSE_PROJECT_NAME` | `aat-part1` | must match the running stack                                   |
| `ENV_FILE`                         | `.env`      | dotenv with ports/tokens                                       |
| `TIMEOUT_SECS`                     | `90`        | delivery/log wait budget                                       |
| `POLL_INTERVAL_SECS`               | `2`         | bounded-poll interval                                          |
| `CURL_TIMEOUT_SECS`                | `10`        | per-request timeout                                            |
| `REBUILD_TIMEOUT_SECS` (P4)        | `300`       | rebuild health wait                                            |
| `REBUILD_SERVICE` (P4)             | `pvmbg`     | only service rebuilt (`postgres`/`nats`/`aggregator` rejected) |
| `DOWNTIME_EVENTS` (P5)             | `3`         | events published while dashboard stopped                       |
| `CLEANUP_TEST_CONSUMER` (P5)       | `0`         | `1` = stop test-consumer iff script started it                 |

Example:

```sh
TIMEOUT_SECS=120 POLL_INTERVAL_SECS=1 ./scripts/test-problem-5.sh
REBUILD_SERVICE=bmkg ./scripts/test-problem-4.sh
```

## How test events are generated and cleaned up

- Unique IDs: `p4-…` / `p5-…` + UTC timestamp + PID + `$RANDOM`
  (e.g. `report_id=p5-c-20261009T000000Z-123-4567`), so reruns never collide.
- Mechanism: the real workflow `POST /internal/ingest/pvmbg`
  (`{"volcanic_reports":[{report_id, volcano_id:MERAPI, alert_level:Siaga, …}]}`)
  with `Authorization: Bearer $AGGREGATOR_TOKEN`, then
  `GET /internal/hazards?source=PVMBG&limit=1000` to confirm mapping/storage,
  then `docker compose logs --since <ts>` polling for `hazard_id` in
  `aggregator` (`hazard published`), `dashboard-updater`, `field-notifier`,
  `test-consumer`. NATS stream checked via `GET /jsz?streams=true`
  (`HAZARDS_STREAM` / `hazards.created.v1` from `internal/eventbus/jetstream.go`).
- Cleanup: test hazard rows are **left** in Postgres (deleting them would
  destroy app data); the scripts never run `down -v`, never truncate tables,
  never delete streams/KV/durables, never restart broker/producer during the
  failure test, and never rebuild unrelated services.

## What each test proves (and limits)

**Problem 4**

- A: separate containers via `ps -q` + `docker inspect` Running state; prints
  service → 12-char container ID. Limit: proves container separation, not CPU
  isolation.
- B: `up -d --build --no-deps pvmbg` (default; `bmkg` allowed), unrelated
  `GET /seismic-events` before/after, unrelated IDs unchanged, rebuilt service
  healthy. Limit: brief `pvmbg` unavailability during rebuild is expected.
- C: ingest without `confidence_level`, ingest with `0.85`, both readable via
  `/internal/hazards`, new `attributes.confidence_level==0.85`, old record has
  no such key (flexibility = `attributes JSONB`, see
  `aggregator/internal/model/hazard_event.go` + `mapper/volcanic.go`). Reports
  failure instead of migrating if unsupported.
- D: intended isolation from `docker compose config` (postgres ∈ {storage}
  only, aggregator ∈ {services,storage}, others ∉ storage, no host ports for
  postgres, `DATABASE_URL` only in aggregator) **plus** runtime proof via
  `docker inspect` networks and DNS probes (`field-notifier` must not resolve
  `postgres`; `aggregator` must). Code grep is corroboration only. Host
  `localhost:808x` access is explicitly distinguished from container isolation.

**Problem 5**

- A/B: files printed, `nats` + both consumers running, `8222/healthz` +
  `jsz` show `HAZARDS_STREAM`, subject/stream taken from code, no hardcoded
  consumer names in `aggregator/`, async outbox relay (`publisher.go`).
- C: same `hazard_id` in both consumer logs (bounded poll, not fixed sleep).
  A healthy container alone is never accepted as delivery proof.
- D: `stop dashboard-updater` only; NATS/aggregator/field-notifier keep
  running; N downtime events still published + received by field-notifier
  (not blocked); after `start`, backlog **is** redelivered — expected from
  durable `DeliverAll` + `AckExplicit` + 7-day `LimitsPolicy` retention
  (`worker.go`). If redelivery ever fails, the script reports loss accurately
  (ephemeral Core NATS would lose; this stack is durable JetStream, so loss =
  real failure).
- E: third consumer lifecycle above; distinct durables
  (`dashboard-updater`/`field-notifier`/`test-consumer`) prove independent
  fan-out, not a shared work queue; aggregator ID + `git diff` prove no
  producer change.
- F: at-least-once (JetStream + ack + `AckWait 30s` redelivery + outbox
  `MsgID`/duplicates window) with consumer KV `deliveryKey` dedupe
  (`worker.go` + `worker_test.go`); duplicate ingest returns `changed:0`.
  Exactly-once is explicitly **not** claimed (crash between handler log and
  KV write can duplicate the demo log line).

## Interpreting failures

- `docker daemon not reachable` → start Docker Desktop first.
- `has no container` / `not running` → start the stack (see above) with the
  same `-p`/`-f`/`--env-file`.
- `did NOT receive <hazard_id>` → check `docker compose logs <svc>` and
  `aggregator` `hazard published` lines; likely causes: poller/outbox lag
  (raise `TIMEOUT_SECS`), NATS stream missing, or wrong `-p` (second broker).
- `container changed` (P4-B/P5-E) → something restarted the service outside
  the script; rerun on a quiescent stack.
- `DATABASE_URL appears N times` / `on storage network` (P4-D) → compose
  networks/env were edited; restore `compose.yaml` intent (postgres internal).
- `test-consumer missed` → verify same `-p`, `NATS_URL=nats://nats:4222`,
  `services` network, and distinct durable name.
- Any `[FAIL]` → exit 1; `[INFO]` lines are diagnostics, not verdicts. A test
  that could not run (e.g. daemon down) is reported as `[FAIL]` with the
  prerequisite, never silently marked passed.
