#!/usr/bin/env python3
"""Validate published images and update only their allowed GitOps pin files."""
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.parse
import urllib.request

from service_build_config import (
    REPOSITORY_ROOT,
    SERVICE_SOURCE_FOLDERS,
    services_affected_by_changes,
)

COMMIT_PATTERN = r"[a-f0-9]{40}"
DIGEST_PATTERN = r"sha256:[a-f0-9]{64}"
IMAGE_FOLDER = Path("code/infrastructure/argo-apps/dev/aat")


def read_image_records(source_commit, owner):
    """Treat artifact JSON as data: validate it before using any field."""
    matrix = json.loads(os.environ["BUILD_MATRIX"])
    expected = [entry["service"] for entry in matrix["include"]]
    if not expected or len(set(expected)) != len(expected):
        raise ValueError("Build matrix is empty or duplicated")
    if not set(expected).issubset(SERVICE_SOURCE_FOLDERS):
        raise ValueError("Build matrix contains an unknown service")

    folder = Path(os.environ["IMAGE_METADATA_DIR"])
    records = []
    for path in sorted(folder.glob("*.json")):
        if path.is_symlink() or path.stat().st_size > 16384:
            raise ValueError("Unexpected image metadata file")
        record = json.loads(path.read_text())
        required_fields = {"service", "repository", "digest", "source_commit"}
        if not isinstance(record, dict) or set(record) != required_fields:
            raise ValueError("Unexpected image metadata fields")

        service = record["service"]
        if not isinstance(service, str) or service not in SERVICE_SOURCE_FOLDERS:
            raise ValueError("Unknown image service")
        if path.name != service + ".json":
            raise ValueError("Image artifact filename does not match its service")
        if record["repository"] != f"ghcr.io/{owner}/aat-apps/{service}":
            raise ValueError("Image repository is outside this project's GHCR namespace")
        if record["source_commit"] != source_commit:
            raise ValueError("Image was built from a different commit")
        if not isinstance(record["digest"], str) or not re.fullmatch(DIGEST_PATTERN, record["digest"]):
            raise ValueError("Invalid image digest")
        records.append(record)

    actual = [record["service"] for record in records]
    if sorted(actual) != sorted(expected):
        raise ValueError("Published image metadata is missing or duplicated")
    return records


def require_public_image(repository, digest):
    """Check anonymous GHCR access; never send the workflow's write token here."""
    package = repository.removeprefix("ghcr.io/")
    query = urllib.parse.urlencode({"service": "ghcr.io", "scope": f"repository:{package}:pull"})
    try:
        with urllib.request.urlopen("https://ghcr.io/token?" + query, timeout=30) as response:
            anonymous_token = json.load(response)["token"]
        request = urllib.request.Request(
            f"https://ghcr.io/v2/{package}/manifests/{digest}",
            method="HEAD",
            headers={
                "Authorization": "Bearer " + anonymous_token,
                "Accept": ", ".join((
                    "application/vnd.oci.image.index.v1+json",
                    "application/vnd.oci.image.manifest.v1+json",
                    "application/vnd.docker.distribution.manifest.list.v2+json",
                    "application/vnd.docker.distribution.manifest.v2+json",
                )),
            },
        )
        with urllib.request.urlopen(request, timeout=30):
            pass
    except (OSError, KeyError) as error:
        raise RuntimeError(
            f"{repository}@{digest} is not anonymously pullable. "
            "Make the GHCR package public before retrying the GitOps job. "
            "Existing deployment pins were not committed."
        ) from error


def services_with_newer_source(source_commit):
    """Reject a history rewrite and detect source that changed during the build."""
    subprocess.run(["git", "merge-base", "--is-ancestor", source_commit, "HEAD"], cwd=REPOSITORY_ROOT, check=True)
    paths = subprocess.check_output(
        ["git", "diff", "--no-renames", "--name-only", source_commit, "HEAD"],
        cwd=REPOSITORY_ROOT, text=True,
    ).splitlines()
    return set(services_affected_by_changes(paths))


def image_pin_path(service):
    """Do not follow a repository symlink outside the fixed image-pin location."""
    path = REPOSITORY_ROOT / IMAGE_FOLDER / service / "image.yaml"
    expected = REPOSITORY_ROOT.resolve() / IMAGE_FOLDER / service / "image.yaml"
    if not path.is_file() or path.is_symlink() or path.resolve() != expected:
        raise ValueError(f"Unexpected image pin path for {service}")
    return path


def write_image_pin(record, source_commit):
    """Write plain YAML, using only fields validated by read_image_records."""
    path = image_pin_path(record["service"])
    path.write_text(
        f"# Published from AAT source commit {source_commit}.\n"
        "image:\n"
        f"  repository: {record['repository']}\n"
        f"  tag: sha-{source_commit}\n"
        f"  digest: {record['digest']}\n"
    )
    print(f"Pin {record['service']}: {record['digest']}")


def main():
    source_commit = os.environ["BUILD_COMMIT"]
    owner = os.environ["GITHUB_REPOSITORY_OWNER"].lower()
    if not re.fullmatch(COMMIT_PATTERN, source_commit):
        raise ValueError("Invalid source commit")
    if not re.fullmatch(r"[a-z0-9]+(?:-[a-z0-9]+)*", owner):
        raise ValueError("Invalid GHCR owner")

    # 1. Validate the complete artifact set before trusting its values.
    records = read_image_records(source_commit, owner)
    newer_services = services_with_newer_source(source_commit)

    # 2. Validate every selected target and image before modifying any file.
    updates = []
    for record in records:
        if record["service"] in newer_services:
            print(f"Skip {record['service']}: main contains newer build inputs.")
            continue
        image_pin_path(record["service"])
        require_public_image(record["repository"], record["digest"])
        updates.append(record)

    # 3. Change only the permitted image pins, never manifests or application code.
    for record in updates:
        write_image_pin(record, source_commit)


if __name__ == "__main__":
    main()
