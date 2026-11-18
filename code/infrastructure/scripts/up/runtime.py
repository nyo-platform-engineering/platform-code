#!/usr/bin/env python3
"""Generate stable local credentials, keeping them out of Helm values/history."""
import base64
import datetime
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
NAMESPACE = "sistem-informasi-kebencanaan-bnpb"
# Each service/client gets its own randomly generated credential.
TOKEN_KEYS = (
    "AGGREGATOR_TOKEN",
    "BMKG_API_KEY",
    "PVMBG_TOKEN",
    "AUTH_INTERNAL_TOKEN",
    "PUBLIC_CLIENT_SECRET",
    "RESPONDER_CLIENT_SECRET",
    "ANALYST_CLIENT_SECRET",
)
REQUIRED = {
    "POSTGRES_USER",
    "POSTGRES_PASSWORD",
    "POSTGRES_DB",
    "DATABASE_URL",
    *TOKEN_KEYS,
}


def kube(*args, data=None):
    """Run kubectl with the selected kubeconfig; pass manifests through stdin."""
    kubeconfig = os.environ.get("KUBECONFIG", str(ROOT / ".state/kubeconfig"))
    return subprocess.run(
        ["kubectl", "--kubeconfig", kubeconfig, *args],
        input=data,
        text=True,
        capture_output=True,
        check=True,
    ).stdout


def new_credentials():
    """Generate credentials only when neither local nor cluster copies exist."""
    database_password = secrets.token_hex(32)
    credentials = {
        "POSTGRES_USER": "aat",
        "POSTGRES_DB": "aat",
        "POSTGRES_PASSWORD": database_password,
        "DATABASE_URL": (
            f"postgres://aat:{database_password}@postgres:5432/aat?sslmode=disable"
        ),
    }
    for key in (*TOKEN_KEYS, "POSTGRES_REPLICATION_PASSWORD", "AUDIT_TOKEN"):
        credentials[key] = secrets.token_hex(32)
    return credentials


def read_env(path):
    # Parse data only. Never source/evaluate a credentials file as shell code.
    credentials = {}
    for line in path.read_text().splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator or not key.isidentifier():
            raise ValueError("Invalid runtime credentials file")
        credentials[key] = value

    # A partial file must be restored, not silently replaced with new passwords:
    # an existing database still expects its original password.
    missing_keys = REQUIRED.difference(credentials)
    empty_keys = [key for key in REQUIRED if key in credentials and not credentials[key]]
    if missing_keys or empty_keys:
        raise ValueError(
            "Runtime file is incomplete; restore .env.runtime "
            "instead of rotating database credentials"
        )
    return credentials


def write_private(path, content):
    """Create an owner-only file and refuse to overwrite an existing one."""
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as file:
        file.write(content)


def replace_private(path, content):
    """Atomically update an owner-only state file."""
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent,
                                     prefix=path.name + ".", delete=False) as file:
        temporary = Path(file.name)
        try:
            file.write(content)
            file.close()
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)


def apply(kind, name, namespace, data):
    # Secrets accept plain strings in stringData; Kubernetes encodes them.
    # ConfigMaps use data directly. Neither manifest is written to a temp file.
    data_field = "stringData" if kind == "Secret" else "data"
    body = {
        "apiVersion": "v1",
        "kind": kind,
        "metadata": {"name": name, "namespace": namespace},
        data_field: data,
    }
    kube("apply", "-f", "-", data=json.dumps(body))


def existing(kind, name, namespace):
    """Return an existing resource, or None when Kubernetes reports it absent."""
    output = kube("get", kind, name, "-n", namespace, "--ignore-not-found", "-o", "json")
    if not output.strip():
        return None
    return json.loads(output)


def ensure_aat_credentials():
    """Reuse local AAT credentials, recover them from Kubernetes, or create them."""
    credentials_file = ROOT / ".env.runtime"

    if not credentials_file.exists():
        cluster_secret = existing("secret", "aat-runtime", NAMESPACE)
        if cluster_secret:
            # Kubernetes returns Secret data as base64, not plain text.
            credentials = {}
            for key, encoded_value in cluster_secret["data"].items():
                credentials[key] = base64.b64decode(encoded_value).decode()
        else:
            credentials = new_credentials()

        file_content = "".join(f"{key}={value}\n" for key, value in credentials.items())
        write_private(credentials_file, file_content)

    # Tighten permissions even when reusing an older local file.
    os.chmod(credentials_file, 0o600)
    credentials = read_env(credentials_file)
    # Extend older environments without rotating any existing credential.
    added = False
    for key in ("POSTGRES_REPLICATION_PASSWORD", "AUDIT_TOKEN"):
        if not credentials.get(key):
            credentials[key] = secrets.token_hex(32)
            added = True
    if added:
        temporary = credentials_file.with_suffix(".next")
        write_private(temporary, "".join(f"{key}={value}\n" for key, value in credentials.items()))
        os.replace(temporary, credentials_file)
    # Mock timing is non-secret configuration, supplied through aat-clock.
    apply("Secret", "aat-runtime", NAMESPACE, {key: value for key, value in credentials.items() if key != "START_TIME"})


def ensure_mock_start_time():
    """Use the shared env setting, recovering the existing timeline when absent."""
    env_file = ROOT / ".env.runtime"
    credentials = read_env(env_file)
    clock_file = ROOT / ".state/mock-start-time"
    start_time = credentials.get("START_TIME", "")
    if not start_time and clock_file.exists():
        start_time = clock_file.read_text().strip()
    if not start_time:
        cluster_clock = existing("configmap", "aat-clock", NAMESPACE)
        if cluster_clock:
            start_time = cluster_clock["data"]["START_TIME"]
        else:
            now = datetime.datetime.now(datetime.timezone.utc)
            start_time = now.isoformat().replace("+00:00", "Z")

    # Match the RFC3339 timestamps accepted by the Go mock services.
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})", start_time):
        raise ValueError("START_TIME must be RFC3339 with a timezone, e.g. 2026-11-18T00:00:00Z")
    try:
        datetime.datetime.fromisoformat(start_time.replace("Z", "+00:00"))
    except ValueError:
        raise ValueError("START_TIME contains an invalid date or timezone") from None

    if credentials.get("START_TIME") != start_time:
        lines = [line for line in env_file.read_text().splitlines() if not line.startswith("START_TIME=")]
        replace_private(env_file, "\n".join([*lines, "START_TIME=" + start_time, ""]))
    if not clock_file.exists() or clock_file.read_text().strip() != start_time:
        replace_private(clock_file, start_time + "\n")
    configuration = {"START_TIME": start_time}
    apply("ConfigMap", "aat-clock", NAMESPACE, configuration)


def ensure_grafana_credentials():
    """Keep the Grafana password in private local state and its monitoring Secret."""
    password_file = ROOT / ".state/grafana-password"
    if not password_file.exists():
        cluster_secret = existing("secret", "grafana-admin", "monitoring")
        if cluster_secret:
            encoded_password = cluster_secret["data"]["password"]
            password = base64.b64decode(encoded_password).decode()
        else:
            password = secrets.token_urlsafe(32)
        write_private(password_file, password + "\n")

    credentials = {
        "username": "admin",
        "password": password_file.read_text().strip(),
    }
    apply("Secret", "grafana-admin", "monitoring", credentials)


def ensure():
    # Reconcile three independent resources after 2-cluster.sh creates namespaces:
    # 1. AAT credentials for application pods and the database.
    # 2. A stable mock start time so restarting does not reset the demo timeline.
    # 3. Grafana's admin credentials, stored alongside its pods in monitoring.
    ensure_aat_credentials()
    ensure_mock_start_time()
    ensure_grafana_credentials()
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
