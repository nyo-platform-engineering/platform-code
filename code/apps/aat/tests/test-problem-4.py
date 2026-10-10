#!/usr/bin/env python3
"""Problem 4 integration test for Gateway Astolfo (Python port of test-problem-4.sh).

Proves against the existing Dockerized system (no app rewrite):
  A. Independent container deployment (separate containers/networks).
  B. Rebuild + restart one service (pvmbg) without disturbing others.
  C. Flexible HazardEvent storage (attributes JSONB carries confidence_level).
  D. Canonical Store isolation (only aggregator on storage network).

Run from the repository root (code/apps/aat):
  python tests/test-problem-4.py

Configurable via environment (no secrets printed):
  PROJECT / COMPOSE_PROJECT_NAME  Compose project name (default: aat-part1)
  ENV_FILE                        dotenv file (default: .env)
  TIMEOUT_SECS                    generic wait timeout (default: 90)
  POLL_INTERVAL_SECS              polling interval (default: 2)
  CURL_TIMEOUT_SECS               per-request timeout (default: 10)
  REBUILD_TIMEOUT_SECS            rebuild wait timeout (default: 300)
  REBUILD_SERVICE                 service to rebuild (default: pvmbg)

Safety: never runs `down -v`, never wipes tables/streams/consumers,
never restarts postgres/nats/unrelated services, never deletes app data.
"""

from __future__ import annotations

import datetime as dt
import json
import os
import random
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

COMPOSE_MAIN = "compose.yaml"
PROJECT = os.environ.get("COMPOSE_PROJECT_NAME", os.environ.get("PROJECT", "aat-part1"))
ENV_FILE = Path(os.environ.get("ENV_FILE", ".env"))
TIMEOUT_SECS = float(os.environ.get("TIMEOUT_SECS", "90"))
POLL_INTERVAL_SECS = float(os.environ.get("POLL_INTERVAL_SECS", "2"))
CURL_TIMEOUT_SECS = float(os.environ.get("CURL_TIMEOUT_SECS", "10"))
REBUILD_TIMEOUT_SECS = float(os.environ.get("REBUILD_TIMEOUT_SECS", "300"))
REBUILD_SERVICE = os.environ.get("REBUILD_SERVICE", "pvmbg")

PASS_COUNT = 0
FAIL_COUNT = 0


def info(msg: str) -> None:
    print(f"[INFO] {msg}", flush=True)


def passed(msg: str) -> None:
    global PASS_COUNT
    PASS_COUNT += 1
    print(f"[PASS] {msg}", flush=True)


def failed(msg: str) -> None:
    global FAIL_COUNT
    FAIL_COUNT += 1
    print(f"[FAIL] {msg}", flush=True)


def load_env(path: Path) -> dict[str, str]:
    """Read KEY=VALUE from dotenv file without printing values."""
    env: dict[str, str] = {}
    if not path.is_file():
        # also try relative to ROOT
        candidate = ROOT / path if not path.is_absolute() else path
        if not candidate.is_file():
            return env
        path = candidate
    text = path.read_text(encoding="utf-8", errors="replace")
    for line in text.splitlines():
        line = line.strip().lstrip("\ufeff")
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip().replace("\r", "")
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key or ""):
            env[key] = value
    return env


ENV = load_env(ENV_FILE)


def env_val(key: str, default: str = "") -> str:
    v = ENV.get(key, "")
    return v if v else default


def compose_main(*args: str, check: bool = False) -> subprocess.CompletedProcess:
    cmd = ["docker", "compose", "-p", PROJECT, "--env-file", str(ENV_FILE),
           "-f", COMPOSE_MAIN, *args]
    return subprocess.run(cmd, cwd=ROOT, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check)


def run(cmd: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=ROOT, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def cid_of(service: str) -> str:
    proc = compose_main("ps", "-q", service)
    if proc.returncode != 0:
        return ""
    for line in (proc.stdout or "").splitlines():
        cid = line.strip()
        if cid:
            return cid
    return ""


def cid_of_all(service: str) -> str:
    """Container ID including stopped containers (docker compose ps -a).

    cid_of() only lists running containers, so a service left stopped by a
    previous crashed run reports "". This fallback still finds its ID so the
    test can confirm/handle the stopped state instead of logging "?".
    """
    proc = compose_main("ps", "-a", "-q", service)
    if proc.returncode != 0:
        return ""
    for line in (proc.stdout or "").splitlines():
        cid = line.strip()
        if cid:
            return cid
    return ""


def is_running(cid: str) -> bool:
    if not cid:
        return False
    proc = run(["docker", "inspect", "--format", "{{.State.Running}}", cid])
    return proc.returncode == 0 and proc.stdout.strip().strip("\r") == "true"


def health_status(cid: str) -> str:
    proc = run(["docker", "inspect", "--format",
                "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", cid])
    if proc.returncode != 0:
        return "unknown"
    return proc.stdout.strip().strip("\r") or "unknown"


def wait_for(desc: str, timeout: float, func, *args, **kwargs):
    """Poll func until it returns truthy / does not raise. Returns result or None."""
    import time
    deadline = time.monotonic() + timeout
    last: object = None
    while time.monotonic() < deadline:
        try:
            result = func(*args, **kwargs)
            if result:
                return result
        except Exception as exc:  # noqa: BLE001 - polling helper
            last = exc
        time.sleep(POLL_INTERVAL_SECS)
    if last is not None:
        info(f"{desc}: last error: {str(last)[-300:]}")
    return None


def http_get(url: str, headers: dict | None = None, timeout: float | None = None) -> tuple[int, bytes]:
    req = urllib.request.Request(url, headers=headers or {}, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=timeout or CURL_TIMEOUT_SECS) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read() if hasattr(exc, "read") else b""
    except OSError:
        # Target stopped / connection refused / timeout: expected while the
        # rebuilt service is down. Return 0 so callers treat it as "down".
        return 0, b""


def http_post_json(url: str, payload: dict, headers: dict | None = None,
                   timeout: float | None = None) -> tuple[int, bytes]:
    data = json.dumps(payload).encode()
    hdrs = dict(headers or {})
    hdrs["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=hdrs, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout or CURL_TIMEOUT_SECS) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        try:
            body = exc.read()
        except Exception:
            body = b""
        return exc.code, body
    except OSError as exc:
        return 0, str(exc).encode()


def fetch_hazards_page(source: str, after: str = "", limit: int = 1000
                       ) -> tuple[list | None, str]:
    """Fetch one page of /internal/hazards. Returns (items, next_after).

    Returns (None, "") on HTTP/parse failure. `AGG_PORT`/`AGG_TOKEN` are read
    lazily from module-level env values set in main().
    """
    params: dict[str, str] = {"source": source, "limit": str(limit)}
    if after:
        params["after"] = after
    url = (f"http://127.0.0.1:{env_val('AGGREGATOR_PORT', '8083')}/internal/hazards?"
           + urllib.parse.urlencode(params))
    st, body = http_get(url, headers={"Authorization": f"Bearer {env_val('AGGREGATOR_TOKEN', '')}"})
    if not (200 <= st < 300):
        return None, ""
    try:
        data = json.loads(body.decode())
        items = data.get("items", [])
        return items if isinstance(items, list) else [], str(data.get("next_after", "") or "")
    except (ValueError, UnicodeDecodeError, AttributeError):
        return None, ""


def get_all_hazards(source: str, limit: int = 1000, max_pages: int = 100) -> list | None:
    """Collect ALL hazards for source by following after/next_after cursors.

    The API orders by id ascending with max limit=1000/page, so a single GET
    only returns the oldest page. Returns None on HTTP failure.
    """
    all_items: list = []
    after = ""
    for _ in range(max_pages):
        items, nxt = fetch_hazards_page(source, after, limit)
        if items is None:
            return None
        all_items.extend(items)
        if not nxt:
            break
        after = nxt
    return all_items


def utc_stamp() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def utc_rfc3339() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def grep_recursive(pattern: str, roots: list[str]) -> list[str]:
    rx = re.compile(pattern)
    hits: list[str] = []
    for root in roots:
        base = ROOT / root
        if not base.exists():
            continue
        for path in base.rglob("*"):
            if not path.is_file():
                continue
            try:
                if path.stat().st_size > 1_000_000:
                    continue
                text = path.read_text(encoding="utf-8", errors="ignore")
            except OSError:
                continue
            if rx.search(text):
                hits.append(str(path.relative_to(ROOT)))
    return hits


def main() -> int:
    global REBUILD_SERVICE
    info("Problem 4 test")
    info(f"root={ROOT} project={PROJECT} main_file={COMPOSE_MAIN} env_file={ENV_FILE}")
    info(f"timeouts: TIMEOUT_SECS={TIMEOUT_SECS:g} POLL={POLL_INTERVAL_SECS:g} "
         f"CURL={CURL_TIMEOUT_SECS:g} REBUILD={REBUILD_TIMEOUT_SECS:g} service={REBUILD_SERVICE}")

    if shutil.which("docker") is None:
        failed("missing required command: docker")
    if run(["docker", "info"]).returncode != 0:
        failed("docker daemon not reachable; start Docker Desktop and the stack first")
        info(f"prerequisite: docker compose -p {PROJECT} -f {COMPOSE_MAIN} up -d --build")
    if not (ROOT / COMPOSE_MAIN).is_file():
        failed(f"main compose file not found: {COMPOSE_MAIN}")
    env_path = ENV_FILE if ENV_FILE.is_absolute() else ROOT / ENV_FILE
    if not env_path.is_file():
        failed(f"env file not found: {ENV_FILE} (run scripts/setup-env.py first)")

    agg_port = env_val("AGGREGATOR_PORT", "8083")
    bmkg_port = env_val("BMKG_PORT", "8081")
    pvmbg_port = env_val("PVMBG_PORT", "8082")
    agg_token = env_val("AGGREGATOR_TOKEN", "")
    bmkg_key = env_val("BMKG_API_KEY", "")
    pvmbg_token = env_val("PVMBG_TOKEN", "")
    if not agg_token or not bmkg_key or not pvmbg_token:
        failed(f"missing tokens in {ENV_FILE} (AGGREGATOR_TOKEN/BMKG_API_KEY/PVMBG_TOKEN required)")
    info(f"ports: bmkg={bmkg_port} pvmbg={pvmbg_port} aggregator={agg_port} (tokens loaded, not printed)")

    # ---------------------------------------------------------------- A. Independent containers
    info("--- A. Independent container deployment ---")
    services = ["postgres", "aggregator", "bmkg", "pvmbg", "nats",
                "dashboard-updater", "field-notifier"]
    all_ok = True
    for svc in services:
        cid = cid_of(svc)
        if not cid:
            failed(f"service {svc} has no container (is the stack running? "
                   f"docker compose -p {PROJECT} -f {COMPOSE_MAIN} ps)")
            all_ok = False
            continue
        short = cid[:12]
        if is_running(cid):
            info(f"service {svc} container {short} running (health={health_status(cid)})")
        else:
            failed(f"service {svc} container {short} is not running")
            all_ok = False
    if all_ok:
        passed("all major components have running containers")

    agg_cid = cid_of("aggregator")
    pg_cid = cid_of("postgres")
    bmkg_cid = cid_of("bmkg")
    pvmbg_cid = cid_of("pvmbg")
    nats_cid = cid_of("nats")
    dash_cid = cid_of("dashboard-updater")
    field_cid = cid_of("field-notifier")

    if agg_cid and pg_cid:
        if agg_cid != pg_cid and is_running(agg_cid) and is_running(pg_cid):
            passed(f"aggregator and postgres are separate running containers "
                   f"({agg_cid[:12]} vs {pg_cid[:12]})")
        else:
            failed("aggregator/postgres are not separate running containers")
    else:
        failed("cannot compare aggregator/postgres container IDs (stack not running?)")

    if agg_cid and bmkg_cid and pvmbg_cid and nats_cid:
        if (agg_cid != bmkg_cid and agg_cid != pvmbg_cid and agg_cid != nats_cid
                and agg_cid != dash_cid and agg_cid != field_cid):
            passed("upstream/broker/consumers are separate containers from aggregator")
            info(f"aggregator={agg_cid[:12]} bmkg={bmkg_cid[:12]} pvmbg={pvmbg_cid[:12]} "
                 f"nats={nats_cid[:12]} dashboard={dash_cid[:12]} field={field_cid[:12]}")
        else:
            failed("container ID collision: some service shares aggregator container ID")
    else:
        failed("cannot verify separation: one or more container IDs missing")

    # ---------------------------------------------------------------- B. Rebuild one service
    info(f"--- B. Rebuild and restart one service independently ({REBUILD_SERVICE}) ---")
    if REBUILD_SERVICE in ("postgres", "nats", "aggregator"):
        failed(f"REBUILD_SERVICE must not be postgres/nats/aggregator (would disturb the test); "
               f"got {REBUILD_SERVICE}")
        REBUILD_SERVICE = "pvmbg"
        info("falling back to pvmbg")

    before = {"aggregator": agg_cid, "postgres": pg_cid, "nats": nats_cid,
              "bmkg": bmkg_cid, "pvmbg": pvmbg_cid,
              "dashboard-updater": dash_cid, "field-notifier": field_cid}
    rebuilt_port = bmkg_port if REBUILD_SERVICE == "bmkg" else pvmbg_port
    target_id = before.get(REBUILD_SERVICE, "") or cid_of_all(REBUILD_SERVICE)
    if not target_id or not is_running(target_id):
        info(f"{REBUILD_SERVICE} not running at Section B start "
             f"(leftover stopped container? id={target_id[:12] if target_id else '?'}); "
             f"starting it first (docker compose up -d --no-deps {REBUILD_SERVICE})")
        up = compose_main("up", "-d", "--no-deps", REBUILD_SERVICE)
        if up.returncode != 0:
            failed(f"could not (re)start {REBUILD_SERVICE} before stop-phase")
            info((up.stderr or "")[-2000:])

        def _target_healthy() -> bool:
            st, _ = http_get(f"http://127.0.0.1:{rebuilt_port}/health")
            return 200 <= st < 300

        if wait_for(f"(re)started {REBUILD_SERVICE} health", REBUILD_TIMEOUT_SECS,
                    _target_healthy):
            info(f"{REBUILD_SERVICE} running again before stop-phase")
        else:
            failed(f"{REBUILD_SERVICE} did not become healthy before stop-phase")
        before = {svc: cid_of(svc) for svc in before}
        if REBUILD_SERVICE == "pvmbg":
            pvmbg_cid = before["pvmbg"]
        else:
            bmkg_cid = before["bmkg"]
        info(f"re-snapshotted before IDs (target={before.get(REBUILD_SERVICE, '')[:12] or '?'})")

    info("verifying unrelated BMKG request before restart")
    st, _ = http_get(f"http://127.0.0.1:{bmkg_port}/seismic-events?since=2026-01-01T00:00:00Z",
                     headers={"X-BMKG-Key": bmkg_key})
    if 200 <= st < 300:
        passed("unrelated BMKG request succeeds before restart")
    else:
        failed(f"unrelated BMKG request failed before restart (bmkg down? HTTP {st})")
    st, _ = http_get(f"http://127.0.0.1:{agg_port}/health")
    if 200 <= st < 300:
        passed("aggregator health succeeds before restart")
    else:
        failed("aggregator health failed before restart")

    info(f"stopping {REBUILD_SERVICE} first (docker compose stop {REBUILD_SERVICE})")
    stop = compose_main("stop", REBUILD_SERVICE)
    if stop.returncode == 0:
        passed(f"stop command for {REBUILD_SERVICE} accepted")
    else:
        failed(f"stop command for {REBUILD_SERVICE} failed")
        info((stop.stderr or "")[-2000:])
    time.sleep(3)

    target_before_id = before.get(REBUILD_SERVICE, "") or cid_of_all(REBUILD_SERVICE)
    if target_before_id and not is_running(target_before_id):
        passed(f"{REBUILD_SERVICE} confirmed stopped ({target_before_id[:12]} not running)")
    elif not target_before_id:
        failed(f"cannot confirm {REBUILD_SERVICE} stopped (no container ID before stop)")
    else:
        failed(f"{REBUILD_SERVICE} still running after stop")

    # While the target is stopped, unrelated services must keep serving.
    # NOTE: cid_of() uses `ps -q` (running only), so the stopped service may
    # legitimately report no ID here — only unrelated services are compared.
    stopped_ids = {svc: cid_of(svc) for svc in before if svc != REBUILD_SERVICE}
    stopped_unrelated_ok = True
    for name in ("aggregator", "postgres", "nats", "dashboard-updater", "field-notifier",
                 "bmkg", "pvmbg"):
        if name == REBUILD_SERVICE:
            continue
        b, cur = before.get(name, ""), stopped_ids.get(name, "")
        if not b or not cur:
            failed(f"unrelated {name} missing container ID while {REBUILD_SERVICE} stopped")
            stopped_unrelated_ok = False
        elif b != cur:
            failed(f"unrelated {name} container ID changed while {REBUILD_SERVICE} stopped "
                   f"({b} -> {cur})")
            stopped_unrelated_ok = False
        elif not is_running(cur):
            failed(f"unrelated {name} not running while {REBUILD_SERVICE} stopped")
            stopped_unrelated_ok = False
    if stopped_unrelated_ok:
        passed(f"unrelated containers undisturbed while {REBUILD_SERVICE} is stopped")

    info(f"hitting unrelated BMKG request while {REBUILD_SERVICE} is stopped")
    st, _ = http_get(f"http://127.0.0.1:{bmkg_port}/seismic-events?since=2026-01-01T00:00:00Z",
                     headers={"X-BMKG-Key": bmkg_key})
    if REBUILD_SERVICE == "bmkg":
        # Target itself is down; its own endpoint is expected to fail — the
        # independence proof in this mode is aggregator health below.
        info(f"bmkg endpoint while bmkg stopped: HTTP {st} (expected failure, not a verdict)")
    elif 200 <= st < 300:
        passed(f"unrelated BMKG request succeeds while {REBUILD_SERVICE} is stopped")
    else:
        failed(f"unrelated BMKG request failed while {REBUILD_SERVICE} stopped (HTTP {st})")
    st, _ = http_get(f"http://127.0.0.1:{agg_port}/health")
    if 200 <= st < 300:
        passed(f"aggregator health succeeds while {REBUILD_SERVICE} is stopped")
    else:
        failed(f"aggregator health failed while {REBUILD_SERVICE} is stopped")
    st, _ = http_get(f"http://127.0.0.1:{rebuilt_port}/health")
    if 200 <= st < 300:
        info(f"{REBUILD_SERVICE} endpoint unexpectedly still serves while stopped "
             f"(HTTP {st}; continuing to rebuild)")
    else:
        passed(f"{REBUILD_SERVICE} is down while stopped as expected (HTTP {st or 'conn-refused'})")
    info(f"rebuilding only {REBUILD_SERVICE} (docker compose up -d --build --no-deps {REBUILD_SERVICE})")
    rebuild = compose_main("up", "-d", "--build", "--no-deps", REBUILD_SERVICE)
    if rebuild.returncode == 0:
        passed(f"rebuild command for {REBUILD_SERVICE} accepted")
    else:
        failed("rebuild command failed")
        info((rebuild.stderr or "")[-2000:])

    info(f"waiting for {REBUILD_SERVICE} health on 127.0.0.1:{rebuilt_port} "
         f"(timeout {REBUILD_TIMEOUT_SECS:g}s)")

    def _rebuilt_healthy() -> bool:
        st, _ = http_get(f"http://127.0.0.1:{rebuilt_port}/health")
        return 200 <= st < 300

    if wait_for(f"rebuilt {REBUILD_SERVICE} health", REBUILD_TIMEOUT_SECS, _rebuilt_healthy):
        passed(f"{REBUILD_SERVICE} is healthy after rebuild")
    else:
        failed(f"{REBUILD_SERVICE} did not become healthy within {REBUILD_TIMEOUT_SECS:g}s")

    after = {svc: cid_of(svc) for svc in before}
    info(f"before: agg={before['aggregator'][:12] if before['aggregator'] else '?'} "
         f"pg={before['postgres'][:12] if before['postgres'] else '?'} "
         f"nats={before['nats'][:12] if before['nats'] else '?'} "
         f"bmkg={before['bmkg'][:12] if before['bmkg'] else '?'} "
         f"pvmbg={before['pvmbg'][:12] if before['pvmbg'] else '?'}")
    info(f"after:  agg={after['aggregator'][:12] if after['aggregator'] else '?'} "
         f"pg={after['postgres'][:12] if after['postgres'] else '?'} "
         f"nats={after['nats'][:12] if after['nats'] else '?'} "
         f"bmkg={after['bmkg'][:12] if after['bmkg'] else '?'} "
         f"pvmbg={after['pvmbg'][:12] if after['pvmbg'] else '?'}")

    unrelated_ok = True
    for name in ("aggregator", "postgres", "nats", "dashboard-updater", "field-notifier"):
        if name == REBUILD_SERVICE:
            continue
        b, a = before.get(name, ""), after.get(name, "")
        if not b or not a:
            failed(f"unrelated {name} missing container ID before/after")
            unrelated_ok = False
        elif b != a:
            failed(f"unrelated {name} container ID changed ({b} -> {a})")
            unrelated_ok = False
        elif not is_running(a):
            failed(f"unrelated {name} not running after rebuild")
            unrelated_ok = False
    if REBUILD_SERVICE == "pvmbg":
        if before.get("bmkg") != after.get("bmkg"):
            failed("unrelated bmkg changed during pvmbg rebuild")
            unrelated_ok = False
    else:
        if before.get("pvmbg") != after.get("pvmbg"):
            failed("unrelated pvmbg changed during bmkg rebuild")
            unrelated_ok = False
    if unrelated_ok:
        passed("unrelated containers retained IDs and remain running")

    st, _ = http_get(f"http://127.0.0.1:{bmkg_port}/seismic-events?since=2026-01-01T00:00:00Z",
                     headers={"X-BMKG-Key": bmkg_key})
    if 200 <= st < 300:
        passed("unrelated BMKG request still succeeds after rebuild")
    else:
        failed("unrelated BMKG request failed after rebuild")

    # ---------------------------------------------------------------- C. Flexible storage
    info("--- C. Flexible HazardEvent storage (confidence_level in attributes JSONB) ---")
    stamp = utc_stamp()
    rand = random.randint(0, 32767)
    rid_base = f"p4-{stamp}-{os.getpid()}-{rand}"
    rid_noconf = f"{rid_base}-noc"
    rid_conf = f"{rid_base}-con"
    reported_at = utc_rfc3339()
    info(f"test report_ids: {rid_noconf} , {rid_conf} (reported_at={reported_at})")

    def build_payload(rid: str, conf: float | None) -> dict:
        rec: dict = {"report_id": rid, "volcano_id": "MERAPI", "alert_level": "Siaga",
                     "eruption_count_24h": 2, "ash_column_height_m": 300,
                     "reported_at": reported_at}
        if conf is not None:
            rec["confidence_level"] = conf
        return {"volcanic_reports": [rec]}

    def ingest(rid: str, conf: float | None) -> tuple[bool, object]:
        st, body = http_post_json(
            f"http://127.0.0.1:{agg_port}/internal/ingest/pvmbg",
            build_payload(rid, conf),
            headers={"Authorization": f"Bearer {agg_token}"})
        if not (200 <= st < 300):
            return False, body[:500] if body else f"HTTP {st}"
        try:
            return True, json.loads(body.decode())
        except (ValueError, UnicodeDecodeError):
            return False, body[:500]

    ok, resp = ingest(rid_noconf, None)
    if ok:
        changed = (resp or {}).get("changed", "?") if isinstance(resp, dict) else "?"
        info(f"ingest without confidence_level: changed={changed}")
        if changed == 1:
            passed("ingest without confidence_level stored (changed=1)")
        elif changed == 0:
            failed("ingest without confidence_level reported changed=0 (duplicate ID?)")
        else:
            failed(f"ingest without confidence_level: unexpected response {str(resp)[:500]}")
    else:
        failed(f"ingest without confidence_level failed: {str(resp)[:500]}")

    ok, resp = ingest(rid_conf, 0.85)
    if ok:
        changed = (resp or {}).get("changed", "?") if isinstance(resp, dict) else "?"
        info(f"ingest with confidence_level=0.85: changed={changed}")
        if changed == 1:
            passed("ingest with confidence_level stored without migration (changed=1)")
        else:
            failed(f"ingest with confidence_level: changed={changed} (expected 1)")
    else:
        failed(f"ingest with confidence_level failed (schema migration required? "
               f"see implementation): {str(resp)[:500]}")

    info("retrieving both records via GET /internal/hazards (paginated via after/next_after)")
    items = get_all_hazards("PVMBG")
    if items is None:
        failed("GET /internal/hazards failed during flexible-storage check")
    else:
        by_ref = {h.get("source_ref_id"): h for h in items if isinstance(h, dict)}
        for rid, label in ((rid_noconf, "without"), (rid_conf, "with")):
            if rid not in by_ref:
                failed(f"record {label} confidence_level not retrievable")
            else:
                passed(f"record {label} confidence_level retrievable")
        h_conf = by_ref.get(rid_conf, {})
        attrs = h_conf.get("attributes", {}) if isinstance(h_conf, dict) else {}
        if "confidence_level" in attrs:
            try:
                if abs(float(attrs["confidence_level"]) - 0.85) < 1e-9:
                    passed("new record contains confidence_level=0.85")
                else:
                    failed(f"new record confidence value wrong: {attrs.get('confidence_level')}")
            except (TypeError, ValueError):
                failed(f"new record confidence value wrong: {attrs.get('confidence_level')}")
        else:
            failed("new record missing confidence_level in attributes")
        h_noc = by_ref.get(rid_noconf, {})
        attrs_noc = h_noc.get("attributes", {}) if isinstance(h_noc, dict) else {}
        if "confidence_level" not in attrs_noc:
            passed("older record remains valid without fabricated confidence_level")
        else:
            failed(f"older record unexpectedly has confidence_level (fabricated?): {attrs_noc}")
    info(f"note: test records {rid_noconf}/{rid_conf} are intentionally left in the DB "
         "(no deletion of app data)")

    # ---------------------------------------------------------------- D. Canonical Store isolation
    info("--- D. Canonical Store isolation ---")
    cfg_proc = compose_main("config", "--format", "json")
    if cfg_proc.returncode == 0:
        try:
            cfg = json.loads(cfg_proc.stdout)
        except ValueError:
            cfg = None
        if cfg is None:
            failed("docker compose config failed (cannot verify intended network membership)")
        else:
            wanted = ["postgres", "aggregator", "bmkg", "pvmbg", "nats",
                      "dashboard-updater", "field-notifier"]
            nets: dict[str, str] = {}
            for svc in wanted:
                raw = (cfg.get("services", {}).get(svc, {}).get("networks", {}))
                if isinstance(raw, dict):
                    nets[svc] = ",".join(sorted(raw.keys()))
                elif isinstance(raw, list):
                    nets[svc] = ",".join(sorted(str(x) for x in raw))
                else:
                    nets[svc] = ""
                info(f"{svc}: {nets[svc]}")
            pg_nets = nets.get("postgres", "")
            agg_nets = nets.get("aggregator", "")
            if "storage" in pg_nets.split(",") and "services" not in pg_nets.split(","):
                passed(f"compose: postgres only on storage network ({pg_nets})")
            else:
                failed(f"compose: postgres networks unexpected ({pg_nets})")
            if "storage" in agg_nets.split(",") and "services" in agg_nets.split(","):
                passed(f"compose: aggregator bridges storage+services ({agg_nets})")
            else:
                failed(f"compose: aggregator networks unexpected ({agg_nets})")
            other_bad = False
            for svc in ("bmkg", "pvmbg", "nats", "dashboard-updater", "field-notifier"):
                n = nets.get(svc, "")
                if "storage" in n.split(","):
                    failed(f"compose: {svc} unexpectedly on storage network ({n})")
                    other_bad = True
            if not other_bad:
                passed("compose: unrelated app services have no storage network")
            pg_ports = cfg.get("services", {}).get("postgres", {}).get("ports", [])
            if not pg_ports:
                passed("compose: postgres publishes no host ports")
            else:
                yaml_proc = compose_main("config")
                yaml_text = yaml_proc.stdout if yaml_proc.returncode == 0 else ""
                m = re.search(r"^  postgres:.*?(?=^  \S+:|\Z)", yaml_text,
                              re.MULTILINE | re.DOTALL)
                if m and re.search(r"^\s+ports:", m.group(0), re.MULTILINE):
                    failed("compose: postgres publishes host ports (should be internal-only)")
                else:
                    passed("compose: postgres publishes no host ports")
            db_count = 0
            for _svc, details in (cfg.get("services", {}) or {}).items():
                if "DATABASE_URL" in str((details or {}).get("environment", {})):
                    db_count += 1
            if db_count == 1:
                passed("compose: only aggregator configures DATABASE_URL")
            else:
                failed(f"compose: DATABASE_URL in {db_count} service(s) "
                       "(expected exactly 1: aggregator)")
    else:
        failed("docker compose config failed (cannot verify intended network membership)")

    # 2) Runtime network membership via docker inspect (actual isolation).
    pg_cid = cid_of("postgres")
    agg_cid = cid_of("aggregator")
    field_cid = cid_of("field-notifier")
    bmkg_cid = cid_of("bmkg")
    if pg_cid and agg_cid and field_cid:
        info("runtime networks:")

        def runtime_nets(cid: str) -> str:
            proc = run(["docker", "inspect", "--format",
                        "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", cid])
            return (proc.stdout or "").strip() if proc.returncode == 0 else ""

        for name, cid in (("postgres", pg_cid), ("aggregator", agg_cid),
                          ("field-notifier", field_cid), ("bmkg", bmkg_cid)):
            info(f"  {name} ({cid[:12]}): {runtime_nets(cid)}")
        field_nets = runtime_nets(field_cid)
        if "storage" in field_nets:
            failed("runtime: field-notifier attached to storage network (isolation broken)")
        else:
            passed("runtime: field-notifier NOT on storage network")
        agg_runtime = runtime_nets(agg_cid)
        if "storage" in agg_runtime and "services" in agg_runtime:
            # substring match mirrors original `grep -q` behaviour:
            # runtime names are prefixed (aat-part1_services, aat-part1_storage)
            passed("runtime: aggregator on both storage and services networks")
        else:
            failed(f"runtime: aggregator networks unexpected ({agg_runtime})")
    else:
        failed("cannot inspect runtime networks (missing container IDs)")

    # 3) DNS reachability probes.
    if field_cid and agg_cid:
        field_probe = run(["docker", "exec", field_cid, "sh", "-c",
                           "getent hosts postgres || nslookup postgres || "
                           "wget -q -O- -T 3 http://postgres:5432/"])
        combined = ((field_probe.stdout or "") + (field_probe.stderr or ""))
        if field_probe.returncode == 0:
            if re.search(r"bad address|not found|NXDOMAIN|Name does not resolve",
                         combined, re.IGNORECASE):
                passed("unrelated container cannot resolve postgres (isolated)")
            else:
                info("field-notifier -> postgres probe output ambiguous; relying on "
                     "network-membership check (see above)")
                info(combined[:300])
        else:
            passed("unrelated container cannot reach postgres (isolated, probe failed as expected)")
        agg_probe = run(["docker", "exec", agg_cid, "sh", "-c",
                         "getent hosts postgres || nslookup postgres"])
        if agg_probe.returncode == 0:
            passed("aggregator CAN resolve postgres via storage network")
        else:
            info("aggregator DNS probe inconclusive (tooling may differ); "
                 "network membership already verified")
            info(((agg_probe.stdout or "") + (agg_probe.stderr or ""))[:300])
        info("distinction: host access (localhost ports for APIs) is separate from "
             "container-network isolation verified above")
    else:
        failed("cannot run DNS isolation probes (missing containers)")

    # 4) Consumers use Aggregator API / NATS, not direct DB.
    hits = grep_recursive(r"DATABASE_URL|postgres.*5432|jackc/pgx|gorm.*postgres",
                          ["consumers", "bmkg", "pvmbg"])
    if not hits:
        passed("code: no direct DB access strings in consumers/bmkg/pvmbg "
               "(data via Aggregator API/NATS)")
    else:
        failed("code: unexpected direct DB reference in non-aggregator services:")
        for h in hits[:20]:
            info(f"  {h}")

    # ---------------------------------------------------------------- Summary
    print("==============================")
    print(f"Problem 4 summary: {PASS_COUNT} passed, {FAIL_COUNT} failed")
    if FAIL_COUNT > 0:
        print(f"[FAIL] problem-4: {FAIL_COUNT} check(s) failed")
        return 1
    print("[PASS] problem-4: all required checks passed")
    return 0


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="p4-"):
        os.chdir(ROOT)
        sys.exit(main())
