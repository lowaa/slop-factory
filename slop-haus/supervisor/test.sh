#!/usr/bin/env bash
# Runs the same checks as CI: ruff (../../dev-tools/lint/README.md), pyright (strict), the
# suppression ratchet, then pytest.
set -euo pipefail
cd "$(dirname "$0")"
../../dev-tools/lint/ruff.sh .
uv run --frozen pyright
python3 tools/suppression_ratchet.py
uv run --frozen pytest "$@"
