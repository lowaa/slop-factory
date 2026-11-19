#!/usr/bin/env bash
#
# The Go linters over one module, each through ratchet.sh against that
# module's baseline. Every module's test.sh calls it; see README.md.
#
#   dev-tools/lint/go.sh [--prune|--rebaseline] [--only=TOOL[,TOOL]] MODULE_DIR
#
# Tools: staticcheck errcheck gosec gocognit revive, and deadcode for a module
# that has a deadcode/ directory (an app with mains under cmd/). Baselines are
# MODULE_DIR/lint_baseline/<tool>.txt; deadcode's are deadcode/baseline.txt
# and deadcode/allowlist.txt.
#
# On the host, not in the test containers: it needs no database, only Go,
# which CI installs for every Go job, and the tools build once into a cache
# keyed by go.mod and go.sum.
set -euo pipefail
export LC_ALL=C
# shellcheck source=SCRIPTDIR/../require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../require_bash.sh"
shopt -s inherit_errexit

LINT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "${LINT_DIR}/../.." && pwd)"
# shellcheck source=SCRIPTDIR/lib.sh
. "${LINT_DIR}/lib.sh"
ALL_TOOLS=(staticcheck errcheck gosec gocognit revive deadcode)
# Cognitive complexity above this fails as new debt; README.md says why 20.
GOCOGNIT_OVER="${GOCOGNIT_OVER:-20}"

usage() {
  echo "usage: go.sh [--prune|--rebaseline] [--only=TOOL[,TOOL]] MODULE_DIR" >&2
  echo "tools: ${ALL_TOOLS[*]}" >&2
  exit 2
}

mode_flag="" only=""
while (($#)); do
  case "$1" in
    --prune | --rebaseline) mode_flag="$1" ;;
    --only=*)
      only="${1#--only=}"
      [[ -n "${only}" ]] || usage
      ;;
    -*) usage ;;
    *) break ;;
  esac
  shift
done
(($# == 1)) || usage
MODULE="$(cd "$1" && pwd)"
[[ -f "${MODULE}/go.mod" ]] || {
  echo "go.sh: ${MODULE} has no go.mod" >&2
  exit 2
}
MODULE_REL="${MODULE#"${REPO}"/}"

tools=()
if [[ -n "${only}" ]]; then
  IFS=, read -r -a tools <<< "${only}"
  for t in "${tools[@]}"; do
    [[ " ${ALL_TOOLS[*]} " == *" ${t} "* ]] || {
      echo "go.sh: unknown tool '${t}'" >&2
      usage
    }
  done
else
  tools=("${ALL_TOOLS[@]}")
fi

# The tools, built from dev-tools/lint/go.mod's tool directives into a
# directory named for its go.mod and go.sum, so a version bump rebuilds and
# nothing else does. go.mod's go line matters: the tools type-check with
# the go/types they were built with, so they must be built by the repo's
# toolchain or they reject newer language features (GOTOOLCHAIN=auto
# fetches it where the installed Go is older).
build_tools() {
  local key dir tmp
  key="$(cat "${LINT_DIR}/go.mod" "${LINT_DIR}/go.sum" | sha256sum | cut -c1-16)"
  dir="${LINT_GO_BIN_DIR:-${XDG_CACHE_HOME:-${HOME}/.cache}/slop-factory/lint-go-${key}}"
  if [[ ! -f "${dir}/.built" ]]; then
    mkdir -p "$(dirname "${dir}")"
    tmp="$(mktemp -d "${dir}.XXXXXX")"
    echo "go.sh: building the lint tools into ${dir}" >&2
    (cd "${LINT_DIR}" && GOWORK=off GOTOOLCHAIN=auto go build -o "${tmp}/" tool)
    touch "${tmp}/.built"
    install_dir "${tmp}" "${dir}"
    [[ -f "${dir}/.built" ]] || {
      echo "go.sh: could not put the lint tools in ${dir}" >&2
      exit 1
    }
  fi
  BIN="${dir}"
}

# Each run_<tool> prints the module's findings, one "<file> <rule> [<symbol>]"
# line each (paths relative to the module, no line numbers), and fails if
# the tool itself did not run cleanly: a tool that breaks must not read as
# a clean module.

run_staticcheck() {
  local out rc=0
  out="$("${BIN}/staticcheck" ./... 2>&1)" || rc=$?
  # 1 means findings; a package that does not build is reported as a
  # "compile" finding, which is a broken run, not debt.
  if ((rc > 1)) || grep -q '(compile)$' <<< "${out}"; then
    printf '%s\n' "${out}" >&2
    return 1
  fi
  normalise 's/^([^:]+):[0-9]+:[0-9]+: .* \(([A-Z]+[0-9]+)\)$/\1 \2/' <<< "${out}"
}

run_errcheck() {
  local out rc=0
  # Anything but `_ =` that drops an error is a finding (the repo's rule).
  # errcheck's own built-in excludes stay: fmt.Print* and the writers that
  # document they never fail.
  out="$("${BIN}/errcheck" -ignoregenerated ./... 2>&1)" || rc=$?
  ((rc <= 1)) || {
    printf '%s\n' "${out}" >&2
    return 1
  }
  # "<file>:<line>:<col>:\t<source>": the call itself names the finding.
  normalise 's/^([^:]+):[0-9]+:[0-9]+:[[:space:]]+(.*)$/\1 errcheck \2/' <<< "${out}" | tr -s ' \t' ' '
}

run_gosec() {
  local out
  # Without the test files (gosec's default). G104, unhandled errors, is
  # errcheck's job here. A load error is not a gosec failure (it reports
  # nothing): staticcheck, run alongside, fails on it.
  out="$("${BIN}/gosec" -quiet -no-fail -exclude-generated -exclude=G104 -fmt=golint ./... 2>&1)" || {
    printf '%s\n' "${out}" >&2
    return 1
  }
  # Its paths are absolute; the module prefix is cut as a literal string,
  # not spliced into the pattern, where a "." in it would match anything.
  normalise 's/^([^:]+):[0-9]+:[0-9]+: .*\(Rule:(G[0-9]+), .*\)$/\1 \2/' <<< "${out//"${MODULE}/"/}"
}

run_gocognit() {
  local out err rc=0
  err="$(mktemp)"
  # Not the tests: a table-driven test reads fine at any score.
  out="$("${BIN}/gocognit" -test=false -over "${GOCOGNIT_OVER}" \
    -f '{{.Pos.Filename}} gocognit {{.FuncName}}' . 2> "${err}")" || rc=$?
  # It exits 1 for findings and for a file it cannot parse alike, so its
  # stderr tells them apart. The one note it always prints (recursion is
  # under-counted without -exact-recursion, which needs a build) is not
  # an error.
  if ((rc > 1)) || grep -v -q 'gocognit: note: ' "${err}"; then
    cat "${err}" >&2
    rm -f "${err}"
    return 1
  fi
  rm -f "${err}"
  [[ -z "${out}" ]] || sed 's|^\./||' <<< "${out}"
}

run_revive() {
  local out
  out="$("${BIN}/revive" -config "${LINT_DIR}/revive.toml" -formatter unix ./... 2>&1)" || {
    printf '%s\n' "${out}" >&2
    return 1
  }
  normalise 's/^([^:]+):[0-9]+:[0-9]+: \[([a-z-]+)\] .*$/\1 \2/' <<< "${out}"
}

# Exported functions and methods no main under cmd/ reaches, as "<file>
# <symbol>", and "<dir>/ package" for packages no main imports: deadcode
# only sees imported packages, so a package landed wholly unwired would
# otherwise not show at all. Unexported ones are left to staticcheck's U1000.
# Only apps opt in (a deadcode/ directory): an unused export in a library is
# normal.
run_deadcode() {
  local mains=() d dc all reached
  for d in cmd/*/; do [[ -d "${d}" ]] && mains+=("./${d%/}"); done
  ((${#mains[@]})) || {
    echo "go.sh: deadcode needs mains under ${MODULE_REL}/cmd/" >&2
    return 1
  }
  dc="$("${BIN}/deadcode" "${mains[@]}")" || return 1
  all="$(go list -f '{{.Dir}}' ./... | sort -u)" || return 1
  reached="$(go list -deps -f '{{if not .Standard}}{{.Dir}}{{end}}' "${mains[@]}" | sort -u)" || return 1
  [[ -z "${dc}" ]] || normalise 's/^([^:]+):[0-9]+:[0-9]+: unreachable func: (.*)$/\1 \2/' <<< "${dc}" |
    awk '{ k = split($2, p, "."); if (p[k] ~ /^[A-Z]/) print }' || return 1
  comm -23 <(printf '%s\n' "${all}") <(printf '%s\n' "${reached}") |
    sed -E "s|^${MODULE}/?||; s|\$|/ package|; s|^/ package\$|./ package|"
}

# own_findings: drop findings in a module nested below this one
# (slop-review/report under slop-review). gosec walks directories, and
# deadcode's filter is a prefix of the module path, so both reach into it;
# the nested module is linted, and baselined, on its own.
own_findings() {
  if [[ -z "${NESTED}" ]]; then
    cat
    return
  fi
  awk -v nested="${NESTED}" '
    BEGIN { n = split(nested, skip, "\n") }
    { for (i = 1; i <= n; i++) if (index($1, skip[i]) == 1) next; print }'
}

baseline_args() { # baseline_args TOOL: ratchet.sh's baseline and allow-list
  if [[ "$1" == deadcode ]]; then
    RATCHET_ARGS=(--allowlist "${MODULE_REL}/deadcode/allowlist.txt" "${MODULE_REL}/deadcode/baseline.txt")
  else
    RATCHET_ARGS=("${MODULE_REL}/lint_baseline/$1.txt")
  fi
}

build_tools
cd "${MODULE}"
# Every module resolves through the root workspace, as the apps build:
# artifact-store has no go.sum of its own, and the apps import siblings.
# (LINT_GOWORK: the tests lint scratch modules outside it.)
export GOWORK="${LINT_GOWORK:-${REPO}/go.work}"
NESTED="$(find . -mindepth 2 -name go.mod -not -path '*/node_modules/*' | sed -e 's|^\./||' -e 's|[^/]*$||')"

failed=()
for tool in "${tools[@]}"; do
  if [[ "${tool}" == deadcode && ! -d "${MODULE}/deadcode" ]]; then
    [[ -z "${only}" ]] || echo "go.sh: ${MODULE_REL} has no deadcode/ directory; skipped" >&2
    continue
  fi
  echo "== ${MODULE_REL}: ${tool}" >&2
  # errexit does not reach into a function called as a condition, so every
  # run_<tool> returns its failures explicitly.
  if ! findings="$("run_${tool}")"; then
    failed+=("${tool} (did not run)")
    continue
  fi
  baseline_args "${tool}"
  printf '%s\n' "${findings}" | own_findings |
    (cd "${REPO}" && "${LINT_DIR}/ratchet.sh" ${mode_flag:+"${mode_flag}"} \
      --regen "dev-tools/lint/go.sh --prune --only=${tool} ${MODULE_REL}" \
      "${RATCHET_ARGS[@]}") ||
    failed+=("${tool}")
done

if ((${#failed[@]})); then
  echo "go.sh: ${MODULE_REL}: failed: ${failed[*]}" >&2
  exit 1
fi
