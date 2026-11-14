#!/usr/bin/env python3
"""Generate stable local credentials, keeping them out of Helm values/history."""
import base64
import datetime
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
NAMESPACE = "sistem-informasi-kebencanaan-bnpb"
TOKEN_KEYS = (
    "AGGREGATOR_TOKEN", "BMKG_API_KEY", "PVMBG_TOKEN", "AUTH_INTERNAL_TOKEN",
    "PUBLIC_CLIENT_SECRET", "RESPONDER_CLIENT_SECRET", "ANALYST_CLIENT_SECRET",
)
REQUIRED = {"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "DATABASE_URL", *TOKEN_KEYS}


def kube(*args, data=None):
    config = os.environ.get("KUBECONFIG", str(ROOT / ".state/kubeconfig"))
    return subprocess.run(
        ["kubectl", "--kubeconfig", config, *args], input=data, text=True,
        capture_output=True, check=True,
    ).stdout


def new_credentials():
    password = secrets.token_hex(32)
    return {
        "POSTGRES_USER": "aat", "POSTGRES_DB": "aat", "POSTGRES_PASSWORD": password,
        "DATABASE_URL": f"postgres://aat:{password}@postgres:5432/aat?sslmode=disable",
        **{key: secrets.token_hex(32) for key in TOKEN_KEYS},
    }


def read_env(path):
    # Parse data only. Never source/evaluate a credentials file as shell code.
    result = {}
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator or not key.isidentifier():
            raise ValueError("Invalid runtime credentials file")
        result[key] = value
    if not REQUIRED.issubset(result) or any(not result[key] for key in REQUIRED):
        raise ValueError("Runtime file is incomplete; restore .env.runtime instead of rotating database credentials")
    return result


def write_private(path, content):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as file:
        file.write(content)


def apply(kind, name, namespace, data):
    body = {
        "apiVersion": "v1", "kind": kind,
        "metadata": {"name": name, "namespace": namespace},
        "stringData" if kind == "Secret" else "data": data,
    }
    kube("apply", "-f", "-", data=json.dumps(body))


def existing(kind, name, namespace):
    value = kube("get", kind, name, "-n", namespace, "--ignore-not-found", "-o", "json")
    return json.loads(value) if value.strip() else None


def ensure():
    runtime = ROOT / ".env.runtime"
    if not runtime.exists():
        old = existing("secret", "aat-runtime", NAMESPACE)
        values = ({k: base64.b64decode(v).decode() for k, v in old["data"].items()}
                  if old else new_credentials())
        write_private(runtime, "".join(f"{key}={value}\n" for key, value in values.items()))
    os.chmod(runtime, 0o600)
    values = read_env(runtime)
    apply("Secret", "aat-runtime", NAMESPACE, values)
    clock_file = ROOT / ".state/mock-start-time"
    if not clock_file.exists():
        clock = existing("configmap", "aat-clock", NAMESPACE)
        start = (clock["data"]["START_TIME"] if clock else
                 datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"))
        write_private(clock_file, start + "\n")
    apply("ConfigMap", "aat-clock", NAMESPACE, {"START_TIME": clock_file.read_text().strip()})
    grafana_file = ROOT / ".state/grafana-password"
    if not grafana_file.exists():
        old = existing("secret", "grafana-admin", "monitoring")
        password = base64.b64decode(old["data"]["password"]).decode() if old else secrets.token_urlsafe(32)
        write_private(grafana_file, password + "\n")
    apply("Secret", "grafana-admin", "monitoring", {"username": "admin", "password": grafana_file.read_text().strip()})
    print("Runtime Secrets and mock start time reconciled; local credentials retained.")


if __name__ == "__main__":
    try:
        ensure()
    except subprocess.CalledProcessError as error:
        print("Kubernetes credential reconciliation failed. Check connectivity and namespace access.", file=sys.stderr)
        sys.exit(error.returncode)
    except (ValueError, OSError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
