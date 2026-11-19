#!/usr/bin/env bash
#
# The linters that look at the whole repo rather than one module, each
# through ratchet.sh against dev-tools/lint/baseline/<tool>.txt:
#
#   knip   unused files, exports, types and dependencies across the npm
#          workspaces and the e2e suite (knip.jsonc)
#   jscpd  duplicated code across Go and TypeScript (.jscpd.json), counted
#          per pair of files
#
#   dev-tools/lint/repo.sh [--prune|--rebaseline] [--only=TOOL[,TOOL]]
#
# Needs the root `npm ci`: knip loads the workspaces' Vite and Storybook
# configs. CI's lint job runs it; see README.md.
set -euo pipefail
export LC_ALL=C
# shellcheck source=SCRIPTDIR/../require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../require_bash.sh"
shopt -s inherit_errexit

LINT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=SCRIPTDIR/lib.sh
. "${LINT_DIR}/lib.sh"
REPO="$(cd "${LINT_DIR}/../.." && pwd)"
BIN="${REPO}/node_modules/.bin"
ALL_TOOLS=(knip jscpd)

usage() {
  echo "usage: repo.sh [--prune|--rebaseline] [--only=TOOL[,TOOL]]   tools: ${ALL_TOOLS[*]}" >&2
  exit 2
}

mode_flag="" tools=("${ALL_TOOLS[@]}")
while (($#)); do
  case "$1" in
    --prune | --rebaseline) mode_flag="$1" ;;
    --only=?*) IFS=, read -r -a tools <<< "${1#--only=}" ;;
    *) usage ;;
  esac
  shift
done
for t in "${tools[@]}"; do
  [[ " ${ALL_TOOLS[*]} " == *" ${t} "* ]] || {
    echo "repo.sh: unknown tool '${t}'" >&2
    usage
  }
  [[ -x "${BIN}/${t}" ]] || {
    echo "repo.sh: no ${BIN}/${t}; run npm ci at the repo root" >&2
    exit 2
  }
done

cd "${REPO}"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# Each report_<tool> prints the tool's JSON report. Their exit codes say
# only whether there were findings; findings.mjs fails on anything that is
# not a report.
report_knip() {
  local key
  # Its cache (a quarter of the time, warm) keys on each file's content, not
  # on its own config or the installed packages, so those name the cache.
  # CI starts with none, and so runs it cold.
  key="$(cat knip.jsonc package-lock.json | hash16)"
  "${BIN}/knip" --no-progress --reporter json --cache --cache-location "node_modules/.cache/knip-${key}" || true
}

report_jscpd() {
  "${BIN}/jscpd" --config .jscpd.json --output "${WORK}/jscpd" --silent . > /dev/null || return 1
  cat "${WORK}/jscpd/jscpd-report.json"
}

failed=()
for tool in "${tools[@]}"; do
  echo "== ${tool}" >&2
  if ! report="$("report_${tool}")" || ! found="$(node "${LINT_DIR}/findings.mjs" "${tool}" <<< "${report}")"; then
    failed+=("${tool} (did not run)")
    continue
  fi
  printf '%s\n' "${found}" |
    "${LINT_DIR}/ratchet.sh" ${mode_flag:+"${mode_flag}"} \
      --regen "dev-tools/lint/repo.sh --prune --only=${tool}" \
      "dev-tools/lint/baseline/${tool}.txt" ||
    failed+=("${tool}")
done

if ((${#failed[@]})); then
  echo "repo.sh: failed: ${failed[*]}" >&2
  exit 1
fi
