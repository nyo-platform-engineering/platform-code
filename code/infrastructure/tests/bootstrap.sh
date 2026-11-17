#!/usr/bin/env bash
# Exercise only local-bootstrap fakes; never modifies the host cluster.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
exec "${PYTHON:-/usr/bin/python3}" -m unittest discover -s tests -p 'test_local_bootstrap.py' -v
