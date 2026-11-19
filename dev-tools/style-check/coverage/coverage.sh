#!/usr/bin/env bash
#
# Reports the style rules nothing uses: renders every story of the sites in
# config.json under Chromium's rule-usage tracking, adds the
# captures the e2e suites wrote (see README.md), and lists, per SCSS file,
# the rules no render applied. It removes nothing.
#
#   coverage.sh [--out <dir>] [--site <dir>]... [--e2e <dir>]... [--include <path-prefix>]...
#
# --site limits the stories to some of the config's sites (default: all).
# --e2e is a directory of capture files from an e2e run (CSS_COVERAGE_DIR).
# --include limits the report to source files under a path prefix.
# The report is <out>/report.md and report.json (default out: ./css-coverage);
# the captures it was made from are <out>/captures/.
#
# Exit 0: report written. 2: a render or the report failed, or bad usage.
# Unused is not the same as removable: a state no story shows is reported
# too. README.md says how to triage.
set -euo pipefail

# shellcheck source=SCRIPTDIR/../../require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../../require_bash.sh"
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TOP="$(cd -- "${HERE}/../../.." && pwd)"
CONFIG="${HERE}/config.json"

usage() {
  echo "usage: $0 [--out <dir>] [--site <dir>]... [--e2e <dir>]... [--include <path-prefix>]..." >&2
  exit 2
}

OUT="css-coverage"
SITES=()
E2E=()
INCLUDE=()
while (($#)); do
  case "$1" in
    --out)
      OUT="${2:?--out needs a directory}"
      shift 2
      ;;
    --site)
      SITES+=("${2:?--site needs a directory}")
      shift 2
      ;;
    --e2e)
      E2E+=("${2:?--e2e needs a directory}")
      shift 2
      ;;
    --include)
      INCLUDE+=(--include "${2:?--include needs a path prefix}")
      shift 2
      ;;
    *) usage ;;
  esac
done
[[ ${OUT} == /* ]] || OUT="${PWD}/${OUT}"
if ((${#SITES[@]} == 0)); then
  mapfile -t SITES < <(node -p "Object.keys(require(process.argv[1]).sites).join('\n')" "${CONFIG}")
fi

# Only what this script writes: --out may hold more.
rm -rf "${OUT}/captures" "${OUT}/report.md" "${OUT}/report.json"
mkdir -p "${OUT}/captures"

CAPTURES=()
for site in "${SITES[@]}"; do
  capture="${OUT}/captures/stories-${site//\//-}.json"
  node "${HERE}/collect_stories.mjs" "${CONFIG}" "${site}" "${capture}" || {
    echo "coverage: rendering the ${site} stories failed" >&2
    exit 2
  }
  CAPTURES+=("${capture}")
done
for dir in "${E2E[@]}"; do
  [[ -d ${dir} ]] || {
    echo "coverage: no such e2e capture directory: ${dir}" >&2
    exit 2
  }
  while IFS= read -r f; do CAPTURES+=("${f}"); done < <(find "${dir}" -name '*.json' | sort)
done

node "${HERE}/report.mjs" --root "${TOP}" --out "${OUT}" "${INCLUDE[@]}" "${CAPTURES[@]}" || exit 2
