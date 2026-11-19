"""Shared P1 helpers, grouped by responsibility."""

from pathlib import Path
import sys

# Support running P1 scripts directly as well as importing their groups.
M2_DIR = Path(__file__).resolve().parents[2]
if str(M2_DIR) not in sys.path:
    sys.path.insert(0, str(M2_DIR))
