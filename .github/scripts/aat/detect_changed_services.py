#!/usr/bin/env python3
"""Choose Dockerfiles to build from the event's changed paths."""
import json
import os
import re
from pathlib import Path
import subprocess

from service_build_config import (
    AAT_SOURCE_DIRECTORY,
    REPOSITORY_ROOT,
    SERVICE_SOURCE_FOLDERS,
    services_affected_by_changes,
)


def main():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    event_name = os.environ["GITHUB_EVENT_NAME"]
    if event_name == "workflow_dispatch":
        selected = sorted(SERVICE_SOURCE_FOLDERS)
    else:
        base = event["before"]
        if not base or set(base) == {"0"}:
            selected = sorted(SERVICE_SOURCE_FOLDERS)
        else:
            if not re.fullmatch(r"[a-f0-9]{40}", base):
                raise ValueError("Unexpected base commit in the GitHub event")
            changed = subprocess.check_output(
                ["git", "diff", "--no-renames", "--name-only", base, "HEAD"], cwd=REPOSITORY_ROOT, text=True,
            ).splitlines()
            selected = services_affected_by_changes(changed)
    matrix = {"include": [
        {"service": name, "dockerfile": AAT_SOURCE_DIRECTORY + SERVICE_SOURCE_FOLDERS[name] + "/Dockerfile"}
        for name in selected
    ]}
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write("matrix=" + json.dumps(matrix) + "\n")
        output.write("has_images=" + str(bool(selected)).lower() + "\n")
    print("Images to build: " + (", ".join(selected) or "none (no image changes)"))


if __name__ == "__main__":
    main()
