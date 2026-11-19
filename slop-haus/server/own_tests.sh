#!/usr/bin/env bash
#
# Runs the Go test files an attempt added or changed twice -- on its head, and
# on the merge-base with only those test files laid over -- and prints one
# JSON array of {Path, Base, Head} (attemptsignals/owntests.FileRun) between
# OWNTESTS_JSON_BEGIN and OWNTESTS_JSON_END. The server reads that block from
# the CI step's log; no block, or one it can't parse, means "unknown".
#
#   own_tests.sh run <base-ref>                      # in a checkout of the head
#   own_tests.sh summarize <manifest> <head> <base>  # the pure step, for tests
#
# Results are per file though `go test` reports per test: a file's result is
# the outcome of the top-level tests it declares. Only slop-haus/server's Go
# tests are covered; other languages extend `changed_test_files`/`run_tree`.
# It always exits 0 once it has printed a block: a test failing on the base is
# the good outcome, so this must never fail the pull request.
set -euo pipefail
# shellcheck source=SCRIPTDIR/../../dev-tools/require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../../dev-tools/require_bash.sh"

MODULE_DIR=slop-haus/server

# Per manifest entry {path, pkg, tests[]} and the `go test -json` events of
# each side. A missing package in the events is "unknown" (the run didn't
# happen); a build failure is "compile-fail", the good case for a new test on
# a base that lacks what it tests.
read -r -d '' SUMMARIZE_JQ << 'JQ' || true
def side($events; $f):
  [$events[] | select(.Package == $f.pkg)] as $e
  | if ($e | length) == 0 then "unknown"
    elif $e | any(.Action == "output" and ((.Output // "") | test("\\[(build|setup) failed\\]"))) then "compile-fail"
    else
      [$e[] | select(.Test != null and (.Test as $t | $f.tests | index($t)) and (.Action | IN("pass", "fail", "skip")))] as $t
      | ($e | any(.Test == null and (.Action | IN("pass", "fail", "skip")))) as $finished
      | if $t | any(.Action == "fail") then "fail"
        # An interrupted run leaves some tests unreported: not a pass.
        elif ($finished | not) and (($t | length) < ($f.tests | length)) then "unknown"
        elif $t | any(.Action == "pass") then "pass"
        elif $e | any(.Test == null and .Action == "fail") then "fail"
        else "did-not-run" end
    end;
$manifest[0] as $m
| [$m[] | {Path: .path, Base: side($base; .), Head: side($head; .)}]
JQ

events() {
  # Not every line of the output is an event (docker build chatter).
  jq -cR 'fromjson? | select(type == "object")' "$1"
}

summarize() {
  local manifest=$1 head=$2 base=$3
  jq -n -c \
    --slurpfile manifest "${manifest}" \
    --slurpfile head <(events "${head}") \
    --slurpfile base <(events "${base}") \
    "${SUMMARIZE_JQ}"
}

changed_test_files() {
  git diff --name-only --diff-filter=AMR "$1...HEAD" -- "${MODULE_DIR}" | grep '_test\.go$' || true
}

module_path() {
  sed -nE 's/^module[[:space:]]+//p' "${MODULE_DIR}/go.mod"
}

# manifest <module> <file>...: {path, pkg, tests[]} per file, the tests being
# the top-level Test functions the file declares.
manifest() {
  local module=$1 f dir pkg tests
  shift
  for f in "$@"; do
    dir=$(dirname "${f#"${MODULE_DIR}"/}")
    # A package at the module root is the module path itself.
    pkg="${module}/${dir}"
    [[ "${dir}" == "." ]] && pkg="${module}"
    tests=$(grep -oE '^func Test[A-Za-z0-9_]*\(' "$f" | sed 's/^func //; s/($//' | jq -R . | jq -sc . || true)
    jq -nc --arg path "$f" --arg pkg "${pkg}" --argjson tests "${tests:-[]}" \
      '{path: $path, pkg: $pkg, tests: $tests}'
  done | jq -sc .
}

# run_tree <dir> <out> <pattern>: the Go suite of the checkout in <dir>, as
# test.sh runs it (so the same containers and reaping), limited by -run.
# Failing tests make test.sh exit non-zero; that is data, not an error.
run_tree() {
  (cd "$1" && SUITE=go "${MODULE_DIR}/test.sh" -json -count=1 -run "$3") > "$2" 2> "$2.err" || true
}

# Global: the EXIT trap runs after run()'s locals are gone.
work=
cleanup() {
  [[ -n "${work}" ]] || return 0
  git worktree remove --force "${work}/base" > /dev/null 2>&1 || true
  rm -rf "${work}"
}

run() {
  local base_ref=$1 merge_base files module pattern
  merge_base=$(git merge-base "${base_ref}" HEAD)
  mapfile -t files < <(changed_test_files "${merge_base}")
  if ((${#files[@]} == 0)); then
    emit '[]'
    return
  fi
  module=$(module_path)
  work=$(mktemp -d)
  trap cleanup EXIT
  manifest "${module}" "${files[@]}" > "${work}/manifest.json"
  pattern="^($(jq -r '[.[].tests[]] | unique | join("|")' "${work}/manifest.json"))\$"

  git worktree add --detach "${work}/base" "${merge_base}" > /dev/null
  local f
  for f in "${files[@]}"; do
    mkdir -p "${work}/base/$(dirname "$f")"
    cp "$f" "${work}/base/$f"
  done

  run_tree . "${work}/head.json" "${pattern}"
  run_tree "${work}/base" "${work}/base.json" "${pattern}"
  emit "$(summarize "${work}/manifest.json" "${work}/head.json" "${work}/base.json")"
}

emit() {
  echo "OWNTESTS_JSON_BEGIN"
  echo "$1"
  echo "OWNTESTS_JSON_END"
}

case "${1:-}" in
  run) run "${2:?usage: own_tests.sh run <base-ref>}" ;;
  summarize) summarize "${2:?manifest}" "${3:?head}" "${4:?base}" ;;
  manifest)
    shift
    manifest "$@"
    ;;
  *)
    echo "usage: own_tests.sh run <base-ref> | summarize <manifest> <head> <base>" >&2
    exit 2
    ;;
esac
