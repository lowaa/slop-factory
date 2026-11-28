#!/usr/bin/env bash
# Coverage, complexity and test time against quality_baseline/; see
# dev-tools/quality/README.md for the flags.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "$(git -C "${here}" rev-parse --show-toplevel)/dev-tools/quality/quality_lib.sh"
quality_go "${here}" "$@"
