#!/usr/bin/env bash
# Security scan; SARIF to $SCAN_REPORT_DIR. See dev-tools/security/README.md.
# trivy config cannot render the chart on its own (it has required values),
# so the chart is scanned as rendered with every feature on.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. "$(git rev-parse --show-toplevel)/dev-tools/security/scan_lib.sh"
# shellcheck source=SCRIPTDIR/lib.sh
. ./lib.sh

# Before rendering: the rendered Secret holds ci/'s placeholders.
scan_secrets

rendered="$(mktemp -d ./.rendered.XXXXXX)"
trap 'rm -rf "${rendered}"' EXIT
helm template slop-haus "${CHART}" -n slop-haus -f "${CHART}/ci/all-features-values.yaml" > "${rendered}/slop-haus.yaml"
scan_config "${rendered#./}" docker_compose argo_cd
