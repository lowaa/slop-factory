#!/usr/bin/env bash
# Shared library for each project's quality.sh; see dev-tools/quality/README.md.
# Source it, then hand it the project's directory and the arguments:
#
#   here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
#   . "$(git -C "${here}" rev-parse --show-toplevel)/dev-tools/quality/quality_lib.sh"
#   quality_go "${here}" "$@"
#
# Environment:
#   QUALITY_REPORT_DIR  where --report writes, a directory per project
#                       (default <repo>/quality-reports)
#   QUALITY_BIN         a quality CLI to run instead of building this one

QUALITY_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=SCRIPTDIR/../require_bash.sh
. "${QUALITY_LIB_DIR}/../require_bash.sh"
QUALITY_ROOT="$(cd "${QUALITY_LIB_DIR}/../.." && pwd)"
QUALITY_MEASURES=coverage,complexity,testtime

quality_usage() {
  cat >&2 << 'EOF'
usage: quality.sh [--prune | --rebaseline | --report] [--only=MEASURE[,MEASURE]]
                  [--runs=N] [--coverprofile=FILE]... [--coverdir=DIR]... [--mutation[=changed|all]]
measures: coverage complexity testtime
  (default)           check: fail on any regression against quality_baseline/
  --prune             tighten the baselines where a measure improved; never loosens
  --rebaseline        rewrite the baselines from the current values (deliberate)
  --report            write the full reports to $QUALITY_REPORT_DIR; never fails
  --runs=N            run the suite N times; coverage counts what every run covers
                      (default: quality_baseline/coverage_runs for --prune and
                      --rebaseline, else 1)
  --coverprofile=FILE reuse a cover profile rather than running the tests; repeatable
  --coverdir=DIR      merge a GOCOVERDIR (an e2e run's) into the coverage; repeatable
  --mutation          mutation testing lives in dev-tools/mutation/mutation.sh; exits 2
EOF
}

# quality_abs PATH: PATH made absolute against the current directory, so a
# path the caller gave still means the same after quality_go's cd.
quality_abs() {
  case "$1" in
    /*) echo "$1" ;;
    *) echo "${PWD}/$1" ;;
  esac
}

QUALITY_REPORT_DIR="$(quality_abs "${QUALITY_REPORT_DIR:-${QUALITY_ROOT}/quality-reports}")"

# quality_parse_args ARGS...: sets QUALITY_MODE (check, prune, rebaseline or
# report), QUALITY_ONLY, QUALITY_RUNS (empty unless given), QUALITY_PROFILES,
# QUALITY_COVERDIRS and QUALITY_MUTATION (empty, changed or all). Returns 2 on
# a usage error.
quality_parse_args() {
  QUALITY_MODE=check QUALITY_ONLY="${QUALITY_MEASURES}" QUALITY_MUTATION="" QUALITY_RUNS=""
  QUALITY_PROFILES=() QUALITY_COVERDIRS=()
  local modes=0 m only
  while (($#)); do
    case "$1" in
      --prune | --rebaseline | --report)
        QUALITY_MODE="${1#--}"
        modes=$((modes + 1))
        ;;
      --only=?*)
        QUALITY_ONLY="${1#--only=}"
        IFS=, read -r -a only <<< "${QUALITY_ONLY}"
        for m in "${only[@]}"; do
          [[ ",${QUALITY_MEASURES}," == *",${m},"* ]] || {
            echo "quality.sh: unknown measure '${m}'" >&2
            quality_usage
            return 2
          }
        done
        ;;
      --runs=?*)
        QUALITY_RUNS="${1#--runs=}"
        quality_positive "${QUALITY_RUNS}" || {
          echo "quality.sh: --runs takes a positive number, not '${QUALITY_RUNS}'" >&2
          return 2
        }
        ;;
      --coverprofile=?*) QUALITY_PROFILES+=("$(quality_abs "${1#*=}")") ;;
      --coverdir=?*) QUALITY_COVERDIRS+=("$(quality_abs "${1#*=}")") ;;
      --mutation) QUALITY_MUTATION=changed ;;
      --mutation=changed | --mutation=all) QUALITY_MUTATION="${1#*=}" ;;
      -h | --help)
        quality_usage
        return 2
        ;;
      *)
        echo "quality.sh: unknown argument '$1'" >&2
        quality_usage
        return 2
        ;;
    esac
    shift
  done
  ((modes <= 1)) || {
    echo "quality.sh: --prune, --rebaseline and --report are exclusive" >&2
    return 2
  }
  [[ -z "${QUALITY_RUNS}" || ${#QUALITY_PROFILES[@]} -eq 0 ]] || {
    echo "quality.sh: --runs runs the suite, and --coverprofile stands in for it: give one" >&2
    return 2
  }
}

# quality_positive S: whether S is a positive whole number.
quality_positive() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]]
}

# quality_runs: how many times to run the suite. --runs says, or else, for
# the modes that write a baseline, the project's quality_baseline/coverage_runs
# (its first line that isn't a comment): a project whose coverage some timing
# moves keeps out of its baseline what only some runs reach. Otherwise once,
# and always once when coverage isn't measured: only coverage reads the
# other runs.
quality_runs() {
  local f=quality_baseline/coverage_runs n
  if [[ ",${QUALITY_ONLY}," != *,coverage,* ]]; then
    echo 1
  elif [[ -n "${QUALITY_RUNS}" ]]; then
    echo "${QUALITY_RUNS}"
  elif [[ "${QUALITY_MODE}" =~ ^(prune|rebaseline)$ && -f "${f}" ]]; then
    n="$(grep -v -e '^#' -e '^[[:space:]]*$' "${f}" | head -n 1 | tr -d '[:space:]')"
    quality_positive "${n}" || {
      echo "quality.sh: ${f}: want a positive number of runs, not '${n}'" >&2
      return 2
    }
    echo "${n}"
  else
    echo 1
  fi
}

# quality_project_path: the current directory, relative to the repo root.
quality_project_path() {
  local here root
  here="$(pwd -P)"
  root="$(cd "${QUALITY_ROOT}" && pwd -P)"
  here="${here#"${root}"}"
  echo "${here#/}"
}

# quality_slug: the project path with / as -, as scan_lib's report names.
quality_slug() {
  local p
  p="$(quality_project_path)"
  echo "${p:-root}" | tr '/' '-'
}

# quality_runs_tests: whether the selected measures need a test run. A
# --coverprofile stands in for it, so testtime is then skipped.
quality_runs_tests() {
  ((${#QUALITY_PROFILES[@]} == 0)) || return 1
  [[ ",${QUALITY_ONLY}," == *,coverage,* || ",${QUALITY_ONLY}," == *,testtime,* ]]
}

# quality_bin DIR: the quality CLI, built into DIR. GOWORK=off builds it
# against its own go.sum, as dev-tools/lint builds its tools, so the
# workspace's other modules can't move its dependencies.
quality_bin() {
  if [[ -n "${QUALITY_BIN:-}" ]]; then
    echo "${QUALITY_BIN}"
    return
  fi
  (cd "${QUALITY_LIB_DIR}" && GOWORK=off go build -o "$1/quality" ./cmd/quality) >&2 || {
    echo "quality.sh: building the quality CLI failed" >&2
    return 1
  }
  echo "$1/quality"
}

# quality_go_test DIR BIN [RUN]: the suite once, with -race as test.sh runs
# it, for both coverage (of every package of the module, whichever package's
# tests reach it) and test time, into DIR/cover[-RUN].out and
# DIR/test[-RUN].json. Returns 1 for a failing suite, 2 for a module it
# couldn't list.
quality_go_test() {
  local dir="$1" bin="$2" run="${3:+-$3}" listed pkgs rc=0
  listed="$(go list ./...)" || return 2
  [[ -n "${listed}" ]] || {
    echo "quality.sh: go list found no packages in $(quality_project_path)" >&2
    return 2
  }
  mapfile -t pkgs <<< "${listed}"
  # A cached result replays in milliseconds, so timing it needs a real run;
  # and one of several runs (RUN given) would replay the first's coverage.
  local count=()
  [[ ",${QUALITY_ONLY}," != *,testtime,* && -z "${3:-}" ]] || count=(-count=1)
  echo "== go test -race ${count[*]:+${count[*]} }-coverpkg=<the module's ${#pkgs[@]} packages> ./...${3:+ (run $3)}" >&2
  go test -race "${count[@]}" -coverprofile="${dir}/cover${run}.out" -coverpkg="$(
    IFS=,
    echo "${pkgs[*]}"
  )" -json ./... > "${dir}/test${run}.json" || rc=$?
  ((rc == 0)) && return 0
  "${bin}" failures "${dir}/test${run}.json" || true
  echo "quality.sh: go test failed (exit ${rc}); the measures need a passing suite" >&2
  return 1
}

# quality_go PROJECT_DIR ARGS...: quality.sh for a Go module. A subshell,
# so its cd and trap stay its own. Each step checks its own status: set -e
# does nothing in a function called from an `if` or `||`.
quality_go() (
  project="$1"
  shift
  quality_parse_args "$@" || exit
  if [[ -n "${QUALITY_MUTATION}" ]]; then
    echo "quality.sh: --mutation=${QUALITY_MUTATION}: mutation testing is dev-tools/mutation/mutation.sh <project-dir> (dev-tools/mutation/README.md)" >&2
    exit 2
  fi
  cd "${project}" || exit 2
  tmp="$(mktemp -d)" || exit 2
  trap 'rm -rf "${tmp}"' EXIT

  bin="$(quality_bin "${tmp}")" || exit 2
  args=(measure --module . --mode "${QUALITY_MODE}" --only "${QUALITY_ONLY}"
    --prune-cmd "$(quality_project_path)/quality.sh --prune")
  [[ "${QUALITY_MODE}" != report ]] || args+=(--report-dir "${QUALITY_REPORT_DIR}/$(quality_slug)")
  for p in "${QUALITY_PROFILES[@]}"; do args+=(--coverprofile "${p}"); done
  for d in "${QUALITY_COVERDIRS[@]}"; do args+=(--coverdir "${d}"); done
  if quality_runs_tests; then
    runs="$(quality_runs)" || exit
    if ((runs == 1)); then
      quality_go_test "${tmp}" "${bin}" || exit
      args+=(--coverprofile "${tmp}/cover.out" --test-json "${tmp}/test.json")
    else
      for ((i = 1; i <= runs; i++)); do
        quality_go_test "${tmp}" "${bin}" "${i}" || exit
        args+=(--rerun-profile "${tmp}/cover-${i}.out")
      done
      # Each run is timed alike; the first stands for them.
      args+=(--test-json "${tmp}/test-1.json")
    fi
  fi
  "${bin}" "${args[@]}"
)
