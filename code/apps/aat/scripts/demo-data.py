"""Verify part 1 over HTTP using manual ingestion; automatic polling belongs to part 3.
"""
import argparse
import datetime as dt
import json
from pathlib import Path
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--env-file", type=Path, default=root / ".env")
args = parser.parse_args()
env = {}
for line in args.env_file.read_text().splitlines():
    if line.strip() and not line.lstrip().startswith("#") and "=" in line:
        key, value = line.split("=", 1)
        env[key.strip()] = value.strip().strip("\"'")
urls = {name: f"http://127.0.0.1:{env.get(key, default)}" for name, key, default in (
    ("aggregator", "AGGREGATOR_PORT", "8083"), ("bmkg", "BMKG_PORT", "8081"),
    ("pvmbg", "PVMBG_PORT", "8082"),
)}
correlation = "data-demo-" + uuid.uuid4().hex

def call(service, path, body=None, expected=200):
    headers = {"X-Correlation-ID": correlation}
    if service == "bmkg":
        headers["X-BMKG-Key"] = env["BMKG_API_KEY"]
    else:
        headers["Authorization"] = "Bearer " + env["PVMBG_TOKEN" if service == "pvmbg" else "AGGREGATOR_TOKEN"]
    data = None if body is None else json.dumps(body).encode()
    if data is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(urls[service] + path, data=data, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            code, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        code, raw = error.code, error.read()
    if code != expected:
        raise RuntimeError(f"{service} {path}: HTTP {code}, expected {expected}")
    return json.loads(raw)

for service in urls:
    assert call(service, "/health")["ok"], service
quakes = call("bmkg", "/seismic-events")
warnings = call("bmkg", "/tsunami-warnings")
assert len(quakes) >= 20
batch = {"seismic_events": quakes, "tsunami_warnings": warnings}
result = call("aggregator", "/internal/ingest/bmkg", batch)
seismic = call("aggregator", "/internal/hazards?source=BMKG&limit=1000")["items"]
by_ref = {item["source_ref_id"]: item for item in seismic}
rank = {"Waspada": 1, "Siaga": 2, "Awas": 3}
for quake in quakes:
    related = [w for w in warnings if w["related_event_id"] == quake["event_id"]]
    expected = max(related, key=lambda w: rank[w["threat_level"]])["threat_level"].upper() if related else (
        "NORMAL" if quake["magnitude"] < 5 else "WASPADA" if quake["magnitude"] < 6.5 else "SIAGA")
    item = by_ref[quake["event_id"]]
    assert item["severity"] == expected
    assert item["area_name"] == quake["region_name"]
    assert item["latitude"] == quake["epicenter_lat"] and item["longitude"] == quake["epicenter_lon"]
    if related:
        assert item["attributes"]["tsunami_warnings"] == related
assert call("aggregator", "/internal/ingest/bmkg", batch)["changed"] == 0

try:
    call("pvmbg", "/admin/schema-version", {"enabled": False})
    reports = call("pvmbg", "/volcanic-reports")
    assert len(reports) >= 20 and all("confidence_level" not in r for r in reports)
    call("aggregator", "/internal/ingest/pvmbg", {"volcanic_reports": reports})
    since = dt.datetime.now(dt.timezone.utc).isoformat()
    call("pvmbg", "/admin/schema-version", {"enabled": True})
    # Keep historical records unchanged; ingest only events born after the switch.
    time.sleep(int(env.get("EVENT_INTERVAL_SECONDS", "10")) + 1)
    reports = call("pvmbg", "/volcanic-reports?" + urllib.parse.urlencode({"since": since}))
    assert reports and all(0 <= r["confidence_level"] <= 1 for r in reports)
    call("aggregator", "/internal/ingest/pvmbg", {"volcanic_reports": reports})
    volcanic = call("aggregator", "/internal/hazards?source=PVMBG&limit=1000")["items"]
    assert any("confidence_level" not in r["attributes"] for r in volcanic)
    assert any("confidence_level" in r["attributes"] for r in volcanic)
    call("pvmbg", "/admin/outage", {"enabled": True})
    call("pvmbg", "/volcanic-reports", expected=503)
    assert call("bmkg", "/seismic-events")
    assert call("aggregator", "/internal/hazards?source=PVMBG")["items"]
    call("pvmbg", "/admin/outage", {"enabled": False})
    assert call("pvmbg", "/volcanic-reports")
finally:
    call("pvmbg", "/admin/outage", {"enabled": False})
    call("pvmbg", "/admin/schema-version", {"enabled": False})
print(json.dumps({"correlation_id": correlation, "seismic_mapping_and_correlation": "passed",
    "idempotent_ingest": "passed", "schema_change_without_restart": "passed",
    "old_and_new_records_coexist": "passed", "mock_outage_and_recovery": "passed",
    "automatic_polling": "not checked by this manual-ingest script; use demo-system.py"}, indent=2))
