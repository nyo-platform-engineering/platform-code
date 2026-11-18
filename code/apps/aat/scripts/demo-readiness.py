#!/usr/bin/env python3
"""Read-only readiness check for the live M1 demonstration.

The script never prints secret values and does not create, restart, or mutate
containers. Run it from any directory before opening the three demo terminals.
"""

from pathlib import Path
import json
import shutil
import subprocess
import sys
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
ENV_FILE = ROOT / ".env"
SERVICES = (
    "postgres", "aggregator", "bmkg", "pvmbg", "nats", "dashboard-updater",
    "field-notifier", "auth", "client",
)
REQUIRED_SECRETS = (
    "BMKG_API_KEY", "PVMBG_TOKEN", "AGGREGATOR_TOKEN", "POSTGRES_PASSWORD",
    "AUTH_INTERNAL_TOKEN", "PUBLIC_CLIENT_SECRET", "RESPONDER_CLIENT_SECRET",
    "ANALYST_CLIENT_SECRET",
)
EXPECTED_CONFIG = {
    "PVMBG_DELAY_MIN_MS": "3000",
    "PVMBG_DELAY_MAX_MS": "3000",
    "ACCESS_TTL_SECONDS": "60",
    "CLIENT_MAX_CONCURRENT": "16",
    "AUTH_MAX_CONCURRENT": "32",
}


def load_env(path):
    values = {}
    for line in path.read_text().splitlines():
        if line.strip() and not line.lstrip().startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            values[key.strip()] = value.strip().strip("\"'")
    return values


def check(label, ok, detail=""):
    marker = "PASS" if ok else "FAIL"
    suffix = f" - {detail}" if detail else ""
    print(f"[{marker}] {label}{suffix}")
    return ok


def command_output(argv):
    return subprocess.check_output(
        argv, cwd=ROOT, text=True, stderr=subprocess.STDOUT, timeout=20
    )


def main():
    passed = True
    print(f"AAT M1 demo readiness ({ROOT})")

    for command in ("docker", "python3", "curl", "jq", "k6"):
        passed &= check(f"tool {command}", shutil.which(command) is not None)

    if not ENV_FILE.is_file():
        check(".env exists", False, "run python3 scripts/setup-env.py")
        return 1
    passed &= check(".env exists", True)
    env = load_env(ENV_FILE)

    missing = [key for key in REQUIRED_SECRETS if not env.get(key)]
    passed &= check("all credential variables are populated", not missing,
                    "missing: " + ", ".join(missing) if missing else "values redacted")
    values = [env.get(key, "") for key in REQUIRED_SECRETS]
    passed &= check("credential domains use distinct values",
                    len(values) == len(set(values)) and all(values), "values redacted")
    for key, expected in EXPECTED_CONFIG.items():
        actual = env.get(key, expected)
        passed &= check(f"{key}={expected}", actual == expected,
                        f"configured as {actual!r}" if actual != expected else "")

    try:
        command_output(["docker", "info", "--format", "{{.ServerVersion}}"])
        docker_ready = True
    except (FileNotFoundError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        docker_ready = False
    passed &= check("Docker daemon is reachable", docker_ready,
                    "start Docker Desktop" if not docker_ready else "")
    if not docker_ready:
        print("\nReadiness stopped before runtime checks; no state was changed.")
        return 1

    compose = [
        "docker", "compose", "-p", "aat-part1", "--env-file", str(ENV_FILE),
        "-f", "compose.yaml", "-f", "compose.client.yaml",
    ]
    try:
        ids = command_output(compose + ["ps", "-a", "-q", *SERVICES]).split()
        inspected = json.loads(command_output(["docker", "inspect", *ids])) if ids else []
        state = {
            item.get("Config", {}).get("Labels", {}).get("com.docker.compose.service"):
            item.get("State", {}) for item in inspected
        }
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired, json.JSONDecodeError):
        state = {}

    for service in SERVICES:
        found = service in state
        service_state = state.get(service, {})
        ok = bool(service_state.get("Running")) and not service_state.get("OOMKilled")
        health = service_state.get("Health", {}).get("Status", "no healthcheck")
        if not found:
            detail = "container not found; start the Compose stack"
        elif not service_state.get("Running"):
            detail = "container exists but is stopped"
        elif service_state.get("OOMKilled"):
            detail = "container was OOM-killed"
        else:
            detail = health
        passed &= check(f"container {service} is running", ok, detail)

    urls = {
        "client": f"http://127.0.0.1:{env.get('CLIENT_PORT', '8080')}/health",
        "auth": f"http://127.0.0.1:{env.get('AUTH_PORT', '8084')}/health",
        "aggregator": f"http://127.0.0.1:{env.get('AGGREGATOR_PORT', '8083')}/health",
        "bmkg": f"http://127.0.0.1:{env.get('BMKG_PORT', '8081')}/health",
        "pvmbg": f"http://127.0.0.1:{env.get('PVMBG_PORT', '8082')}/health",
        "nats": "http://127.0.0.1:8222/healthz",
    }
    for service, url in urls.items():
        try:
            with urllib.request.urlopen(url, timeout=3) as response:
                ok = response.status == 200
        except (urllib.error.URLError, TimeoutError):
            ok = False
        passed &= check(f"health endpoint {service}", ok, url)

    evidence = sorted((ROOT / "docs" / "evidence").glob("problem-*/result.json"))
    passed &= check("saved Problem 2/3 evidence exists",
                    any("problem-2-" in str(path) for path in evidence)
                    and any("problem-3-" in str(path) for path in evidence),
                    f"{len(evidence)} result file(s)")

    print("\nREADY: open the API, DB, and load-test terminals." if passed else
          "\nNOT READY: fix the FAIL items before the demonstration.")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
