#!/usr/bin/env python3
"""Probe Citus ingress from real application pods; never change policy or data."""
import argparse
from concurrent.futures import ThreadPoolExecutor, as_completed
import datetime as dt
import ipaddress
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
NAMESPACE = "sistem-informasi-kebencanaan-bnpb"
PROBE = '''set -eu
if command -v python3 >/dev/null; then
  exec python3 - "$@" <<'PY'
import socket
import sys

def reachable(address):
    host, port = address.rsplit(":", 1)
    try:
        with socket.create_connection((host, int(port)), timeout=2):
            return True
    except OSError:
        return False

if not reachable(sys.argv[1]):
    raise SystemExit(21)
print("control,reachable")
for address in sys.argv[2:]:
    print(address + (",reachable" if reachable(address) else ",blocked"))
PY
fi
control="$1"
shift
command -v nc >/dev/null || exit 20
nc -z -w 2 "${control%:*}" "${control##*:}" >/dev/null 2>&1 || exit 21
printf 'control,reachable\n'
for target in "$@"; do
  if nc -z -w 2 "${target%:*}" "${target##*:}" >/dev/null 2>&1; then
    printf '%s,reachable\n' "$target"
  else
    printf '%s,blocked\n' "$target"
  fi
done
'''


def ready(pod):
    return pod["status"].get("phase") == "Running" and any(
        c["type"] == "Ready" and c["status"] == "True"
        for c in pod["status"].get("conditions", [])
    ) and not pod["metadata"].get("deletionTimestamp")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", default=os.environ.get("KUBECONFIG", str(ROOT / ".state/kubeconfig")))
    parser.add_argument("--output", type=Path, default=ROOT / ".state/storage-networkpolicy/results.json")
    args = parser.parse_args()
    base = ["kubectl", "--kubeconfig", args.kubeconfig, "-n", NAMESPACE]

    def get(kind):
        return json.loads(subprocess.check_output([*base, "get", kind, "-o", "json"], text=True, timeout=30))["items"]

    pods = get("pods")
    policies = get("networkpolicies")
    policy = next(p for p in policies if p["metadata"]["name"] == "postgres-owner-only")
    services = get("services")
    postgres = next(s for s in services if s["metadata"]["name"] == "postgres")
    storage = [p for p in pods if p["metadata"].get("labels", {}).get("app.kubernetes.io/name") == "aat-citus"]
    owners = [p for p in pods if p["metadata"].get("labels", {}).get("app.kubernetes.io/instance") == "aggregator" and ready(p)]
    if len(storage) < 7 or not all(ready(p) for p in storage) or not owners:
        raise RuntimeError("Expected at least seven Ready Citus members and a Ready Aggregator before testing")
    sources = [p for p in pods if ready(p)]
    if not any(p["metadata"].get("labels", {}).get("role") in ("api", "mock", "consumer") for p in sources):
        raise RuntimeError("No non-owner application pods available for negative tests")
    control = str(ipaddress.ip_address(owners[0]["status"]["podIP"])) + ":8083"
    targets = []
    for pod in storage:
        host = str(ipaddress.ip_address(pod["status"]["podIP"]))
        for port in (5432, 8008):
            targets.append({"name": pod["metadata"]["name"], "node": pod["spec"]["nodeName"], "address": f"{host}:{port}"})
    targets.append({"name": "service/postgres", "address": str(ipaddress.ip_address(postgres["spec"]["clusterIP"])) + ":5432"})

    def probe(pod):
        labels = pod["metadata"].get("labels", {})
        expected = labels.get("app.kubernetes.io/instance") == "aggregator" or labels.get("app.kubernetes.io/name") == "aat-citus"
        # Same-pod connections bypass ingress policy and add no isolation evidence.
        checked = [target for target in targets if target["address"].rsplit(":", 1)[0] != pod["status"]["podIP"]]
        result = {"pod": pod["metadata"]["name"], "uid": pod["metadata"]["uid"],
                  "node": pod["spec"]["nodeName"], "labels": labels,
                  "expected": "reachable" if expected else "blocked", "checks": []}
        try:
            command = [*base, "exec", "-i", result["pod"], "--", "sh", "-s", "--", control,
                       *[target["address"] for target in checked]]
            run = subprocess.run(command, input=PROBE, text=True, capture_output=True, timeout=2 * len(checked) + 20)
            if run.returncode:
                raise RuntimeError(f"probe failed (exit {run.returncode}); nc unavailable, positive control failed, or exec failed: {run.stderr.strip()}")
            lines = run.stdout.splitlines()
            if not lines or lines[0] != "control,reachable" or len(lines) != len(checked) + 1:
                raise RuntimeError("incomplete probe output; cannot establish isolation")
            result["positive_control"] = "reachable"
            if labels.get("app.kubernetes.io/instance") == "aggregator":
                # This endpoint pings PostgreSQL using the owner's configured DB
                # connection. Credentials never leave the application process.
                health = subprocess.run([*base, "exec", result["pod"], "--", "wget", "-q", "-T", "5", "-O", "-", "http://127.0.0.1:8083/health"],
                                        capture_output=True, text=True, timeout=15)
                if health.returncode or json.loads(health.stdout).get("ok") is not True:
                    raise RuntimeError("owner's authenticated database health check failed")
                result["owner_database_health"] = "ok"
            for target, line in zip(checked, lines[1:]):
                address, state = line.rsplit(",", 1)
                if address != target["address"] or state not in ("reachable", "blocked"):
                    raise RuntimeError("invalid probe output")
                result["checks"].append(dict(target, actual=state, passed=state == result["expected"]))
            result["passed"] = all(c["passed"] for c in result["checks"])
        except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as error:
            result.update(passed=False, error=str(error))
        return result

    print(f"Testing PostgreSQL :5432 and Patroni :8008 on all {len(storage)} Citus members, plus service/postgres.", flush=True)
    results = []
    with ThreadPoolExecutor(max_workers=8) as pool:
        futures = [pool.submit(probe, pod) for pod in sources]
        for future in as_completed(futures):
            result = future.result()
            results.append(result)
            passed = sum(c["passed"] for c in result["checks"])
            print(f"{'PASS' if result['passed'] else 'FAIL'} {result['pod']}: expected {result['expected']}, {passed}/{len(result['checks'])} checks" + ("; " + result["error"] if "error" in result else ""), flush=True)
    # Avoid a false pass if targets disappear after the owner checks, making
    # subsequent negative probes fail merely because the database was replaced.
    final_pods = get("pods")
    initial_ids = {p["metadata"]["uid"]: p["status"]["podIP"] for p in sources}
    final_ids = {p["metadata"]["uid"]: p["status"]["podIP"] for p in final_pods if ready(p)}
    final_policy = next(p for p in get("networkpolicies") if p["metadata"]["name"] == "postgres-owner-only")
    final_postgres = next(s for s in get("services") if s["metadata"]["name"] == "postgres")
    stable = (initial_ids == final_ids and policy["metadata"]["uid"] == final_policy["metadata"]["uid"]
              and policy["spec"] == final_policy["spec"]
              and postgres["spec"]["clusterIP"] == final_postgres["spec"]["clusterIP"])
    passed = stable and all(result["passed"] for result in results)
    if not stable:
        print("FAIL: pod inventory, policy, or Service changed during probing; repeat after rollout settles.")
    report = {"checked_at": dt.datetime.now(dt.timezone.utc).isoformat(), "namespace": NAMESPACE,
              "passed": passed, "stable_snapshot": stable, "policy": policy, "positive_control": control,
              "note": "TCP connectivity only; no database credentials or mutations. Tester Agent namespace exception is configured but not exercised by this application-pod test.",
              "storage_members": [{"pod": p["metadata"]["name"], "uid": p["metadata"]["uid"], "node": p["spec"]["nodeName"], "ip": p["status"]["podIP"]} for p in storage],
              "results": sorted(results, key=lambda r: r["pod"])}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(f"{'PASS' if passed else 'FAIL'}: {len(results)} source pods; evidence: {args.output}")
    return 0 if passed else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError, StopIteration, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit(f"Storage isolation check could not complete: {error}")
