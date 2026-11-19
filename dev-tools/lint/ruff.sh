#!/usr/bin/env bash
#
# ruff over one Python project's files, through ratchet.sh against
# PROJECT_DIR/lint_baseline/ruff.txt. The rules are ruff.toml's; see
# README.md.
#
#   dev-tools/lint/ruff.sh [--prune|--rebaseline] PROJECT_DIR
#
# The files are the project's *.py, tracked or untracked, less those marked
# linguist-generated (the vendored slop_haus_token.py copies) and those of a
# project nested below it, which has a pyproject.toml of its own and is
# linted on its own. ruff is pinned by ruff/uv.lock and run through uv, as
# the Python projects run their own tools.
set -euo pipefail
export LC_ALL=C
# shellcheck source=SCRIPTDIR/../require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../require_bash.sh"
shopt -s inherit_errexit

LINT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${LINT_DIR}/../.." && pwd)"
# shellcheck source=SCRIPTDIR/lib.sh
. "${LINT_DIR}/lib.sh"

mode_flag=""
case "${1:-}" in
  --prune | --rebaseline)
    mode_flag="$1"
    shift
    ;;
esac
(($# == 1)) || {
  echo "usage: ruff.sh [--prune|--rebaseline] PROJECT_DIR" >&2
  exit 2
}
PROJECT="$(cd "$1" && pwd)"
PROJECT_REL="${PROJECT#"${REPO}"/}"
cd "${PROJECT}"

# The Python the project declares it runs on, so a rule that depends on the
# version judges the code by it; a project without a pyproject.toml (the
# harness suite, the workspace scripts) runs on whatever python3 a machine
# has, so on the oldest the repo supports, slop_haus_token.py's 3.9.
target=py39
if [[ -f pyproject.toml ]]; then
  minor="$(sed -nE 's/^requires-python *= *">= *3\.([0-9]+).*/\1/p' pyproject.toml)"
  [[ -z "${minor}" ]] || target="py3${minor}"
fi

nested="$(find . -mindepth 2 -name pyproject.toml -not -path '*/node_modules/*' | sed -e 's|^\./||' -e 's|[^/]*$||')"
files=()
while IFS= read -r f; do
  [[ "${f}" == *.py ]] || continue
  while IFS= read -r n; do
    [[ -z "${n}" || "${f}" != "${n}"* ]] || continue 2
  done <<< "${nested}"
  files+=("${f}")
done < <(lint_files .)

echo "== ${PROJECT_REL}: ruff" >&2
findings=""
if ((${#files[@]})); then
  rc=0
  out="$(uv run --quiet --frozen --project "${LINT_DIR}/ruff" ruff check \
    --config "${LINT_DIR}/ruff.toml" --target-version "${target}" \
    --no-cache --quiet --output-format=concise -- "${files[@]}" 2>&1)" || rc=$?
  # 1 means findings; 2 is a broken run (a bad config, a missing file).
  ((rc <= 1)) || {
    printf '%s\n' "${out}" >&2
    exit 1
  }
  # "<file>:<line>:<col>: <rule> [*] <message>": the first `name` the
  # message quotes, when it quotes one, says which function or import. A
  # line of any other shape (a syntax error) fails the run as unrecognised.
  # shellcheck disable=SC2016 # the backquotes are the pattern's, not an expansion
  findings="$(normalise 's/^([^:]+):[0-9]+:[0-9]+: ([A-Z]+[0-9]+) (\[\*\] )?[^`]*(`([^`]*)`)?.*$/\1 \2 \5/' <<< "${out}" |
    awk '{ s = $0; sub(/^[^ ]+ [^ ]+ ?/, "", s); gsub(/ /, "_", s); print $1 " " $2 (s == "" ? "" : " " s) }')" || {
    echo "ruff.sh: ${PROJECT_REL}: ruff did not run cleanly" >&2
    exit 1
  }
fi

cd "${REPO}"
printf '%s\n' "${findings}" |
  "${LINT_DIR}/ratchet.sh" ${mode_flag:+"${mode_flag}"} \
    --regen "dev-tools/lint/ruff.sh --prune ${PROJECT_REL}" \
    "${PROJECT_REL}/lint_baseline/ruff.txt"
