"""Exercise the running part-2 stack; no tokens/secrets are printed or persisted."""
from pathlib import Path
import argparse
import time
import json
import urllib.error
import urllib.request

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--env-file", type=Path, default=root / ".env")
parser.add_argument("--wait-expiry", action="store_true", help="Wait for natural responder token expiry before refreshing")
args = parser.parse_args()
env = {}
for line in args.env_file.read_text().splitlines():
    if line.strip() and not line.lstrip().startswith("#") and "=" in line:
        key, value = line.split("=", 1)
        env[key.strip()] = value.strip().strip("\"'")

urls = {name: f"http://127.0.0.1:{env.get(key, default)}" for name, key, default in (
    ("auth", "AUTH_PORT", "8084"), ("client", "CLIENT_PORT", "8080"),
    ("aggregator", "AGGREGATOR_PORT", "8083"), ("bmkg", "BMKG_PORT", "8081"),
    ("pvmbg", "PVMBG_PORT", "8082"),
)}

def call(service, path, body=None, token=None, headers=None, expected=200):
    headers = dict(headers or {})
    if token:
        headers["Authorization"] = f"Bearer {token}"
    data = None if body is None else json.dumps(body).encode()
    if data is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(urls[service] + path, data=data, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    if status != expected:
        raise RuntimeError(f"{service} {path}: HTTP {status}, expected {expected}")
    return json.loads(raw)

for service in urls:
    assert call(service, "/health")["ok"], service

pairs = {}
fields = {}
for identity, count in (("public", 7), ("responder", 11), ("analyst", 11)):
    pair = call("auth", "/auth/token", {
        "client_id": identity, "client_secret": env[f"{identity.upper()}_CLIENT_SECRET"],
    })
    pairs[identity] = pair
    page = call("client", "/hazards?source=BMKG&limit=1", token=pair["access_token"])
    if not page["items"]:
        raise SystemExit("Ingest data BMKG lewat demo bagian 1 dahulu; lalu ulangi script ini.")
    item = page["items"][0]
    assert len(item) == count, (identity, sorted(item))
    fields[identity] = sorted(item)
    if identity == "public":
        assert "attributes" not in item and "source_ref_id" not in item
    if identity == "public":
        assert "latitude" not in item and "longitude" not in item
    assert page["sources"][0]["status"] in ("fresh", "stale", "unknown")
    if page["sources"][0]["status"] == "unknown":
        assert page["sources"][0]["stale"] is True

if args.wait_expiry:
    responder = pairs["responder"]
    wait = responder["expires_in"] + 1
    print(f"Menunggu kedaluwarsa alami token Tim Lapangan ({wait} detik)...", flush=True)
    time.sleep(wait)
    call("client", "/hazards", token=responder["access_token"], expected=401)
    fresh = call("auth", "/auth/refresh", {"refresh_token": responder["refresh_token"]})
    call("client", "/hazards", token=fresh["access_token"])
    call("client", "/hazards", token=responder["access_token"], expected=401)
    # Refresh remaining sessions without another manual login.
    for identity in ("public", "analyst"):
        pairs[identity] = call("auth", "/auth/refresh", {"refresh_token": pairs[identity]["refresh_token"]})
    pairs["responder"] = fresh

public = pairs["public"]
call("client", "/hazards", expected=401)
call("client", "/hazards?fields=attributes", token=public["access_token"], expected=403)
call("auth", "/auth/token", {"client_id": "public", "client_secret": "wrong"}, expected=401)
rotated = call("auth", "/auth/refresh", {"refresh_token": public["refresh_token"]})
call("client", "/hazards", token=public["access_token"], expected=401)
call("client", "/hazards", token=rotated["access_token"])
call("auth", "/auth/refresh", {"refresh_token": public["refresh_token"]}, expected=401)
call("client", "/hazards", token=rotated["access_token"], expected=401)

# Check the existing mock contract using the actual services, including wrong credentials.
call("bmkg", "/seismic-events", headers={"X-BMKG-Key": env["BMKG_API_KEY"]})
call("pvmbg", "/volcanic-reports", token=env["PVMBG_TOKEN"])
call("bmkg", "/seismic-events", headers={"X-BMKG-Key": env["PVMBG_TOKEN"]}, expected=401)
call("pvmbg", "/volcanic-reports", token=env["BMKG_API_KEY"], expected=401)
call("bmkg", "/seismic-events", headers={"X-BMKG-Key": pairs["analyst"]["access_token"]}, expected=401)
call("pvmbg", "/volcanic-reports", token=pairs["analyst"]["access_token"], expected=401)
call("aggregator", "/internal/hazards", token=pairs["analyst"]["access_token"], expected=401)
print(json.dumps({"identities_and_fields": fields, "natural_expiry": "passed" if args.wait_expiry else "not run (use --wait-expiry)", "refresh_rotation_and_replay": "passed", "mock_auth_contract": "passed", "source_status": page["sources"]}, indent=2))
