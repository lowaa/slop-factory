#!/usr/bin/env bash
# Security scan; SARIF to $SCAN_REPORT_DIR.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. "$(git rev-parse --show-toplevel)/dev-tools/security/scan_lib.sh"
scan_go
scan_secrets
# The images this project builds, when the pipeline names them (SCAN_IMAGE,
# space-separated); skipped locally.
scan_image "${SCAN_IMAGE:-}"
