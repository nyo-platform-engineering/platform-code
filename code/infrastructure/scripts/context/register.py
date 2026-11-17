#!/usr/bin/env python3
"""Register a named VM cluster context without replacing unrelated kubeconfig entries."""
import argparse
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile


def load_config(path):
    # kubectl parses YAML and embeds certificate files before we move the entries.
    result = subprocess.run(
        ["kubectl", "--kubeconfig", str(path), "config", "view", "--raw", "--flatten", "-o", "json"],
        capture_output=True, text=True, check=True,
    )
    return json.loads(result.stdout)


def write_private(path, content):
    # Replace atomically; a failed write must not truncate the existing kubeconfig.
    descriptor, temporary = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as file:
            file.write(content)
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def register(source, target, name):
    if source.resolve() == target.resolve():
        raise ValueError("The imported lab kubeconfig and destination must be different files.")
    if not source.is_file():
        raise ValueError("Lab kubeconfig missing. Run task up first.")

    lab = load_config(source)
    active = next(entry["context"] for entry in lab["contexts"] if entry["name"] == lab["current-context"])
    cluster = next(entry["cluster"] for entry in lab["clusters"] if entry["name"] == active["cluster"])
    user = next(entry["user"] for entry in lab["users"] if entry["name"] == active["user"])
    config = load_config(target)
    original = copy.deepcopy(config)

    # Rename all three references to avoid colliding with another cluster's "default".
    entries = {
        "clusters": {"name": name, "cluster": cluster},
        "users": {"name": name, "user": user},
        "contexts": {"name": name, "context": {
            "cluster": name, "user": name, "namespace": "sistem-informasi-kebencanaan-bnpb",
        }},
    }
    for section, entry in entries.items():
        config[section] = [item for item in (config.get(section) or []) if item["name"] != name]
        config[section].append(entry)
        config[section].sort(key=lambda item: item["name"])

    # Select the lab only when there was no active context to preserve.
    if not config.get("current-context"):
        config["current-context"] = name
    if config != original:
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if target.exists():
            backup = target.with_name(target.name + ".before-aat-local")
            write_private(backup, target.read_bytes())
        # JSON is also valid kubeconfig YAML and needs no extra Python dependency.
        write_private(target, (json.dumps(config, indent=2) + "\n").encode())
    print(f"Context {name} registered in {target} (active: {config['current-context']}).")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--target", required=True, type=Path)
    parser.add_argument("--name", default="aat-local")
    args = parser.parse_args()
    register(args.source, args.target, args.name)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, StopIteration, subprocess.CalledProcessError) as error:
        raise SystemExit(f"Could not register cluster context: {error}")
