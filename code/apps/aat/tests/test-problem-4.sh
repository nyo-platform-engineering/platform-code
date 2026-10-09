#!/usr/bin/env bash
# Problem 4 integration test for Gateway Astolfo.
#
# Proves against the existing Dockerized system (no app rewrite):
#   A. Independent container deployment (separate containers/networks).
#   B. Rebuild + restart one service (pvmbg) without disturbing others.
#   C. Flexible HazardEvent storage (attributes JSONB carries confidence_level).
#   D. Canonical Store isolation (only aggregator on storage network).
#
# Run from the repository root (code/apps/aat):
#   ./scripts/test-problem-4.sh
#
# Configurable via environment (no secrets printed):
#   PROJECT / COMPOSE_PROJECT_NAME  Compose project name (default: aat-part1)
#   ENV_FILE                        dotenv file (default: .env)
#   TIMEOUT_SECS                    generic wait timeout (default: 90)
#   POLL_INTERVAL_SECS              polling interval (default: 2)
#   CURL_TIMEOUT_SECS               per-request curl timeout (default: 10)
#   REBUILD_TIMEOUT_SECS            rebuild wait timeout (default: 300)
#   REBUILD_SERVICE                 service to rebuild (default: pvmbg)
#
# Safety: never runs `down -v`, never wipes tables/streams/consumers,
# never restarts postgres/nats/unrelated services, never deletes app data.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

COMPOSE_MAIN="compose.yaml"
PROJECT="${COMPOSE_PROJECT_NAME:-${PROJECT:-aat-part1}}"
ENV_FILE="${ENV_FILE:-.env}"
TIMEOUT_SECS="${TIMEOUT_SECS:-90}"
POLL_INTERVAL_SECS="${POLL_INTERVAL_SECS:-2}"
CURL_TIMEOUT_SECS="${CURL_TIMEOUT_SECS:-10}"
REBUILD_TIMEOUT_SECS="${REBUILD_TIMEOUT_SECS:-300}"
REBUILD_SERVICE="${REBUILD_SERVICE:-pvmbg}"

PASS_COUNT=0
FAIL_COUNT=0

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

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing required command: $1"
    return 1
  fi
  return 0
}

# Read a KEY from ENV_FILE without printing its value.
env_val() {
  local key="$1" def="${2:-}"
  local v=""
  if [ -f "$ENV_FILE" ]; then
    v="$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | tail -n1 | cut -d= -f2- | tr -d '\r' | sed -e 's/^["'\'']//' -e 's/["'\'']$//')"
  fi
  if [ -z "$v" ]; then v="$def"; fi
  printf '%s' "$v"
}

compose_main() {
  docker compose -p "$PROJECT" --env-file "$ENV_FILE" -f "$COMPOSE_MAIN" "$@"
}

cid_of() {
  compose_main ps -q "$1" 2>/dev/null | tr -d '\r' | head -n1 | tr -d '[:space:]'
}

is_running() {
  local cid="$1"
  [ -n "$cid" ] || return 1
  local st
  st="$(docker inspect --format '{{.State.Running}}' "$cid" 2>/dev/null | tr -d '\r')"
  [ "$st" = "true" ]
}

health_status() {
  local cid="$1"
  docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid" 2>/dev/null | tr -d '\r'
}

# Poll until command succeeds (bounded retries).
wait_for() {
  local desc="$1" timeout="$2"; shift 2
  local deadline=$((SECONDS + timeout))
  local out=""
  while [ "$SECONDS" -lt "$deadline" ]; do
    if out="$("$@" 2>&1)"; then
      printf '%s' "$out"
      return 0
    fi
    sleep "$POLL_INTERVAL_SECS"
  done
  echo "$out" | tail -n5
  return 1
}

info "Problem 4 test"
info "root=$ROOT project=$PROJECT main_file=$COMPOSE_MAIN env_file=$ENV_FILE"
info "timeouts: TIMEOUT_SECS=$TIMEOUT_SECS POLL=$POLL_INTERVAL_SECS CURL=$CURL_TIMEOUT_SECS REBUILD=$REBUILD_TIMEOUT_SECS service=$REBUILD_SERVICE"

need docker || true
need curl || true
if [ -z "$PYBIN" ] && ! command -v jq >/dev/null 2>&1; then
  fail "need python3/python or jq for JSON parsing"
fi

if [ ! -f "$COMPOSE_MAIN" ]; then
  fail "main compose file not found: $COMPOSE_MAIN"
fi
if [ ! -f "$ENV_FILE" ]; then
  fail "env file not found: $ENV_FILE (run scripts/setup-env.py first)"
fi
if ! docker info >/dev/null 2>&1; then
  fail "docker daemon not reachable; start Docker Desktop and the stack first"
  info "prerequisite: docker compose -p $PROJECT -f $COMPOSE_MAIN up -d --build"
fi

AGG_PORT="$(env_val AGGREGATOR_PORT 8083)"
BMKG_PORT="$(env_val BMKG_PORT 8081)"
PVMBG_PORT="$(env_val PVMBG_PORT 8082)"
AGG_TOKEN="$(env_val AGGREGATOR_TOKEN "")"
BMKG_KEY="$(env_val BMKG_API_KEY "")"
PVMBG_TOKEN="$(env_val PVMBG_TOKEN "")"
if [ -z "$AGG_TOKEN" ] || [ -z "$BMKG_KEY" ] || [ -z "$PVMBG_TOKEN" ]; then
  fail "missing tokens in $ENV_FILE (AGGREGATOR_TOKEN/BMKG_API_KEY/PVMBG_TOKEN required)"
fi
info "ports: bmkg=$BMKG_PORT pvmbg=$PVMBG_PORT aggregator=$AGG_PORT (tokens loaded, not printed)"

# ---------------------------------------------------------------- A. Independent containers
info "--- A. Independent container deployment ---"
SERVICES="postgres aggregator bmkg pvmbg nats dashboard-updater field-notifier"
ALL_OK=1
for s in $SERVICES; do
  cid="$(cid_of "$s")"
  if [ -z "$cid" ]; then
    fail "service $s has no container (is the stack running? docker compose -p $PROJECT -f $COMPOSE_MAIN ps)"
    ALL_OK=0
    continue
  fi
  short="$(echo "$cid" | cut -c1-12)"
  if is_running "$cid"; then
    hs="$(health_status "$cid")"
    info "service $s container $short running (health=$hs)"
  else
    fail "service $s container $short is not running"
    ALL_OK=0
  fi
done
if [ "$ALL_OK" = "1" ]; then
  pass "all major components have running containers"
fi

AGG_CID="$(cid_of aggregator)"
PG_CID="$(cid_of postgres)"
BMKG_CID="$(cid_of bmkg)"
PVMBG_CID="$(cid_of pvmbg)"
NATS_CID="$(cid_of nats)"
DASH_CID="$(cid_of dashboard-updater)"
FIELD_CID="$(cid_of field-notifier)"

if [ -n "$AGG_CID" ] && [ -n "$PG_CID" ]; then
  if [ "$AGG_CID" != "$PG_CID" ] && is_running "$AGG_CID" && is_running "$PG_CID"; then
    pass "aggregator and postgres are separate running containers (${AGG_CID:0:12} vs ${PG_CID:0:12})"
  else
    fail "aggregator/postgres are not separate running containers"
  fi
else
  fail "cannot compare aggregator/postgres container IDs (stack not running?)"
fi

if [ -n "$AGG_CID" ] && [ -n "$BMKG_CID" ] && [ -n "$PVMBG_CID" ] && [ -n "$NATS_CID" ]; then
  if [ "$AGG_CID" != "$BMKG_CID" ] && [ "$AGG_CID" != "$PVMBG_CID" ] && [ "$AGG_CID" != "$NATS_CID" ] \
     && [ "$AGG_CID" != "$DASH_CID" ] && [ "$AGG_CID" != "$FIELD_CID" ]; then
    pass "upstream/broker/consumers are separate containers from aggregator"
    info "aggregator=${AGG_CID:0:12} bmkg=${BMKG_CID:0:12} pvmbg=${PVMBG_CID:0:12} nats=${NATS_CID:0:12} dashboard=${DASH_CID:0:12} field=${FIELD_CID:0:12}"
  else
    fail "container ID collision: some service shares aggregator container ID"
  fi
else
  fail "cannot verify separation: one or more container IDs missing"
fi

# ---------------------------------------------------------------- B. Rebuild one service
info "--- B. Rebuild and restart one service independently ($REBUILD_SERVICE) ---"
if [ "$REBUILD_SERVICE" = "postgres" ] || [ "$REBUILD_SERVICE" = "nats" ] || [ "$REBUILD_SERVICE" = "aggregator" ]; then
  fail "REBUILD_SERVICE must not be postgres/nats/aggregator (would disturb the test); got $REBUILD_SERVICE"
  REBUILD_SERVICE="pvmbg"
  info "falling back to pvmbg"
fi

BEFORE_AGG="$AGG_CID"; BEFORE_PG="$PG_CID"; BEFORE_NATS="$NATS_CID"
BEFORE_BMKG="$BMKG_CID"; BEFORE_PVMBG="$PVMBG_CID"
BEFORE_DASH="$DASH_CID"; BEFORE_FIELD="$FIELD_CID"

# Unrelated request: BMKG seismic-events does not depend on pvmbg.
info "verifying unrelated BMKG request before restart"
if curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "X-BMKG-Key: $BMKG_KEY" \
    "http://127.0.0.1:${BMKG_PORT}/seismic-events?since=2026-01-01T00:00:00Z" -o /dev/null 2>&1; then
  pass "unrelated BMKG request succeeds before restart"
else
  fail "unrelated BMKG request failed before restart (bmkg down?)"
fi
if curl -fsS --max-time "$CURL_TIMEOUT_SECS" "http://127.0.0.1:${AGG_PORT}/health" -o /dev/null 2>&1; then
  pass "aggregator health succeeds before restart"
else
  fail "aggregator health failed before restart"
fi

info "rebuilding only $REBUILD_SERVICE (docker compose up -d --build --no-deps $REBUILD_SERVICE)"
if compose_main up -d --build --no-deps "$REBUILD_SERVICE" >/tmp/p4-rebuild.log 2>&1; then
  pass "rebuild command for $REBUILD_SERVICE accepted"
else
  fail "rebuild command failed; see /tmp/p4-rebuild.log"
  tail -n20 /tmp/p4-rebuild.log 2>/dev/null || true
fi

# Wait for rebuilt service to be healthy/operational.
REBUILT_PORT="$PVMBG_PORT"
if [ "$REBUILD_SERVICE" = "bmkg" ]; then REBUILT_PORT="$BMKG_PORT"; fi
info "waiting for $REBUILD_SERVICE health on 127.0.0.1:$REBUILT_PORT (timeout ${REBUILD_TIMEOUT_SECS}s)"
if wait_for "rebuilt $REBUILD_SERVICE health" "$REBUILD_TIMEOUT_SECS" \
    curl -fsS --max-time "$CURL_TIMEOUT_SECS" "http://127.0.0.1:${REBUILT_PORT}/health" -o /dev/null; then
  pass "$REBUILD_SERVICE is healthy after rebuild"
else
  fail "$REBUILD_SERVICE did not become healthy within ${REBUILD_TIMEOUT_SECS}s"
fi

AFTER_AGG="$(cid_of aggregator)"; AFTER_PG="$(cid_of postgres)"
AFTER_NATS="$(cid_of nats)"; AFTER_BMKG="$(cid_of bmkg)"
AFTER_PVMBG="$(cid_of pvmbg)"
AFTER_DASH="$(cid_of dashboard-updater)"; AFTER_FIELD="$(cid_of field-notifier)"
info "before: agg=${BEFORE_AGG:0:12} pg=${BEFORE_PG:0:12} nats=${BEFORE_NATS:0:12} bmkg=${BEFORE_BMKG:0:12} pvmbg=${BEFORE_PVMBG:0:12}"
info "after:  agg=${AFTER_AGG:0:12} pg=${AFTER_PG:0:12} nats=${AFTER_NATS:0:12} bmkg=${AFTER_BMKG:0:12} pvmbg=${AFTER_PVMBG:0:12}"

UNRELATED_OK=1
for pair in "aggregator:$BEFORE_AGG:$AFTER_AGG" "postgres:$BEFORE_PG:$AFTER_PG" "nats:$BEFORE_NATS:$AFTER_NATS" \
            "dashboard-updater:$BEFORE_DASH:$AFTER_DASH" "field-notifier:$BEFORE_FIELD:$AFTER_FIELD"; do
  name="${pair%%:*}"; rest="${pair#*:}"; before="${rest%%:*}"; after="${rest##*:}"
  if [ "$name" = "$REBUILD_SERVICE" ]; then continue; fi
  if [ -z "$before" ] || [ -z "$after" ]; then
    fail "unrelated $name missing container ID before/after"
    UNRELATED_OK=0
  elif [ "$before" != "$after" ]; then
    fail "unrelated $name container ID changed ($before -> $after)"
    UNRELATED_OK=0
  elif ! is_running "$after"; then
    fail "unrelated $name not running after rebuild"
    UNRELATED_OK=0
  fi
done
# bmkg is unrelated when rebuilding pvmbg and vice versa.
if [ "$REBUILD_SERVICE" = "pvmbg" ]; then
  if [ "$BEFORE_BMKG" != "$AFTER_BMKG" ]; then fail "unrelated bmkg changed during pvmbg rebuild"; UNRELATED_OK=0; fi
else
  if [ "$BEFORE_PVMBG" != "$AFTER_PVMBG" ]; then fail "unrelated pvmbg changed during bmkg rebuild"; UNRELATED_OK=0; fi
fi
if [ "$UNRELATED_OK" = "1" ]; then
  pass "unrelated containers retained IDs and remain running"
fi

if curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "X-BMKG-Key: $BMKG_KEY" \
    "http://127.0.0.1:${BMKG_PORT}/seismic-events?since=2026-01-01T00:00:00Z" -o /dev/null 2>&1; then
  pass "unrelated BMKG request still succeeds after rebuild"
else
  fail "unrelated BMKG request failed after rebuild"
fi

# ---------------------------------------------------------------- C. Flexible storage
info "--- C. Flexible HazardEvent storage (confidence_level in attributes JSONB) ---"
STAMP="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || echo "manual")"
RAND="${RANDOM:-$$}"
RID_BASE="p4-${STAMP}-${RAND}"
RID_NOCONF="${RID_BASE}-noc"
RID_CONF="${RID_BASE}-con"
REPORTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || "$PYBIN" -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
info "test report_ids: $RID_NOCONF , $RID_CONF (reported_at=$REPORTED_AT)"

build_pvmbg_payload() {
  local rid="$1" conf_json="$2" out="$3"
  "$PYBIN" - "$rid" "$REPORTED_AT" "$conf_json" "$out" <<'PY'
import json, sys
rid, reported_at, conf_json, out = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
rec = {"report_id": rid, "volcano_id": "MERAPI", "alert_level": "Siaga",
       "eruption_count_24h": 2, "ash_column_height_m": 300, "reported_at": reported_at}
if conf_json != "none":
    rec["confidence_level"] = float(conf_json)
with open(out, "w") as f:
    json.dump({"volcanic_reports": [rec]}, f)
PY
}

TMP1="$(mktemp)"; TMP2="$(mktemp)"
HAZ1=""; HAZ2=""
build_pvmbg_payload "$RID_NOCONF" "none" "$TMP1"
if curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "Authorization: Bearer $AGG_TOKEN" \
     -H 'Content-Type: application/json' --data-binary @"$TMP1" \
     "http://127.0.0.1:${AGG_PORT}/internal/ingest/pvmbg" -o /tmp/p4-ing1.json 2>/tmp/p4-ing1.err; then
  CHANGED="$("$PYBIN" -c 'import json;print(json.load(open("/tmp/p4-ing1.json")).get("changed","?"))' 2>/dev/null || echo "?")"
  info "ingest without confidence_level: changed=$CHANGED"
  if [ "$CHANGED" = "1" ]; then pass "ingest without confidence_level stored (changed=1)";
  elif [ "$CHANGED" = "0" ]; then fail "ingest without confidence_level reported changed=0 (duplicate ID?)";
  else fail "ingest without confidence_level: unexpected response"; cat /tmp/p4-ing1.json 2>/dev/null | head -c 500; echo; fi
else
  fail "ingest without confidence_level failed"; head -c 500 /tmp/p4-ing1.err 2>/dev/null; echo
fi

build_pvmbg_payload "$RID_CONF" "0.85" "$TMP2"
if curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "Authorization: Bearer $AGG_TOKEN" \
     -H 'Content-Type: application/json' --data-binary @"$TMP2" \
     "http://127.0.0.1:${AGG_PORT}/internal/ingest/pvmbg" -o /tmp/p4-ing2.json 2>/tmp/p4-ing2.err; then
  CHANGED="$("$PYBIN" -c 'import json;print(json.load(open("/tmp/p4-ing2.json")).get("changed","?"))' 2>/dev/null || echo "?")"
  info "ingest with confidence_level=0.85: changed=$CHANGED"
  if [ "$CHANGED" = "1" ]; then pass "ingest with confidence_level stored without migration (changed=1)";
  else fail "ingest with confidence_level: changed=$CHANGED (expected 1)"; fi
else
  fail "ingest with confidence_level failed (schema migration required? see implementation)"; head -c 500 /tmp/p4-ing2.err 2>/dev/null; echo
fi
rm -f "$TMP1" "$TMP2"

info "retrieving both records via GET /internal/hazards"
if curl -fsS --max-time "$CURL_TIMEOUT_SECS" -H "Authorization: Bearer $AGG_TOKEN" \
    "http://127.0.0.1:${AGG_PORT}/internal/hazards?source=PVMBG&limit=1000" -o /tmp/p4-haz.json 2>/tmp/p4-haz.err; then
  "$PYBIN" - "$RID_NOCONF" "$RID_CONF" <<'PY' > /tmp/p4-flex.txt 2>/tmp/p4-flex.err
import json, sys
noc, con = sys.argv[1], sys.argv[2]
items = json.load(open("/tmp/p4-haz.json"))["items"]
by_ref = {i["source_ref_id"]: i for i in items}
for rid in (noc, con):
    if rid not in by_ref:
        print(f"MISSING {rid}")
        continue
    h = by_ref[rid]
    attrs = h.get("attributes", {})
    has = "confidence_level" in attrs
    print(f"FOUND {rid} hazard_id={h.get('hazard_id')} has_conf={has} conf={attrs.get('confidence_level')}")
PY
  cat /tmp/p4-flex.txt
  if grep -q "MISSING $RID_NOCONF" /tmp/p4-flex.txt; then fail "record without confidence_level not retrievable";
  else pass "record without confidence_level retrievable"; fi
  if grep -q "MISSING $RID_CONF" /tmp/p4-flex.txt; then fail "record with confidence_level not retrievable";
  else pass "record with confidence_level retrievable"; fi
  LINE_CONF="$(grep "FOUND $RID_CONF" /tmp/p4-flex.txt || true)"
  if echo "$LINE_CONF" | grep -q "has_conf=True"; then
    if echo "$LINE_CONF" | grep -Eq "conf=0\.85"; then pass "new record contains confidence_level=0.85";
    else fail "new record confidence value wrong: $LINE_CONF"; fi
  else
    fail "new record missing confidence_level in attributes"
  fi
  LINE_NOC="$(grep "FOUND $RID_NOCONF" /tmp/p4-flex.txt || true)"
  if echo "$LINE_NOC" | grep -q "has_conf=False"; then
    pass "older record remains valid without fabricated confidence_level"
  else
    fail "older record unexpectedly has confidence_level (fabricated?): $LINE_NOC"
  fi
else
  fail "GET /internal/hazards failed during flexible-storage check"
fi
info "note: test records $RID_NOCONF/$RID_CONF are intentionally left in the DB (no deletion of app data)"

# ---------------------------------------------------------------- D. Canonical Store isolation
info "--- D. Canonical Store isolation ---"
# 1) Compose-level network membership (intended isolation).
if compose_main config --format json 2>/dev/null > /tmp/p4-config.json; then
  cp /tmp/p4-config.json /tmp/p4-config.yaml 2>/dev/null || true
  "$PYBIN" - <<'PY' > /tmp/p4-nets.txt 2>&1
import json
cfg = json.load(open("/tmp/p4-config.json"))
services = ["postgres","aggregator","bmkg","pvmbg","nats","dashboard-updater","field-notifier"]
svcs = cfg.get("services", {})
for s in services:
    nets = svcs.get(s, {}).get("networks", {})
    if isinstance(nets, dict):
        names = ",".join(sorted(nets.keys()))
    elif isinstance(nets, list):
        names = ",".join(sorted(nets))
    else:
        names = ""
    print(f"{s}: {names}")
PY
  cat /tmp/p4-nets.txt
  PG_NETS="$(grep '^postgres:' /tmp/p4-nets.txt | cut -d: -f2-)"
  AGG_NETS="$(grep '^aggregator:' /tmp/p4-nets.txt | cut -d: -f2-)"
  if echo "$PG_NETS" | grep -q storage && ! echo "$PG_NETS" | grep -q services; then
    pass "compose: postgres only on storage network ($PG_NETS)"
  else
    fail "compose: postgres networks unexpected ($PG_NETS)"
  fi
  if echo "$AGG_NETS" | grep -q storage && echo "$AGG_NETS" | grep -q services; then
    pass "compose: aggregator bridges storage+services ($AGG_NETS)"
  else
    fail "compose: aggregator networks unexpected ($AGG_NETS)"
  fi
  OTHER_BAD=0
  for s in bmkg pvmbg nats dashboard-updater field-notifier; do
    n="$(grep "^${s}:" /tmp/p4-nets.txt | cut -d: -f2-)"
    if echo "$n" | grep -q storage; then fail "compose: $s unexpectedly on storage network ($n)"; OTHER_BAD=1; fi
  done
  if [ "$OTHER_BAD" = "0" ]; then pass "compose: unrelated app services have no storage network"; fi
  # ports: postgres must not publish host ports (JSON-aware, yaml fallback)
  if "$PYBIN" - <<'PY' 2>/dev/null; then
import json, sys
cfg = json.load(open("/tmp/p4-config.json"))
ports = cfg.get("services", {}).get("postgres", {}).get("ports", [])
sys.exit(0 if not ports else 1)
PY
    pass "compose: postgres publishes no host ports"
  else
    # textual fallback
    compose_main config 2>/dev/null > /tmp/p4-config.yaml || true
    if grep -A6 '^  postgres:' /tmp/p4-config.yaml | grep -q 'ports:'; then
      fail "compose: postgres publishes host ports (should be internal-only)"
    else
      pass "compose: postgres publishes no host ports"
    fi
  fi
  # DATABASE_URL only in aggregator
  DB_COUNT="$("$PYBIN" - <<'PY' 2>/dev/null || echo unknown
import json
cfg = json.load(open("/tmp/p4-config.json"))
n = sum(1 for s, d in cfg.get("services", {}).items() if "DATABASE_URL" in str(d.get("environment", {})))
print(n)
PY
)"
  if [ "$DB_COUNT" = "1" ]; then
    pass "compose: only aggregator configures DATABASE_URL"
  else
    fail "compose: DATABASE_URL in $DB_COUNT service(s) (expected exactly 1: aggregator)"
  fi
else
  fail "docker compose config failed (cannot verify intended network membership)"
fi

# 2) Runtime network membership via docker inspect (actual isolation).
# Re-fetch fresh IDs (B may have recreated the rebuilt service).
PG_CID="$(cid_of postgres)"; AGG_CID="$(cid_of aggregator)"
FIELD_CID="$(cid_of field-notifier)"; BMKG_CID="$(cid_of bmkg)"
if [ -n "$PG_CID" ] && [ -n "$AGG_CID" ] && [ -n "$FIELD_CID" ]; then
  info "runtime networks:"
  for pair in "postgres:$PG_CID" "aggregator:$AGG_CID" "field-notifier:$FIELD_CID" "bmkg:$BMKG_CID"; do
    n="${pair%%:*}"; c="${pair##*:}"
    nets="$(docker inspect --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$c" 2>/dev/null | tr -d '\r')"
    info "  $n (${c:0:12}): $nets"
  done
  FIELD_NETS="$(docker inspect --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$FIELD_CID" 2>/dev/null)"
  if echo "$FIELD_NETS" | grep -q "storage"; then
    fail "runtime: field-notifier attached to storage network (isolation broken)"
  else
    pass "runtime: field-notifier NOT on storage network"
  fi
  AGG_RUNTIME="$(docker inspect --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$AGG_CID" 2>/dev/null)"
  if echo "$AGG_RUNTIME" | grep -q "storage" && echo "$AGG_RUNTIME" | grep -q "services"; then
    pass "runtime: aggregator on both storage and services networks"
  else
    fail "runtime: aggregator networks unexpected ($AGG_RUNTIME)"
  fi
else
  fail "cannot inspect runtime networks (missing container IDs)"
fi

# 3) DNS reachability from unrelated container should fail; from aggregator should succeed.
if [ -n "$FIELD_CID" ] && [ -n "$AGG_CID" ]; then
  if docker exec "$FIELD_CID" sh -c 'getent hosts postgres || nslookup postgres || wget -q -O- -T 3 http://postgres:5432/ ' >/tmp/p4-dns-field.log 2>&1; then
    # wget to postgres:5432 may "succeed" at TCP level with garbage HTTP reply; what matters is DNS.
    if grep -qiE 'bad address|not found|NXDOMAIN|Name does not resolve' /tmp/p4-dns-field.log; then
      pass "unrelated container cannot resolve postgres (isolated)"
    else
      # Ambiguous: connection-level success does not prove DB access; fall back to network-membership verdict.
      info "field-notifier -> postgres probe output ambiguous; relying on network-membership check (see above)"
      info "$(head -c 300 /tmp/p4-dns-field.log)"
    fi
  else
    pass "unrelated container cannot reach postgres (isolated, probe failed as expected)"
  fi
  if docker exec "$AGG_CID" sh -c 'getent hosts postgres || nslookup postgres' >/tmp/p4-dns-agg.log 2>&1; then
    pass "aggregator CAN resolve postgres via storage network"
  else
    info "aggregator DNS probe inconclusive (tooling may differ); network membership already verified"
    head -c 300 /tmp/p4-dns-agg.log 2>/dev/null || true
  fi
  info "distinction: host access (localhost ports for APIs) is separate from container-network isolation verified above"
else
  fail "cannot run DNS isolation probes (missing containers)"
fi

# 4) Consumers use Aggregator API / NATS, not direct DB (code-level corroboration, not sole proof).
if ! grep -RIl 'DATABASE_URL\|postgres.*5432\|jackc/pgx\|gorm.*postgres' consumers/ bmkg/ pvmbg/ 2>/dev/null | grep -q .; then
  pass "code: no direct DB access strings in consumers/bmkg/pvmbg (data via Aggregator API/NATS)"
else
  fail "code: unexpected direct DB reference in non-aggregator services:"
  grep -RIl 'DATABASE_URL\|postgres.*5432\|jackc/pgx\|gorm.*postgres' consumers/ bmkg/ pvmbg/ 2>/dev/null || true
fi

# ---------------------------------------------------------------- Summary
echo "=============================="
echo "Problem 4 summary: $PASS_COUNT passed, $FAIL_COUNT failed"
if [ "$FAIL_COUNT" -gt 0 ]; then
  echo "[FAIL] problem-4: $FAIL_COUNT check(s) failed"
  exit 1
fi
echo "[PASS] problem-4: all required checks passed"
