#!/usr/bin/env python3
"""Print immutable application image refs without a YAML dependency."""
from pathlib import Path
import re

root = Path(__file__).resolve().parents[1] / "argo-apps/dev/aat"
for path in sorted(root.glob("*/values.yaml")):
    repository = re.search(r"^  repository: (\S+)$", path.read_text(), re.M).group(1)
    digest = re.search(r"^  digest: (sha256:[a-f0-9]{64})$", (path.parent / "image.yaml").read_text(), re.M).group(1)
    print(repository + "@" + digest)
