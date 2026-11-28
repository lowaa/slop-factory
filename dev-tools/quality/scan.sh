#!/usr/bin/env bash
# Security scan; SARIF to $SCAN_REPORT_DIR. See dev-tools/security/README.md.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. "$(git rev-parse --show-toplevel)/dev-tools/security/scan_lib.sh"
scan_go
scan_secrets
