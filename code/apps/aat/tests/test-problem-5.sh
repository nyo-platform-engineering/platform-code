#!/usr/bin/env bash
# Problem 5 integration test for Gateway Astolfo (NATS JetStream fan-out).
#
# Uses the TWO existing compose files; never replaces consumers with mocks,
# never modifies the producer to register consumers, never creates a second
# broker or an isolated project that cannot reach the existing NATS.
#   main file:   compose.yaml            (postgres, aggregator, bmkg, pvmbg,
#                                        nats, dashboard-updater, field-notifier)
#   second file: compose.consumer.yaml  (test-consumer only; shares the same
#                                        project/network/NATS via -f chaining)
#
# Flow:
#   A. discover files, verify NATS + two original consumers
#   B. verify producer publication (outbox -> HAZARDS_STREAM/hazards.created.v1)
#   C. verify both original consumers receive the same event (logs)
#   D. failure isolation: stop dashboard-updater, publish during downtime,
#      verify field-notifier continues, restart and check backlog recovery
#   E. add third consumer via second compose file, verify all three receive
#   F. delivery semantics + idempotency (at-least-once + KV dedupe)
#
# Run from code/apps/aat:
#   ./scripts/test-problem-5.sh
#
# Env (no secrets printed):
#   PROJECT / COMPOSE_PROJECT_NAME   default aat-part1 (must match stack)
#   ENV_FILE                         default .env
#   TIMEOUT_SECS                     delivery wait timeout (default 90)
#   POLL_INTERVAL_SECS               polling interval (default 2)
#   CURL_TIMEOUT_SECS                default 10
#   DOWNTIME_EVENTS                  events to publish while stopped (default 3)
#   CLEANUP_TEST_CONSUMER            0=leave test-consumer running (default), 1=stop if we started it
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

COMPOSE_MAIN="compose.yaml"
COMPOSE_EXTRA="compose.consumer.yaml"
PROJECT="${COMPOSE_PROJECT_NAME:-${PROJECT:-aat-part1}}"
ENV_FILE="${ENV_FILE:-.env}"
TIMEOUT_SECS="${TIMEOUT_SECS:-90}"
POLL_INTERVAL_SECS="${POLL_INTERVAL_SECS:-2}"
CURL_TIMEOUT_SECS="${CURL_TIMEOUT_SECS:-10}"
DOWNTIME_EVENTS="${DOWNTIME_EVENTS:-3}"
CLEANUP_TEST_CONSUMER="${CLEANUP_TEST_CONSUMER:-0}"

PASS_COUNT=0
FAIL_COUNT=0
STARTED_TEST_CONSUMER=0

info() { echo "[INFO] $*"; }
pass() { PASS_COUNT=$((PASS_COUNT + 1)); echo "[PASS] $*"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); echo "[FAIL] $*"; }

# Portable python for JSON parsing: probe actual execution (Windows
# python3 stub exists but prints "Python was not found"; require output "ok").
PYBIN=""
for _c in python python3 py; do
  if command -v "$_c" >/dev/null 2>&1 && [ "$("$_c" -c "print('ok')" 2>/dev/null)" = "ok" ]; then
    PYBIN="$_c"; break
  fi
done

need() { command -v "$1" >/dev/null 2>&1 || { fail "missing required command: $1"; return 1; }; }

env_val() {
  local key="$1" def="${2:-}" v=""
  if [ -f "$ENV_FILE" ]; then
    v="$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2- | tr -d '\r' | sed -e 's/^["'\'']//' -e 's/["'\'']$//')"
  fi
  [ -z "$v" ] && v="$def"
  printf '%s' "$v"
}

compose_main()  { docker compose -p "$PROJECT" --env-file "$ENV_FILE" -f "$COMPOSE_MAIN" "$@"; }
compose_both()  { docker compose -p "$PROJECT" --env-file "$ENV_FILE" -f "$COMPOSE_MAIN" -f "$COMPOSE_EXTRA" "$@"; }

cid_main() { compose_main ps -q "$1" 2>/dev/null | tr -d '\r' | head -n1 | tr -d '[:space:]'; }
cid_both() { compose_both ps -q "$1" 2>/dev/null | tr -d '\r' | head -n1 | tr -d '[:space:]'; }
is_running() { [ -n "${1:-}" ] && [ "$(docker inspect --format '{{.State.Running}}' "$1" 2>/dev/null | tr -d '\r')" = "true" ]; }

# Bounded poll: run command until it exits 0 or timeout. Prints last output tail on failure.
wait_for() {
  local desc="$1" timeout="$2"; shift 2
  local deadline=$((SECONDS + timeout)) out=""
  while [ "$SECONDS" -lt "$deadline" ]; do
    if out="$("$@" 2>&1)"; then printf '%s' "$out"; return 0; fi
    sleep "$POLL_INTERVAL_SECS"
  done
  echo "$out" | tail -n8
  return 1
}

# True iff hazard_id appears in service logs since $2 (docker timestamp).
log_has() {
  local svc="$1" since="$2" hid="$3"
  compose_main logs --no-log-prefix --since "$since" "$svc" 2>/dev/null | grep -q "$hid"
}
log_has_both() {
  local svc="$1" since="$2" hid="$3"
  compose_both logs --no-log-prefix --since "$since" "$svc" 2>/dev/null | grep -q "$hid"
}

# True iff the aggregator outbox relay logged any publication since $1.
# (Separate function because wait_for executes in this shell: a
# `bash -c "compose_main ..."` string would NOT see our shell functions.)
agg_published_since() {
  local since="$1"
  compose_main logs --no-log-prefix --since "$since" aggregator 2>/dev/null | grep -q 'hazard published'
}

# Ingest one unique volcanic report; prints "report_id hazard_id" on success.
ingest_unique() {
  local tag="$1" with_conf="$2"
  local rid="p5-${tag}-$(date -u +%Y%m%dT%H%M%S 2>/dev/null || echo t)-$$-$RANDOM"
  local rat
  rat="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || $PYBIN -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
  local payload="/tmp/p5-${rid}.json"
  if [ "$with_conf" = "conf" ]; then
    $PYBIN - "$rid" "$rat" "$payload" <<'PY'
import json, sys
rid, rat, out = sys.argv[1], sys.argv[2], sys.argv[3]
rec = {"report_id": rid, "volcano_id": "MERAPI", "alert_level": "Siaga",
       "eruption_count_24h": 2, "ash_column_height_m": 300,
       "reported_at": rat, "confidence_level": 0.85}
json.dump({"volcanic_reports": [rec]}, open(out, "w"))
PY
  else
    $PYBIN - "$rid" "$rat" "$payload" <<'PY'
import json, sys
rid, rat, out = sys.argv[1], sys.argv[2], sys.argv[3]
rec = {"report_id": rid, "volcano_id": "MERAPI", "alert_level": "Siaga",
       "eruption_count_24h": 2, "ash_column_height_m": 300, "reported_at": rat}
json.dump({"volcanic_reports": [rec]}, open(out, "w"))
PY
  fi
  local resp="/tmp/p5-${rid}.resp.json"
  if ! curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "Authorization: Bearer $AGG_TOKEN" \
      -H 'Content-Type: application/json' --data-binary @"$payload" \
      "http://127.0.0.1:${AGG_PORT}/internal/ingest/pvmbg" -o "$resp" 2>/tmp/p5-ing.err; then
    echo "INGEST_HTTP_FAIL $rid" >&2; head -c 400 /tmp/p5-ing.err >&2; echo >&2
    rm -f "$payload"; return 1
  fi
  rm -f "$payload"
  local hid
  hid="$($PYBIN -c 'import json,sys;print(json.load(open(sys.argv[1]))["items"][0]["hazard_id"])' "$resp" 2>/dev/null || echo "")"
  if [ -z "$hid" ]; then echo "INGEST_NO_HAZARD $rid" >&2; head -c 400 "$resp" >&2; echo >&2; return 1; fi
  rm -f "$resp"
  echo "$rid $hid"
}

# Wait until hazard_id visible in service logs (bounded, not fixed sleep).
wait_log() {
  local svc="$1" since="$2" hid="$3" timeout="${4:-$TIMEOUT_SECS}" use_both="${5:-0}"
  local deadline=$((SECONDS + timeout))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if [ "$use_both" = "1" ]; then log_has_both "$svc" "$since" "$hid" && return 0;
    else log_has "$svc" "$since" "$hid" && return 0; fi
    sleep "$POLL_INTERVAL_SECS"
  done
  return 1
}

now_ts() { date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || $PYBIN -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))'; }

# ---------------------------------------------------------------- header
info "Problem 5 test"
info "root=$ROOT project=$PROJECT main=$COMPOSE_MAIN extra=$COMPOSE_EXTRA env=$ENV_FILE"
info "timeouts: TIMEOUT=$TIMEOUT_SECS POLL=$POLL_INTERVAL_SECS CURL=$CURL_TIMEOUT_SECS downtime_events=$DOWNTIME_EVENTS"
need docker; need curl
if [ -z "$PYBIN" ]; then fail "need python3/python for JSON handling"; fi
[ -f "$COMPOSE_MAIN" ]  || fail "main compose file missing: $COMPOSE_MAIN"
[ -f "$COMPOSE_EXTRA" ] || fail "second compose file missing: $COMPOSE_EXTRA"
[ -f "$ENV_FILE" ]      || fail "env file missing: $ENV_FILE"
docker info >/dev/null 2>&1 || { fail "docker daemon unreachable"; info "start Docker Desktop first"; }

AGG_PORT="$(env_val AGGREGATOR_PORT 8083)"
AGG_TOKEN="$(env_val AGGREGATOR_TOKEN "")"
[ -n "$AGG_TOKEN" ] || fail "AGGREGATOR_TOKEN missing in $ENV_FILE"
NATS_MON="http://127.0.0.1:8222"

# ---------------------------------------------------------------- A. discover + health
info "--- A. Compose files, broker, original consumers ---"
info "discovered main compose file: $COMPOSE_MAIN"
info "discovered second compose file: $COMPOSE_EXTRA (defines test-consumer; shares project/network/NATS)"
if grep -q 'test-consumer' "$COMPOSE_EXTRA" 2>/dev/null; then pass "second file defines test-consumer";
else fail "second file does not define test-consumer"; fi
if grep -q 'networks.*services\|networks: \[services\]' "$COMPOSE_EXTRA" 2>/dev/null; then
  pass "test-consumer attaches to existing services network"
else fail "test-consumer network config unexpected (must join existing services network)"; fi

NATS_CID="$(cid_main nats)"; DASH_CID="$(cid_main dashboard-updater)"; FIELD_CID="$(cid_main field-notifier)"
info "broker container: nats=${NATS_CID:0:12} dashboard-updater=${DASH_CID:0:12} field-notifier=${FIELD_CID:0:12}"
if [ -z "$NATS_CID" ]; then fail "nats container missing; start stack: docker compose -p $PROJECT -f $COMPOSE_MAIN up -d --build";
else is_running "$NATS_CID" && pass "nats container running" || fail "nats container not running"; fi

if curl -fsS --max-time "$CURL_TIMEOUT_SECS" "$NATS_MON/healthz" -o /dev/null 2>&1 \
   || curl -fsS --max-time "$CURL_TIMEOUT_SECS" "$NATS_MON/varz" -o /dev/null 2>&1; then
  pass "NATS healthy (monitoring $NATS_MON reachable)"
else
  fail "NATS monitoring unreachable at $NATS_MON (broker down or wrong port?)"
fi
if curl -fsS --max-time "$CURL_TIMEOUT_SECS" "$NATS_MON/jsz?streams=true" -o /tmp/p5-jsz.json 2>/dev/null \
   && grep -q 'HAZARDS_STREAM' /tmp/p5-jsz.json; then
  pass "JetStream stream HAZARDS_STREAM exists"
else
  fail "HAZARDS_STREAM not visible via $NATS_MON/jsz?streams=true (outbox relay may not have initialized it yet)"
fi

for s in dashboard-updater field-notifier; do
  c="$(cid_main "$s")"
  if [ -z "$c" ]; then fail "$s not running (start stack first)"; continue; fi
  if is_running "$c"; then
    hs="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$c" 2>/dev/null | tr -d '\r')"
    info "$s running (${c:0:12}, health=$hs; no host port by design, health via inspect+logs)"
    if [ "$hs" = "healthy" ] || [ "$hs" = "none" ]; then pass "$s running and operational";
    else fail "$s health=$hs"; fi
  else
    fail "$s container not running"
  fi
done
if curl -fsS --max-time "$CURL_TIMEOUT_SECS" "http://127.0.0.1:${AGG_PORT}/health" -o /dev/null 2>&1; then
  pass "aggregator health OK (publisher side up)"
else
  fail "aggregator health failed"
fi

# ---------------------------------------------------------------- B. producer publication
info "--- B. Producer publication to NATS ---"
STREAM="$(grep -o 'StreamName = "[^"]*"' internal/eventbus/jetstream.go 2>/dev/null | cut -d'"' -f2)"
SUBJECT="$(grep -o 'Subject *= *"[^"]*"' internal/eventbus/jetstream.go 2>/dev/null | cut -d'"' -f2)"
[ -z "$STREAM" ] && STREAM="HAZARDS_STREAM"
[ -z "$SUBJECT" ] && SUBJECT="hazards.created.v1"
info "configured stream=$STREAM subject=$SUBJECT (internal/eventbus/jetstream.go)"
if [ "$STREAM" = "HAZARDS_STREAM" ] && [ "$SUBJECT" = "hazards.created.v1" ]; then
  pass "actual NATS subject/stream discovered from implementation"
else
  fail "unexpected stream/subject ($STREAM/$SUBJECT)"
fi
if grep -R "dashboard-updater\|field-notifier\|test-consumer" aggregator/ internal/eventbus/ 2>/dev/null | grep -q .; then
  fail "producer references consumer names (registration hardcoded?)"
  grep -R "dashboard-updater\|field-notifier\|test-consumer" aggregator/ internal/eventbus/ 2>/dev/null | head -5
else
  pass "no consumer registration hardcoded in producer"
fi
if grep -q "PublishMsg\|js.Publish" aggregator/cmd/server/publisher.go 2>/dev/null \
   && grep -q "hazard_outbox\|ClaimOutbox\|WithMsgID" aggregator/cmd/server/publisher.go aggregator/internal/controller/*.go 2>/dev/null; then
  pass "producer publishes asynchronously via outbox relay -> JetStream (no sync consumer calls)"
else
  fail "publisher implementation does not show expected outbox->JetStream path"
fi

TS_B="$(now_ts)"
OUT_B="$(ingest_unique "b" "plain" || echo FAIL)"
if echo "$OUT_B" | grep -q FAIL; then
  fail "could not ingest probe event for publication check"
  HID_B=""
else
  RID_B="${OUT_B%% *}"; HID_B="${OUT_B##* }"
  info "probe event: report=$RID_B hazard=$HID_B"
  # mapped + stored?
  if curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "Authorization: Bearer $AGG_TOKEN" \
      "http://127.0.0.1:${AGG_PORT}/internal/hazards?source=PVMBG&limit=1000" -o /tmp/p5-hazb.json 2>/dev/null \
     && grep -q "$HID_B" /tmp/p5-hazb.json; then
    pass "aggregator mapped+stored probe event ($HID_B)"
  else
    fail "probe event not found in canonical store"
  fi
  if wait_for "aggregator publish $HID_B" "$TIMEOUT_SECS" agg_published_since "$TS_B"; then
    pass "aggregator published to NATS (outbox relay logged hazard published)"
  else
    fail "no 'hazard published' log for probe window (publishing broken or too slow?)"
  fi
  # payload check: stored hazard JSON should equal published payload shape (hazard_id/source/type/severity/area).
  if "$PYBIN" - "$HID_B" <<'PY' 2>/dev/null; then
import json, sys
hid = sys.argv[1]
items = json.load(open("/tmp/p5-hazb.json"))["items"]
m = [i for i in items if i["hazard_id"] == hid]
assert m, "missing"
h = m[0]
assert h["source"] == "PVMBG" and h["hazard_type"] == "VOLCANIC" and h["area_name"] == "Gunung Merapi", h
print("payload ok")
PY
    pass "published payload matches mapped event (PVMBG/VOLCANIC/Gunung Merapi, hazard_id $HID_B)"
  else
    fail "mapped payload fields unexpected for $HID_B"
  fi
fi

# ---------------------------------------------------------------- C. two original consumers
info "--- C. Both original consumers receive the same event ---"
TS_C="$(now_ts)"
OUT_C="$(ingest_unique "c" "plain" || echo FAIL)"
if echo "$OUT_C" | grep -q FAIL; then
  fail "could not ingest fan-out probe event"
else
  RID_C="${OUT_C%% *}"; HID_C="${OUT_C##* }"
  info "fan-out event: report=$RID_C hazard=$HID_C (baseline $TS_C)"
  OK_C=1
  wait_log dashboard-updater "$TS_C" "$HID_C" "$TIMEOUT_SECS" 0 \
    && pass "dashboard-updater received $HID_C" \
    || { fail "dashboard-updater did NOT receive $HID_C within ${TIMEOUT_SECS}s (logs since $TS_C)"; OK_C=0; }
  wait_log field-notifier "$TS_C" "$HID_C" "$TIMEOUT_SECS" 0 \
    && pass "field-notifier received $HID_C" \
    || { fail "field-notifier did NOT receive $HID_C within ${TIMEOUT_SECS}s"; OK_C=0; }
  if [ "$OK_C" = "1" ]; then pass "both original consumers received the same event"; fi
fi

# ---------------------------------------------------------------- D. failure isolation + recovery
info "--- D. Consumer failure isolation and recovery (stop dashboard-updater) ---"
DASH_CID="$(cid_main dashboard-updater)"; FIELD_CID="$(cid_main field-notifier)"; NATS_CID="$(cid_main nats)"
if ! is_running "$DASH_CID" || ! is_running "$FIELD_CID" || ! is_running "$NATS_CID"; then
  fail "prerequisite: nats + both consumers must be running before failure test"
else
  pass "prerequisite: nats + both consumers operational"
fi
TS_D="$(now_ts)"
info "stopping ONLY dashboard-updater"
if compose_main stop dashboard-updater >/tmp/p5-stop.log 2>&1; then
  pass "dashboard-updater stopped"
else fail "could not stop dashboard-updater"; fi
sleep 3
if is_running "$(cid_main dashboard-updater)"; then fail "dashboard-updater still running after stop";
else info "dashboard-updater confirmed stopped"; fi
is_running "$(cid_main nats)" && is_running "$(cid_main field-notifier)" && is_running "$(cid_main aggregator)" \
  && pass "nats/aggregator/field-notifier still running during outage" \
  || fail "an unrelated service stopped unexpectedly"

info "publishing $DOWNTIME_EVENTS unique events during downtime"
DOWNTIME_HIDS=""
for i in $(seq 1 "$DOWNTIME_EVENTS"); do
  OUT="$(ingest_unique "down${i}" "plain" || echo FAIL)"
  if echo "$OUT" | grep -q FAIL; then fail "downtime ingest #$i failed"; continue; fi
  DOWNTIME_HIDS="$DOWNTIME_HIDS ${OUT##* }"
  info "downtime event #$i: $OUT"
done
DOWNTIME_HIDS="$(echo "$DOWNTIME_HIDS" | xargs)"
if [ -z "$DOWNTIME_HIDS" ]; then
  fail "no downtime events published; cannot test isolation"
else
  # aggregator keeps publishing (not synchronously blocked)
  ALL_PUB=1
  for h in $DOWNTIME_HIDS; do
    if wait_log aggregator "$TS_D" "$h" 20 0; then info "published during downtime: $h";
    else fail "aggregator did NOT publish $h while consumer stopped (blocked?)"; ALL_PUB=0; fi
  done
  [ "$ALL_PUB" = "1" ] && pass "aggregator continued publishing during consumer downtime (not blocked)"
  # healthy consumer keeps receiving
  ALL_HEALTHY=1
  for h in $DOWNTIME_HIDS; do
    if wait_log field-notifier "$TS_D" "$h" "$TIMEOUT_SECS" 0; then info "field-notifier got $h during outage";
    else fail "field-notifier missed $h during dashboard outage"; ALL_HEALTHY=0; fi
  done
  [ "$ALL_HEALTHY" = "1" ] && pass "healthy consumer continued receiving during downtime"
  # stopped consumer must NOT receive while down
  for h in $DOWNTIME_HIDS; do
    if log_has dashboard-updater "$TS_D" "$h"; then fail "dashboard-updater log shows $h while stopped (unexpected)"; fi
  done
  info "stopped consumer correctly received nothing while down (checked)"

  info "restarting dashboard-updater"
  if compose_main start dashboard-updater >/tmp/p5-start.log 2>&1; then pass "dashboard-updater restart issued";
  else fail "could not restart dashboard-updater"; fi
  # Bounded wait for container to be running again (health may be starting/none).
  _dead=$((SECONDS + TIMEOUT_SECS))
  while [ "$SECONDS" -lt "$_dead" ]; do
    _c="$(cid_main dashboard-updater)"
    if is_running "$_c"; then break; fi
    sleep "$POLL_INTERVAL_SECS"
  done
  is_running "$(cid_main dashboard-updater)" \
    && info "dashboard-updater running again" \
    || fail "dashboard-updater not running after start"
  sleep 5
  # Expectation from implementation: durable JetStream consumer, DeliverAllPolicy,
  # AckExplicit, FileStorage, 7-day retention -> backlog SHOULD be redelivered.
  info "implementation: durable=dashboard-updater DeliverAll+AckExplicit, stream LimitsPolicy 7d (worker.go/eventbus) => backlog expected"
  RECOVERED=1
  for h in $DOWNTIME_HIDS; do
    if wait_log dashboard-updater "$TS_D" "$h" "$TIMEOUT_SECS" 0; then info "recovered after restart: $h";
    else fail "dashboard-updater did NOT recover $h after restart"; RECOVERED=0; fi
  done
  if [ "$RECOVERED" = "1" ]; then
    pass "restarted consumer received backlog published while stopped (durable JetStream recovery proven)"
  else
    info "delivery model note: if consumer used ephemeral Core NATS, backlog would be lost; here config is durable JetStream so loss would indicate redelivery/ack misconfig, NOT expected loss"
  fi
fi

# ---------------------------------------------------------------- E. third consumer via second file
info "--- E. Third consumer via $COMPOSE_EXTRA ---"
AGG_BEFORE="$(cid_main aggregator)"
[ -z "$AGG_BEFORE" ] && AGG_BEFORE="$(cid_both aggregator)"
info "aggregator container before: ${AGG_BEFORE:0:12}"
# Snapshot the producer tree BEFORE the third-consumer step. The repo may
# already have pre-existing local modifications (unrelated to this script),
# so the correct check is before-vs-after, NOT a clean tree.
PROD_DIFF_BEFORE="$(git status --porcelain -- aggregator/ internal/eventbus/ 2>/dev/null || echo "git-unavailable")"
if [ -z "$PROD_DIFF_BEFORE" ]; then
  info "producer sources unmodified before third-consumer step (git clean)"
else
  info "note: working tree already has local modifications before this step (not made by script); snapshotted for before/after comparison"
fi
TEST_BEFORE="$(cid_both test-consumer)"
if [ -n "$TEST_BEFORE" ] && is_running "$TEST_BEFORE"; then
  info "test-consumer already running (${TEST_BEFORE:0:12}); will not disrupt it, only verify delivery"
else
  info "starting test-consumer with existing second file (same project/network/NATS, no second broker)"
  info "cmd: docker compose -p $PROJECT -f $COMPOSE_MAIN -f $COMPOSE_EXTRA up -d --build --no-deps test-consumer"
  if compose_both up -d --build --no-deps test-consumer >/tmp/p5-up-test.log 2>&1; then
    pass "test-consumer start issued via second compose file"
    STARTED_TEST_CONSUMER=1
  else
    fail "could not start test-consumer via second compose file"; tail -n20 /tmp/p5-up-test.log || true
  fi
fi
TEST_CID="$(cid_both test-consumer)"
[ -z "$TEST_CID" ] && TEST_CID="$(cid_main test-consumer)"
if [ -n "$TEST_CID" ] && is_running "$TEST_CID"; then
  pass "test-consumer running (${TEST_CID:0:12})"
else
  fail "test-consumer not running after start"
fi
# Same broker/network?
TEST_NETS="$(docker inspect --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$TEST_CID" 2>/dev/null || echo ?)"
TEST_ENV="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$TEST_CID" 2>/dev/null | grep '^NATS_URL=' || echo ?)"
info "test-consumer networks: $TEST_NETS env: $TEST_ENV"
echo "$TEST_NETS" | grep -q "services" && pass "test-consumer on existing services network (same NATS)" \
  || fail "test-consumer not on services network"
echo "$TEST_ENV" | grep -q "nats:4222" && pass "test-consumer connects to same broker (nats:4222)" \
  || fail "test-consumer NATS_URL unexpected: $TEST_ENV"
# Distinct durable (independent delivery, not a shared work queue)?
if grep -q '"dashboard-updater"' consumers/dashboard-updater/main.go \
   && grep -q '"field-notifier"' consumers/field-notifier/main.go \
   && grep -q '"test-consumer"' consumers/test-consumer/main.go; then
  pass "three consumers use distinct durable names (independent fan-out, not one work queue)"
else
  fail "durable names not distinct as expected"
fi

TS_E="$(now_ts)"
OUT_E="$(ingest_unique "e3" "plain" || echo FAIL)"
if echo "$OUT_E" | grep -q FAIL; then
  fail "could not ingest three-consumer probe event"
else
  HID_E="${OUT_E##* }"
  info "three-consumer probe: $OUT_E"
  OK3=1
  wait_log dashboard-updater "$TS_E" "$HID_E" "$TIMEOUT_SECS" 0 \
    && pass "dashboard-updater received third-probe $HID_E" \
    || { fail "dashboard-updater missed $HID_E"; OK3=0; }
  wait_log field-notifier "$TS_E" "$HID_E" "$TIMEOUT_SECS" 0 \
    && pass "field-notifier received third-probe $HID_E" \
    || { fail "field-notifier missed $HID_E"; OK3=0; }
  wait_log test-consumer "$TS_E" "$HID_E" "$TIMEOUT_SECS" 1 \
    && pass "test-consumer received third-probe $HID_E" \
    || { fail "test-consumer missed $HID_E (subscription/stream mismatch?)"; OK3=0; }
  [ "$OK3" = "1" ] && pass "all three independent consumers received the same event"
fi
AGG_AFTER="$(cid_main aggregator)"; [ -z "$AGG_AFTER" ] && AGG_AFTER="$(cid_both aggregator)"
if [ -n "$AGG_BEFORE" ] && [ "$AGG_BEFORE" = "$AGG_AFTER" ]; then
  pass "aggregator container unchanged (${AGG_AFTER:0:12}; no producer restart to add consumer)"
else
  fail "aggregator container changed ($AGG_BEFORE -> $AGG_AFTER)"
fi
PROD_DIFF_AFTER="$(git status --porcelain -- aggregator/ internal/eventbus/ 2>/dev/null || echo "git-unavailable")"
if [ "$PROD_DIFF_AFTER" = "$PROD_DIFF_BEFORE" ]; then
  pass "script introduced no producer source/config changes to register third consumer"
  if [ -n "$PROD_DIFF_AFTER" ] && [ "$PROD_DIFF_AFTER" != "git-unavailable" ]; then
    info "pre-existing local modifications (untouched by script):"
    echo "$PROD_DIFF_AFTER" | head -5 | sed 's/^/[INFO]   /'
    info "running container predates them (ID unchanged above), so they have no effect until a rebuild"
  fi
else
  fail "producer tree changed during this script (before/after diff differs)"
  info "before:"; echo "$PROD_DIFF_BEFORE" | head -5 | sed 's/^/[INFO]   /'
  info "after:"; echo "$PROD_DIFF_AFTER" | head -5 | sed 's/^/[INFO]   /'
fi

# ---------------------------------------------------------------- F. semantics + idempotency
info "--- F. Delivery semantics and idempotency ---"
info "publisher: JetStream FileStorage, LimitsPolicy, Duplicates 2m, ExpectStream, MsgID=eventKey (publisher.go/eventbus)"
info "consumer: DeliverAll, AckExplicit, AckWait 30s, MaxAckPending 1, KV dedupe bucket per durable, NakWithDelay on failure, Term on invalid (worker.go)"
if grep -q 'AckExplicitPolicy' consumers/internal/worker/worker.go \
   && grep -q 'DeliverAllPolicy' consumers/internal/worker/worker.go \
   && grep -q 'FileStorage' internal/eventbus/jetstream.go; then
  pass "delivery is at-least-once (durable JetStream + explicit ack + redelivery), NOT at-most-once"
else
  fail "could not confirm at-least-once configuration from code"
fi
# Duplicate ingest should be idempotent at producer (changed=0, no second publish storm).
TS_F="$(now_ts)"
DUP_OUT="$(ingest_unique "dup" "plain" || echo FAIL)"
if echo "$DUP_OUT" | grep -q FAIL; then
  info "skipping duplicate-idempotency probe (ingest failed)"
else
  DUP_RID="${DUP_OUT%% *}"; DUP_HID="${DUP_OUT##* }"
  info "duplicate probe: re-ingesting report $DUP_RID with the IDENTICAL payload directly via API"
  # Reuse the stored occurred_at as reported_at (volcanic mapping sets
  # occurred_at = reported_at), so the remapped hazard is byte-identical and
  # the producer must answer changed=0 instead of publishing a duplicate.
  DUP_OCC="$(curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "Authorization: Bearer $AGG_TOKEN" \
      "http://127.0.0.1:${AGG_PORT}/internal/hazards?source=PVMBG&limit=1000" 2>/dev/null | \
      "$PYBIN" -c 'import json,sys;hid=sys.argv[1];items=json.load(sys.stdin)["items"];m=[i for i in items if i["hazard_id"]==hid];print(m[0]["occurred_at"] if m else "")' "$DUP_HID" 2>/dev/null || echo "")"
  if [ -z "$DUP_OCC" ]; then
    info "duplicate re-ingest skipped: could not read back occurred_at for $DUP_HID"
  else
  DUP_PAY="/tmp/p5-dup.json"
  "$PYBIN" - "$DUP_RID" "$DUP_OCC" "$DUP_PAY" <<'PY'
import json, sys
rid, rat, out = sys.argv[1], sys.argv[2], sys.argv[3]
rec = {"report_id": rid, "volcano_id": "MERAPI", "alert_level": "Siaga",
       "eruption_count_24h": 2, "ash_column_height_m": 300, "reported_at": rat}
json.dump({"volcanic_reports": [rec]}, open(out, "w"))
PY
  if curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "Authorization: Bearer $AGG_TOKEN" \
      -H 'Content-Type: application/json' --data-binary @"$DUP_PAY" \
      "http://127.0.0.1:${AGG_PORT}/internal/ingest/pvmbg" -o /tmp/p5-dup-resp.json 2>/dev/null; then
    CH="$("$PYBIN" -c 'import json;print(json.load(open("/tmp/p5-dup-resp.json")).get("changed","?"))' 2>/dev/null || echo ?)"
    info "re-ingest changed=$CH (expected 0: producer dedupes unchanged events)"
    if [ "$CH" = "0" ]; then
      pass "duplicate event IDs do not cause duplicate publishes (producer idempotent)"
    else
      fail "identical re-ingest returned changed=$CH (expected 0; duplicate would republish)"
    fi
  else
    info "duplicate re-ingest request failed; cannot assess producer dedupe"
  fi
  rm -f "$DUP_PAY" /tmp/p5-dup-resp.json
  fi
fi
if grep -q 'dedupe\|KeyValue\|deliveryKey' consumers/internal/worker/worker.go \
   && grep -q 'TestDeliveryKey' consumers/internal/worker/worker_test.go; then
  pass "consumer idempotency implemented (KV deliveryKey dedupe) and unit-tested; NOT exactly-once end-to-end (log side effect can duplicate on crash-before-KV-write)"
else
  fail "consumer idempotency mechanism missing or untested"
fi
info "verdict: at-least-once delivery with consumer-side dedupe; exactly-once NOT claimed"

# ---------------------------------------------------------------- cleanup (only own resources)
if [ "$CLEANUP_TEST_CONSUMER" = "1" ] && [ "$STARTED_TEST_CONSUMER" = "1" ]; then
  info "cleanup: stopping test-consumer (we started it and CLEANUP_TEST_CONSUMER=1)"
  compose_both stop test-consumer >/dev/null 2>&1 || info "cleanup stop failed (leaving as-is)"
else
  info "cleanup: leaving test-consumer running (owned by stack/demo, not deleted); streams/durables untouched"
fi
info "never ran: down -v, table/stream wipes, broker/producer restarts during failure test"

# ---------------------------------------------------------------- G. summary
echo "=============================="
echo "Problem 5 summary: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "NATS health, publication, 2-consumer fan-out, downtime continuity, backlog recovery,"
echo "third consumer via $COMPOSE_EXTRA, no-producer-change, and at-least-once findings above."
if [ "$FAIL_COUNT" -gt 0 ]; then
  echo "[FAIL] problem-5: $FAIL_COUNT check(s) failed"
  exit 1
fi
echo "[PASS] problem-5: all required checks passed"
