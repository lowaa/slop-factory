#!/usr/bin/env bash
#
# oxlint over one area (slop-haus/frontend, js-components), type-aware, through
# ratchet.sh against AREA/lint_baseline/oxlint.txt. Each package's
# .oxlintrc.json extends dev-tools/lint/oxlintrc.base.json; see README.md.
#
#   dev-tools/lint/oxlint.sh [--prune|--rebaseline] AREA_DIR
#
# Runs where the area's full npm install is (the test images, or a local
# checkout after `npm ci`): the type-aware rules need every package's types,
# and without them would quietly find less.
set -euo pipefail
export LC_ALL=C
shopt -s inherit_errexit

LINT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${LINT_DIR}/../.." && pwd)"

mode_flag=""
case "${1:-}" in
  --prune | --rebaseline)
    mode_flag="$1"
    shift
    ;;
esac
(($# == 1)) || {
  echo "usage: oxlint.sh [--prune|--rebaseline] AREA_DIR" >&2
  exit 2
}
AREA="$(cd "$1" && pwd)"
AREA_REL="${AREA#"${REPO}"/}"
OXLINT="${REPO}/node_modules/.bin/oxlint"
[[ -x "${OXLINT}" ]] || {
  echo "oxlint.sh: no ${OXLINT}; run npm ci at the repo root" >&2
  exit 2
}
# The type-aware rules run in oxlint-tsgolint, which oxlint otherwise looks
# for under the area; name the root's, so an area anywhere gets the same one.
export OXLINT_TSGOLINT_PATH="${REPO}/node_modules/.bin/tsgolint"

echo "== ${AREA_REL}: oxlint" >&2
report="$(cd "${AREA}" && "${OXLINT}" --type-aware --format=json .)" || true
found="$(node "${LINT_DIR}/findings.mjs" oxlint <<< "${report}")"
cd "${REPO}"
printf '%s\n' "${found}" |
  "${LINT_DIR}/ratchet.sh" ${mode_flag:+"${mode_flag}"} \
    --regen "dev-tools/lint/oxlint.sh --prune ${AREA_REL}" \
    "${AREA_REL}/lint_baseline/oxlint.txt"
