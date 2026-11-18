#!/usr/bin/env bash
#
# A complexity report for the Go and TypeScript code: per function, the
# cyclomatic complexity (CCN), length in lines and tokens, and parameter
# count, from lizard. Report-only, like the security scans: it never fails
# on what it finds, and goes beside their reports.
#
#   dev-tools/lint/complexity_report.sh [PATH...]   default: the whole repo
#
# Writes ${SCAN_REPORT_DIR:-<repo>/security-reports}/lizard-<slug>.csv (every
# function) and .txt (lizard's warnings: functions over its CCN 15, length
# 1000 and 100-parameter defaults, so mostly the CCN ones), the slug being
# the path with / as -, or "repo".
set -euo pipefail

LINT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${LINT_DIR}/../.." && pwd)"
REPORT_DIR="${SCAN_REPORT_DIR:-${REPO}/security-reports}"

cd "${REPO}"
paths=("$@")
slug=repo
if ((${#paths[@]})); then
  slug="$(printf '%s-' "${paths[@]}" | sed 's|/|-|g; s|-*$||; s|--*|-|g')"
else
  paths=(.)
fi
mkdir -p "${REPORT_DIR}"

# Generated code, vendored installs and the gitignored copies are not ours
# to simplify.
excludes=(
  -x './node_modules/*' -x '*/node_modules/*'
  -x '*/api_generated/*' -x '*/testdata/*' -x '*_gen.go'
  -x './.claude/*' -x '*/e2e_support/*' -x '*/dist/*'
)

lizard() {
  uv run --quiet --frozen --project "${LINT_DIR}/lizard" lizard \
    -l go -l typescript -l tsx "${excludes[@]}" "$@" "${paths[@]}"
}

{
  echo "nloc,ccn,tokens,params,length,location,file,function,signature,start,end"
  lizard --csv
} > "${REPORT_DIR}/lizard-${slug}.csv"
# --warnings_only exits 1 when there are warnings, which is the report.
lizard --warnings_only > "${REPORT_DIR}/lizard-${slug}.txt" || true
echo "complexity report: ${REPORT_DIR}/lizard-${slug}.{csv,txt} ($(grep -c ': warning: ' "${REPORT_DIR}/lizard-${slug}.txt" || true) functions over lizard's limits)"
