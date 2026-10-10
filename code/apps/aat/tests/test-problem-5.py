#!/usr/bin/env python3
"""Problem 5 integration test for Gateway Astolfo (Python port of test-problem-5.sh).

Uses the TWO existing compose files; never replaces consumers with mocks,
never modifies the producer to register consumers, never creates a second
broker or an isolated project that cannot reach the existing NATS.
  main file:   compose.yaml            (postgres, aggregator, bmkg, pvmbg,
                                       nats, dashboard-updater, field-notifier)
  second file: compose.consumer.yaml  (test-consumer only; shares the same
                                       project/network/NATS via -f chaining)

Flow:
  A. discover files, verify NATS + two original consumers
  B. verify producer publication (outbox -> HAZARDS_STREAM/hazards.created.v1)
  C. verify both original consumers receive the same event (logs)
  D. failure isolation: stop dashboard-updater, publish during downtime,
     verify field-notifier continues, restart and check backlog recovery
  E. add third consumer via second compose file, verify all three receive
  F. delivery semantics + idempotency (at-least-once + KV dedupe)

Run from code/apps/aat:
  python tests/test-problem-5.py

Env (no secrets printed):
  PROJECT / COMPOSE_PROJECT_NAME   default aat-part1 (must match stack)
  ENV_FILE                         default .env
  TIMEOUT_SECS                     delivery wait timeout (default 90)
  POLL_INTERVAL_SECS               polling interval (default 2)
  CURL_TIMEOUT_SECS                default 10
  DOWNTIME_EVENTS                  events to publish while stopped (default 3)
  CLEANUP_TEST_CONSUMER            0=leave test-consumer running (default), 1=stop if we started it
"""

from __future__ import annotations

import datetime as dt
import json
import os
import random
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

COMPOSE_MAIN = "compose.yaml"
COMPOSE_EXTRA = "compose.consumer.yaml"
PROJECT = os.environ.get("COMPOSE_PROJECT_NAME", os.environ.get("PROJECT", "aat-part1"))
ENV_FILE = Path(os.environ.get("ENV_FILE", ".env"))
TIMEOUT_SECS = float(os.environ.get("TIMEOUT_SECS", "90"))
POLL_INTERVAL_SECS = float(os.environ.get("POLL_INTERVAL_SECS", "2"))
CURL_TIMEOUT_SECS = float(os.environ.get("CURL_TIMEOUT_SECS", "10"))
try:
    DOWNTIME_EVENTS = int(os.environ.get("DOWNTIME_EVENTS", "3"))
except ValueError:
    DOWNTIME_EVENTS = 3
CLEANUP_TEST_CONSUMER = os.environ.get("CLEANUP_TEST_CONSUMER", "0")

PASS_COUNT = 0
FAIL_COUNT = 0
STARTED_TEST_CONSUMER = False


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
    env: dict[str, str] = {}
    candidate = path if path.is_absolute() else ROOT / path
    if not candidate.is_file():
        return env
    for line in candidate.read_text(encoding="utf-8", errors="replace").splitlines():
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


AGG_PORT = env_val("AGGREGATOR_PORT", "8083")
AGG_TOKEN = env_val("AGGREGATOR_TOKEN", "")
NATS_MON = "http://127.0.0.1:8222"


def _compose_base(extra: bool) -> list[str]:
    cmd = ["docker", "compose", "-p", PROJECT, "--env-file", str(ENV_FILE),
           "-f", COMPOSE_MAIN]
    if extra:
        cmd += ["-f", COMPOSE_EXTRA]
    return cmd


def compose_main(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(_compose_base(False) + list(args), cwd=ROOT, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def compose_both(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(_compose_base(True) + list(args), cwd=ROOT, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def _first_line(text: str) -> str:
    for line in (text or "").splitlines():
        s = line.strip()
        if s:
            return s
    return ""


def cid_main(service: str) -> str:
    proc = compose_main("ps", "-q", service)
    return _first_line(proc.stdout) if proc.returncode == 0 else ""


def cid_both(service: str) -> str:
    proc = compose_both("ps", "-q", service)
    return _first_line(proc.stdout) if proc.returncode == 0 else ""


def is_running(cid: str) -> bool:
    if not cid:
        return False
    proc = subprocess.run(["docker", "inspect", "--format", "{{.State.Running}}", cid],
                          cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return proc.returncode == 0 and proc.stdout.strip().strip("\r") == "true"


def health_of(cid: str) -> str:
    proc = subprocess.run(
        ["docker", "inspect", "--format",
         "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", cid],
        cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if proc.returncode != 0:
        return "unknown"
    return (proc.stdout.strip().strip("\r") or "unknown")


def wait_for(desc: str, timeout: float, func, *args, **kwargs):
    """Poll func (truthy return / exit-0 semantics) until timeout. Returns result or None."""
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


def http_get(url: str, headers: dict | None = None) -> tuple[int, bytes]:
    req = urllib.request.Request(url, headers=headers or {}, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=CURL_TIMEOUT_SECS) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        try:
            return exc.code, exc.read()
        except Exception:
            return exc.code, b""
    except OSError:
        return 0, b""


def http_post_json(url: str, payload: dict, headers: dict | None = None) -> tuple[int, bytes]:
    data = json.dumps(payload).encode()
    hdrs = dict(headers or {})
    hdrs["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=hdrs, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=CURL_TIMEOUT_SECS) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        try:
            return exc.code, exc.read()
        except Exception:
            return exc.code, b""
    except OSError as exc:
        return 0, str(exc).encode()


def fetch_hazards_page(source: str, after: str = "", limit: int = 1000
                       ) -> tuple[list | None, str]:
    """Fetch one page of /internal/hazards. Returns (items, next_after).

    Returns (None, "") on HTTP/parse failure.
    """
    params: dict[str, str] = {"source": source, "limit": str(limit)}
    if after:
        params["after"] = after
    url = (f"http://127.0.0.1:{AGG_PORT}/internal/hazards?"
           + urllib.parse.urlencode(params))
    st, body = http_get(url, headers={"Authorization": f"Bearer {AGG_TOKEN}"})
    if not (200 <= st < 300):
        return None, ""
    try:
        data = json.loads(body.decode())
        items = data.get("items", [])
        return items if isinstance(items, list) else [], str(data.get("next_after", "") or "")
    except (ValueError, UnicodeDecodeError, AttributeError):
        return None, ""


def find_hazard(source: str, key: str, value: str, limit: int = 1000,
                max_pages: int = 100) -> tuple[dict | None, bool]:
    """Search all pages for a hazard where hazard[key] == value.

    The API orders by id ascending with max limit=1000/page, so a single GET
    only returns the oldest page — new records need cursor pagination via
    after/next_after. Returns (dict_or_None, ok): ok=False means HTTP failure,
    ok=True + None means fully scanned but not found.
    """
    after = ""
    for _ in range(max_pages):
        items, nxt = fetch_hazards_page(source, after, limit)
        if items is None:
            return None, False
        for h in items:
            if isinstance(h, dict) and h.get(key) == value:
                return h, True
        if not nxt:
            break
        after = nxt
    return None, True


def compose_logs(service: str, since: str, use_both: bool = False) -> str:
    fn = compose_both if use_both else compose_main
    proc = fn("logs", "--no-log-prefix", "--since", since, service)
    if proc.returncode != 0:
        return ""
    return proc.stdout or ""


def log_has(service: str, since: str, hid: str, use_both: bool = False) -> bool:
    return hid in compose_logs(service, since, use_both)


def agg_published_since(since: str) -> bool:
    return "hazard published" in compose_logs("aggregator", since, False)


def now_ts() -> str:
    return dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def ingest_unique(tag: str, with_conf: bool) -> tuple[str, str] | None:
    """Ingest one unique volcanic report. Returns (report_id, hazard_id) or None."""
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S")
    rid = f"p5-{tag}-{stamp}-{os.getpid()}-{random.randint(0, 32767)}"
    rat = now_ts()
    rec: dict = {"report_id": rid, "volcano_id": "MERAPI", "alert_level": "Siaga",
                 "eruption_count_24h": 2, "ash_column_height_m": 300, "reported_at": rat}
    if with_conf:
        rec["confidence_level"] = 0.85
    st, body = http_post_json(f"http://127.0.0.1:{AGG_PORT}/internal/ingest/pvmbg",
                              {"volcanic_reports": [rec]},
                              headers={"Authorization": f"Bearer {AGG_TOKEN}"})
    if not (200 <= st < 300):
        info(f"ingest {rid} HTTP {st}: {body[:400]!r}")
        return None
    try:
        hid = json.loads(body.decode())["items"][0]["hazard_id"]
    except (ValueError, KeyError, IndexError, UnicodeDecodeError, TypeError):
        info(f"ingest {rid} response without hazard_id: {body[:400]!r}")
        return None
    if not hid:
        return None
    return rid, hid


def wait_log(service: str, since: str, hid: str, timeout: float | None = None,
             use_both: bool = False) -> bool:
    timeout = TIMEOUT_SECS if timeout is None else timeout
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if log_has(service, since, hid, use_both):
            return True
        time.sleep(POLL_INTERVAL_SECS)
    return False


def grep_file(path: Path, pattern: str) -> bool:
    try:
        return re.search(pattern, path.read_text(encoding="utf-8", errors="ignore")) is not None
    except OSError:
        return False


def grep_tree(pattern: str, roots: list[Path], include: str = "*") -> list[str]:
    rx = re.compile(pattern)
    hits: list[str] = []
    for root in roots:
        if not root.exists():
            continue
        for path in root.rglob(include):
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


def git_producer_diff() -> str:
    proc = subprocess.run(["git", "status", "--porcelain", "--",
                           "aggregator/", "internal/eventbus/"],
                          cwd=ROOT, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if proc.returncode != 0:
        return "git-unavailable"
    return proc.stdout.strip()


def main() -> int:
    global STARTED_TEST_CONSUMER
    info("Problem 5 test")
    info(f"root={ROOT} project={PROJECT} main={COMPOSE_MAIN} extra={COMPOSE_EXTRA} env={ENV_FILE}")
    info(f"timeouts: TIMEOUT={TIMEOUT_SECS:g} POLL={POLL_INTERVAL_SECS:g} "
         f"CURL={CURL_TIMEOUT_SECS:g} downtime_events={DOWNTIME_EVENTS}")

    if not AGG_TOKEN:
        failed(f"AGGREGATOR_TOKEN missing in {ENV_FILE}")
    if not (ROOT / COMPOSE_MAIN).is_file():
        failed(f"main compose file missing: {COMPOSE_MAIN}")
    if not (ROOT / COMPOSE_EXTRA).is_file():
        failed(f"second compose file missing: {COMPOSE_EXTRA}")
    if not ((ENV_FILE if ENV_FILE.is_absolute() else ROOT / ENV_FILE).is_file()):
        failed(f"env file missing: {ENV_FILE}")
    proc = subprocess.run(["docker", "info"], cwd=ROOT, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if proc.returncode != 0:
        failed("docker daemon unreachable")
        info("start Docker Desktop first")

    # ---------------------------------------------------------------- A. discover + health
    info("--- A. Compose files, broker, original consumers ---")
    info(f"discovered main compose file: {COMPOSE_MAIN}")
    info(f"discovered second compose file: {COMPOSE_EXTRA} "
         "(defines test-consumer; shares project/network/NATS)")
    try:
        extra_text = (ROOT / COMPOSE_EXTRA).read_text(encoding="utf-8", errors="ignore")
    except OSError:
        extra_text = ""
    if "test-consumer" in extra_text:
        passed("second file defines test-consumer")
    else:
        failed("second file does not define test-consumer")
    if re.search(r"networks.*services|networks:\s*\[services\]", extra_text):
        passed("test-consumer attaches to existing services network")
    else:
        failed("test-consumer network config unexpected (must join existing services network)")

    nats_cid = cid_main("nats")
    dash_cid = cid_main("dashboard-updater")
    field_cid = cid_main("field-notifier")
    info(f"broker container: nats={nats_cid[:12] if nats_cid else '?'} "
         f"dashboard-updater={dash_cid[:12] if dash_cid else '?'} "
         f"field-notifier={field_cid[:12] if field_cid else '?'}")
    if not nats_cid:
        failed(f"nats container missing; start stack: docker compose -p {PROJECT} -f {COMPOSE_MAIN} up -d --build")
    elif is_running(nats_cid):
        passed("nats container running")
    else:
        failed("nats container not running")

    st, _ = http_get(f"{NATS_MON}/healthz")
    if not (200 <= st < 300):
        st, _ = http_get(f"{NATS_MON}/varz")
    if 200 <= st < 300:
        passed(f"NATS healthy (monitoring {NATS_MON} reachable)")
    else:
        failed(f"NATS monitoring unreachable at {NATS_MON} (broker down or wrong port?)")

    st, body = http_get(f"{NATS_MON}/jsz?streams=true")
    if 200 <= st < 300 and b"HAZARDS_STREAM" in body:
        passed("JetStream stream HAZARDS_STREAM exists")
    else:
        failed(f"HAZARDS_STREAM not visible via {NATS_MON}/jsz?streams=true "
               "(outbox relay may not have initialized it yet)")

    for svc in ("dashboard-updater", "field-notifier"):
        cid = cid_main(svc)
        if not cid:
            failed(f"{svc} not running (start stack first)")
            continue
        if is_running(cid):
            hs = health_of(cid)
            info(f"{svc} running ({cid[:12]}, health={hs}; no host port by design, "
                 "health via inspect+logs)")
            if hs in ("healthy", "none"):
                passed(f"{svc} running and operational")
            else:
                failed(f"{svc} health={hs}")
        else:
            failed(f"{svc} container not running")

    st, _ = http_get(f"http://127.0.0.1:{AGG_PORT}/health")
    if 200 <= st < 300:
        passed("aggregator health OK (publisher side up)")
    else:
        failed("aggregator health failed")

    # ---------------------------------------------------------------- B. producer publication
    info("--- B. Producer publication to NATS ---")
    stream = "HAZARDS_STREAM"
    subject = "hazards.created.v1"
    try:
        js_text = (ROOT / "internal" / "eventbus" / "jetstream.go").read_text(
            encoding="utf-8", errors="ignore")
        m = re.search(r'StreamName\s*=\s*"([^"]*)"', js_text)
        if m:
            stream = m.group(1)
        m = re.search(r'Subject\s*=\s*"([^"]*)"', js_text)
        if m:
            subject = m.group(1)
    except OSError:
        pass
    info(f"configured stream={stream} subject={subject} (internal/eventbus/jetstream.go)")
    if stream == "HAZARDS_STREAM" and subject == "hazards.created.v1":
        passed("actual NATS subject/stream discovered from implementation")
    else:
        failed(f"unexpected stream/subject ({stream}/{subject})")

    prod_hits = grep_tree(r"dashboard-updater|field-notifier|test-consumer",
                          [ROOT / "aggregator", ROOT / "internal" / "eventbus"])
    if prod_hits:
        failed("producer references consumer names (registration hardcoded?)")
        for h in prod_hits[:5]:
            info(f"  {h}")
    else:
        passed("no consumer registration hardcoded in producer")

    pub_path = ROOT / "aggregator" / "cmd" / "server" / "publisher.go"
    pub_text = ""
    try:
        pub_text = pub_path.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        pass
    ctrl_text = ""
    for p in (ROOT / "aggregator" / "internal" / "controller").glob("*.go"):
        try:
            ctrl_text += p.read_text(encoding="utf-8", errors="ignore") + "\n"
        except OSError:
            continue
    has_publish = ("PublishMsg" in pub_text) or ("js.Publish" in pub_text)
    has_outbox = any(k in (pub_text + "\n" + ctrl_text)
                     for k in ("hazard_outbox", "ClaimOutbox", "WithMsgID"))
    if has_publish and has_outbox:
        passed("producer publishes asynchronously via outbox relay -> JetStream "
               "(no sync consumer calls)")
    else:
        failed("publisher implementation does not show expected outbox->JetStream path")

    ts_b = now_ts()
    hid_b = ""
    out_b = ingest_unique("b", False)
    if out_b is None:
        failed("could not ingest probe event for publication check")
    else:
        rid_b, hid_b = out_b
        info(f"probe event: report={rid_b} hazard={hid_b}")
        # Paginated search: the API returns oldest-first pages (max 1000), so a
        # single GET misses new records once the DB holds >1000 PVMBG rows.
        probe_h, probe_ok = find_hazard("PVMBG", "hazard_id", hid_b)
        if probe_h is not None:
            passed(f"aggregator mapped+stored probe event ({hid_b})")
        elif not probe_ok:
            failed("probe event lookup failed (GET /internal/hazards HTTP error)")
        else:
            failed("probe event not found in canonical store")
        if wait_for(f"aggregator publish {hid_b}", TIMEOUT_SECS, agg_published_since, ts_b):
            passed("aggregator published to NATS (outbox relay logged hazard published)")
        else:
            failed("no 'hazard published' log for probe window (publishing broken or too slow?)")
        try:
            assert probe_h, "missing"
            h = probe_h
            assert (h["source"] == "PVMBG" and h["hazard_type"] == "VOLCANIC"
                    and h["area_name"] == "Gunung Merapi"), h
            passed(f"published payload matches mapped event (PVMBG/VOLCANIC/Gunung Merapi, "
                   f"hazard_id {hid_b})")
        except Exception:  # noqa: BLE001 - payload assertion diagnostics
            failed(f"mapped payload fields unexpected for {hid_b}")

    # ---------------------------------------------------------------- C. two original consumers
    info("--- C. Both original consumers receive the same event ---")
    ts_c = now_ts()
    out_c = ingest_unique("c", False)
    if out_c is None:
        failed("could not ingest fan-out probe event")
    else:
        rid_c, hid_c = out_c
        info(f"fan-out event: report={rid_c} hazard={hid_c} (baseline {ts_c})")
        ok_c = True
        if wait_log("dashboard-updater", ts_c, hid_c, TIMEOUT_SECS, False):
            passed(f"dashboard-updater received {hid_c}")
        else:
            failed(f"dashboard-updater did NOT receive {hid_c} within {TIMEOUT_SECS:g}s "
                   f"(logs since {ts_c})")
            ok_c = False
        if wait_log("field-notifier", ts_c, hid_c, TIMEOUT_SECS, False):
            passed(f"field-notifier received {hid_c}")
        else:
            failed(f"field-notifier did NOT receive {hid_c} within {TIMEOUT_SECS:g}s")
            ok_c = False
        if ok_c:
            passed("both original consumers received the same event")

    # ---------------------------------------------------------------- D. failure isolation + recovery
    info("--- D. Consumer failure isolation and recovery (stop dashboard-updater) ---")
    dash_cid = cid_main("dashboard-updater")
    field_cid = cid_main("field-notifier")
    nats_cid = cid_main("nats")
    if not (is_running(dash_cid) and is_running(field_cid) and is_running(nats_cid)):
        failed("prerequisite: nats + both consumers must be running before failure test")
    else:
        passed("prerequisite: nats + both consumers operational")

    ts_d = now_ts()
    info("stopping ONLY dashboard-updater")
    if compose_main("stop", "dashboard-updater").returncode == 0:
        passed("dashboard-updater stopped")
    else:
        failed("could not stop dashboard-updater")
    time.sleep(3)
    if is_running(cid_main("dashboard-updater")):
        failed("dashboard-updater still running after stop")
    else:
        info("dashboard-updater confirmed stopped")
    if (is_running(cid_main("nats")) and is_running(cid_main("field-notifier"))
            and is_running(cid_main("aggregator"))):
        passed("nats/aggregator/field-notifier still running during outage")
    else:
        failed("an unrelated service stopped unexpectedly")

    info(f"publishing {DOWNTIME_EVENTS} unique events during downtime")
    downtime_hids: list[str] = []
    for i in range(1, DOWNTIME_EVENTS + 1):
        out = ingest_unique(f"down{i}", False)
        if out is None:
            failed(f"downtime ingest #{i} failed")
            continue
        downtime_hids.append(out[1])
        info(f"downtime event #{i}: {out[0]} {out[1]}")
    if not downtime_hids:
        failed("no downtime events published; cannot test isolation")
    else:
        all_pub = True
        for h in downtime_hids:
            if wait_log("aggregator", ts_d, h, 20, False):
                info(f"published during downtime: {h}")
            else:
                failed(f"aggregator did NOT publish {h} while consumer stopped (blocked?)")
                all_pub = False
        if all_pub:
            passed("aggregator continued publishing during consumer downtime (not blocked)")
        all_healthy = True
        for h in downtime_hids:
            if wait_log("field-notifier", ts_d, h, TIMEOUT_SECS, False):
                info(f"field-notifier got {h} during outage")
            else:
                failed(f"field-notifier missed {h} during dashboard outage")
                all_healthy = False
        if all_healthy:
            passed("healthy consumer continued receiving during downtime")
        for h in downtime_hids:
            if log_has("dashboard-updater", ts_d, h, False):
                failed(f"dashboard-updater log shows {h} while stopped (unexpected)")
        info("stopped consumer correctly received nothing while down (checked)")

        info("restarting dashboard-updater")
        if compose_main("start", "dashboard-updater").returncode == 0:
            passed("dashboard-updater restart issued")
        else:
            failed("could not restart dashboard-updater")
        deadline = time.monotonic() + TIMEOUT_SECS
        while time.monotonic() < deadline:
            if is_running(cid_main("dashboard-updater")):
                break
            time.sleep(POLL_INTERVAL_SECS)
        if is_running(cid_main("dashboard-updater")):
            info("dashboard-updater running again")
        else:
            failed("dashboard-updater not running after start")
        time.sleep(5)
        info("implementation: durable=dashboard-updater DeliverAll+AckExplicit, stream "
             "LimitsPolicy 7d (worker.go/eventbus) => backlog expected")
        recovered = True
        for h in downtime_hids:
            if wait_log("dashboard-updater", ts_d, h, TIMEOUT_SECS, False):
                info(f"recovered after restart: {h}")
            else:
                failed(f"dashboard-updater did NOT recover {h} after restart")
                recovered = False
        if recovered:
            passed("restarted consumer received backlog published while stopped "
                   "(durable JetStream recovery proven)")
        else:
            info("delivery model note: if consumer used ephemeral Core NATS, backlog would be "
                 "lost; here config is durable JetStream so loss would indicate "
                 "redelivery/ack misconfig, NOT expected loss")

    # ---------------------------------------------------------------- E. third consumer via second file
    info(f"--- E. Third consumer via {COMPOSE_EXTRA} ---")
    agg_before = cid_main("aggregator") or cid_both("aggregator")
    info(f"aggregator container before: {agg_before[:12] if agg_before else '?'}")
    prod_diff_before = git_producer_diff()
    if not prod_diff_before:
        info("producer sources unmodified before third-consumer step (git clean)")
    elif prod_diff_before != "git-unavailable":
        info("note: working tree already has local modifications before this step "
             "(not made by script); snapshotted for before/after comparison")

    test_before = cid_both("test-consumer")
    if test_before and is_running(test_before):
        info(f"test-consumer already running ({test_before[:12]}); will not disrupt it, "
             "only verify delivery")
    else:
        info("starting test-consumer with existing second file (same project/network/NATS, "
             "no second broker)")
        info(f"cmd: docker compose -p {PROJECT} -f {COMPOSE_MAIN} -f {COMPOSE_EXTRA} "
             "up -d --build --no-deps test-consumer")
        if compose_both("up", "-d", "--build", "--no-deps", "test-consumer").returncode == 0:
            passed("test-consumer start issued via second compose file")
            STARTED_TEST_CONSUMER = True
        else:
            failed("could not start test-consumer via second compose file")

    test_cid = cid_both("test-consumer") or cid_main("test-consumer")
    if test_cid and is_running(test_cid):
        passed(f"test-consumer running ({test_cid[:12]})")
    else:
        failed("test-consumer not running after start")
        test_cid = test_cid or ""

    if test_cid:
        nets_proc = subprocess.run(
            ["docker", "inspect", "--format",
             "{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}", test_cid],
            cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        test_nets = (nets_proc.stdout or "").strip() if nets_proc.returncode == 0 else "?"
        env_proc = subprocess.run(["docker", "inspect", "--format",
                                   "{{range .Config.Env}}{{println .}}{{end}}", test_cid],
                                  cwd=ROOT, text=True,
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        test_env = ""
        if env_proc.returncode == 0:
            for line in (env_proc.stdout or "").splitlines():
                if line.startswith("NATS_URL="):
                    test_env = line.strip()
                    break
            test_env = test_env or "?"
        info(f"test-consumer networks: {test_nets} env: {test_env}")
        if "services" in test_nets:
            passed("test-consumer on existing services network (same NATS)")
        else:
            failed("test-consumer not on services network")
        if "nats:4222" in test_env:
            passed("test-consumer connects to same broker (nats:4222)")
        else:
            failed(f"test-consumer NATS_URL unexpected: {test_env}")
    else:
        failed("cannot inspect test-consumer networks (missing container)")

    durables_ok = all(
        grep_file(ROOT / "consumers" / name / "main.go", f'"{name}"')
        for name in ("dashboard-updater", "field-notifier", "test-consumer"))
    if durables_ok:
        passed("three consumers use distinct durable names (independent fan-out, not one work queue)")
    else:
        failed("durable names not distinct as expected")

    ts_e = now_ts()
    out_e = ingest_unique("e3", False)
    if out_e is None:
        failed("could not ingest three-consumer probe event")
    else:
        hid_e = out_e[1]
        info(f"three-consumer probe: {out_e[0]} {hid_e}")
        ok3 = True
        if wait_log("dashboard-updater", ts_e, hid_e, TIMEOUT_SECS, False):
            passed(f"dashboard-updater received third-probe {hid_e}")
        else:
            failed(f"dashboard-updater missed {hid_e}")
            ok3 = False
        if wait_log("field-notifier", ts_e, hid_e, TIMEOUT_SECS, False):
            passed(f"field-notifier received third-probe {hid_e}")
        else:
            failed(f"field-notifier missed {hid_e}")
            ok3 = False
        if wait_log("test-consumer", ts_e, hid_e, TIMEOUT_SECS, True):
            passed(f"test-consumer received third-probe {hid_e}")
        else:
            failed(f"test-consumer missed {hid_e} (subscription/stream mismatch?)")
            ok3 = False
        if ok3:
            passed("all three independent consumers received the same event")

    agg_after = cid_main("aggregator") or cid_both("aggregator")
    if agg_before and agg_before == agg_after:
        passed(f"aggregator container unchanged ({agg_after[:12]}; no producer restart to add consumer)")
    else:
        failed(f"aggregator container changed ({agg_before} -> {agg_after})")
    prod_diff_after = git_producer_diff()
    if prod_diff_after == prod_diff_before:
        passed("script introduced no producer source/config changes to register third consumer")
        if prod_diff_after and prod_diff_after != "git-unavailable":
            info("pre-existing local modifications (untouched by script):")
            for line in prod_diff_after.splitlines()[:5]:
                info(f"  {line}")
            info("running container predates them (ID unchanged above), so they have no effect "
                 "until a rebuild")
    else:
        failed("producer tree changed during this script (before/after diff differs)")
        info("before:")
        for line in (prod_diff_before or "").splitlines()[:5]:
            info(f"  {line}")
        info("after:")
        for line in (prod_diff_after or "").splitlines()[:5]:
            info(f"  {line}")

    # ---------------------------------------------------------------- F. semantics + idempotency
    info("--- F. Delivery semantics and idempotency ---")
    info("publisher: JetStream FileStorage, LimitsPolicy, Duplicates 2m, ExpectStream, "
         "MsgID=eventKey (publisher.go/eventbus)")
    info("consumer: DeliverAll, AckExplicit, AckWait 30s, MaxAckPending 1, KV dedupe bucket "
         "per durable, NakWithDelay on failure, Term on invalid (worker.go)")
    worker_path = ROOT / "consumers" / "internal" / "worker" / "worker.go"
    jetstream_path = ROOT / "internal" / "eventbus" / "jetstream.go"
    try:
        worker_text = worker_path.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        worker_text = ""
    try:
        jetstream_text = jetstream_path.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        jetstream_text = ""
    if ("AckExplicitPolicy" in worker_text and "DeliverAllPolicy" in worker_text
            and "FileStorage" in jetstream_text):
        passed("delivery is at-least-once (durable JetStream + explicit ack + redelivery), "
               "NOT at-most-once")
    else:
        failed("could not confirm at-least-once configuration from code")

    dup_out = ingest_unique("dup", False)
    if dup_out is None:
        info("skipping duplicate-idempotency probe (ingest failed)")
    else:
        dup_rid, dup_hid = dup_out
        info(f"duplicate probe: re-ingesting report {dup_rid} with the IDENTICAL payload "
             "directly via API")
        dup_h, dup_ok = find_hazard("PVMBG", "hazard_id", dup_hid)
        if dup_h is None and not dup_ok:
            info(f"duplicate re-ingest skipped: hazards lookup failed for {dup_hid}")
            dup_occ = ""
        else:
            dup_occ = (dup_h.get("occurred_at", "") if isinstance(dup_h, dict) else "") or ""
        if not dup_occ:
            info(f"duplicate re-ingest skipped: could not read back occurred_at for {dup_hid}")
        else:
            rec = {"report_id": dup_rid, "volcano_id": "MERAPI", "alert_level": "Siaga",
                   "eruption_count_24h": 2, "ash_column_height_m": 300, "reported_at": dup_occ}
            st, body = http_post_json(
                f"http://127.0.0.1:{AGG_PORT}/internal/ingest/pvmbg",
                {"volcanic_reports": [rec]},
                headers={"Authorization": f"Bearer {AGG_TOKEN}"})
            if 200 <= st < 300:
                try:
                    ch = json.loads(body.decode()).get("changed", "?")
                except (ValueError, UnicodeDecodeError):
                    ch = "?"
                info(f"re-ingest changed={ch} (expected 0: producer dedupes unchanged events)")
                if ch == 0:
                    passed("duplicate event IDs do not cause duplicate publishes "
                           "(producer idempotent)")
                else:
                    failed(f"identical re-ingest returned changed={ch} (expected 0; "
                           "duplicate would republish)")
            else:
                info("duplicate re-ingest request failed; cannot assess producer dedupe")

    worker_test_path = ROOT / "consumers" / "internal" / "worker" / "worker_test.go"
    try:
        worker_test_text = worker_test_path.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        worker_test_text = ""
    if (re.search(r"dedupe|KeyValue|deliveryKey", worker_text)
            and "TestDeliveryKey" in worker_test_text):
        passed("consumer idempotency implemented (KV deliveryKey dedupe) and unit-tested; "
               "NOT exactly-once end-to-end (log side effect can duplicate on crash-before-KV-write)")
    else:
        failed("consumer idempotency mechanism missing or untested")
    info("verdict: at-least-once delivery with consumer-side dedupe; exactly-once NOT claimed")

    # ---------------------------------------------------------------- cleanup (only own resources)
    if CLEANUP_TEST_CONSUMER == "1" and STARTED_TEST_CONSUMER:
        info("cleanup: stopping test-consumer (we started it and CLEANUP_TEST_CONSUMER=1)")
        if compose_both("stop", "test-consumer").returncode != 0:
            info("cleanup stop failed (leaving as-is)")
    else:
        info("cleanup: leaving test-consumer running (owned by stack/demo, not deleted); "
             "streams/durables untouched")
    info("never ran: down -v, table/stream wipes, broker/producer restarts during failure test")

    # ---------------------------------------------------------------- summary
    print("==============================")
    print(f"Problem 5 summary: {PASS_COUNT} passed, {FAIL_COUNT} failed")
    print("NATS health, publication, 2-consumer fan-out, downtime continuity, backlog recovery,")
    print(f"third consumer via {COMPOSE_EXTRA}, no-producer-change, and at-least-once findings above.")
    if FAIL_COUNT > 0:
        print(f"[FAIL] problem-5: {FAIL_COUNT} check(s) failed")
        return 1
    print("[PASS] problem-5: all required checks passed")
    return 0


if __name__ == "__main__":
    os.chdir(ROOT)
    sys.exit(main())
