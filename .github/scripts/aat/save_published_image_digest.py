#!/usr/bin/env python3
"""Record a build digest as JSON data; never include registry credentials."""
import json
import os
from pathlib import Path
import re

from service_build_config import SERVICE_SOURCE_FOLDERS


def main():
    service = os.environ["SERVICE"]
    digest = os.environ["IMAGE_DIGEST"]
    source_commit = os.environ["GITHUB_SHA"]
    owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
    repository = f"ghcr.io/{owner}/aat-apps/{service}"

    # Only known services, immutable digests, and this run's image namespace.
    if service not in SERVICE_SOURCE_FOLDERS:
        raise ValueError("Unexpected service name")
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
        raise ValueError("Invalid image digest")
    if not re.fullmatch(r"[a-f0-9]{40}", source_commit):
        raise ValueError("Invalid source commit")
    if os.environ["IMAGE_REPOSITORY"] != repository:
        raise ValueError("Unexpected image repository")

    # This small artifact contains no token, kubeconfig, or application Secret.
    folder = Path("image-metadata")
    folder.mkdir(exist_ok=True)
    record = {
        "service": service,
        "repository": repository,
        "digest": digest,
        "source_commit": source_commit,
    }
    (folder / f"{service}.json").write_text(json.dumps(record, indent=2) + "\n")


if __name__ == "__main__":
    main()
