#!/usr/bin/env bash
# Runs the same checks as CI: ruff (../../dev-tools/lint/README.md), pyright (strict), then
# pytest on the default Python and the whole suite on the 3.9 floor (the module runs under
# whatever python3 a workspace has). The floor leg uses unittest: pytest 9 (the first release
# without CVE-2025-71176) needs 3.10+, and the tests are all unittest.TestCase, so they need no
# pytest.
set -euo pipefail
cd "$(dirname "$0")"
../../dev-tools/lint/ruff.sh .
uv run --frozen pyright
uv run --frozen pytest "$@"
PYTHONPATH=. uv run --python 3.9 --isolated --no-dev python -m unittest discover -s tests
