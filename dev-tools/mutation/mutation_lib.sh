#!/usr/bin/env bash
# Shared library for mutation.sh; see dev-tools/mutation/README.md.
#
# Environment:
#   MUTATION_REPORT_DIR           where --report writes, a directory per project
#                                 (default <repo>/mutation-reports)
#   MUTATION_TIMEOUT_COEFFICIENT  gremlins' per-mutant timeout, as a multiple of
#                                 the package's own test time (default 10)
#   MUTATION_PACKAGE_TIMEOUT      wall-clock seconds one package may take
#                                 (default 1500); a longer run is an error
#   MUTATION_WORKERS              mutants tested in parallel (default min(4, cores))
#   MUTATION_BIN                  a mutation CLI to run instead of building this one
#   MUTATION_GREMLINS_BIN         a gremlins to run instead of building the pinned one

MUTATION_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=SCRIPTDIR/../require_bash.sh
. "${MUTATION_LIB_DIR}/../require_bash.sh"
MUTATION_ROOT="$(cd "${MUTATION_LIB_DIR}/../.." && pwd)"

mutation_usage() {
  cat >&2 << 'EOF'
usage: mutation.sh <project-dir> [--prune | --rebaseline | --report] [--packages=PATTERN[,PATTERN]]
  (default)       check: fail when a package's score falls beyond the tolerance
  --prune         tighten the baseline where a score improved; never loosens
  --rebaseline    record the current scores (deliberate); keeps packages not run
  --report        write the full reports to $MUTATION_REPORT_DIR; never fails
  --packages=...  package patterns relative to the project (./internal/state,
                  ./internal/agents/...); default: mutation_baseline/packages.txt
EOF
}

# mutation_abs PATH: PATH made absolute against the current directory.
mutation_abs() {
  case "$1" in
    /*) echo "$1" ;;
    *) echo "${PWD}/$1" ;;
  esac
}

# mutation_parse_args ARGS...: sets MUTATION_PROJECT, MUTATION_MODE (check,
# prune, rebaseline or report) and MUTATION_PACKAGES (the --packages value,
# empty for the project's list). Returns 2 on a usage error.
mutation_parse_args() {
  MUTATION_PROJECT="" MUTATION_MODE=check MUTATION_PACKAGES=""
  local modes=0
  while (($#)); do
    case "$1" in
      --prune | --rebaseline | --report)
        MUTATION_MODE="${1#--}"
        modes=$((modes + 1))
        ;;
      --packages=?*) MUTATION_PACKAGES="${1#--packages=}" ;;
      -h | --help)
        mutation_usage
        return 2
        ;;
      -*)
        echo "mutation.sh: unknown argument '$1'" >&2
        mutation_usage
        return 2
        ;;
      *)
        [[ -z "${MUTATION_PROJECT}" ]] || {
          echo "mutation.sh: one project directory only, got '${MUTATION_PROJECT}' and '$1'" >&2
          return 2
        }
        MUTATION_PROJECT="$1"
        ;;
    esac
    shift
  done
  [[ -n "${MUTATION_PROJECT}" ]] || {
    echo "mutation.sh: a project directory is required" >&2
    mutation_usage
    return 2
  }
  ((modes <= 1)) || {
    echo "mutation.sh: --prune, --rebaseline and --report are exclusive" >&2
    return 2
  }
}

# mutation_patterns PROJECT_ABS: the package patterns to run, one a line:
# --packages, else the project's mutation_baseline/packages.txt (comments and
# blank lines dropped).
mutation_patterns() {
  if [[ -n "${MUTATION_PACKAGES}" ]]; then
    tr ',' '\n' <<< "${MUTATION_PACKAGES}"
    return
  fi
  local list="$1/mutation_baseline/packages.txt"
  [[ -f "${list}" ]] || {
    echo "mutation.sh: no --packages, and no ${list}" >&2
    return 2
  }
  sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^$/d' "${list}"
}

# mutation_expand PATTERN...: the packages the patterns name, as
# ./<dir relative to the module> lines, sorted and unique. Run in the
# project. A pattern that matches nothing is an error: a typo would
# otherwise quietly measure less.
mutation_expand() {
  local root dir p dirs all=""
  root="$(dirname "$(go env GOMOD)")" || return 2
  for p in "$@"; do
    dirs="$(go list -f '{{.Dir}}' "${p}" 2> /dev/null)" || dirs=""
    [[ -n "${dirs}" ]] || {
      echo "mutation.sh: '${p}' matches no package" >&2
      return 2
    }
    while IFS= read -r dir; do
      all+=".${dir#"${root}"}"$'\n'
    done <<< "${dirs}"
  done
  printf '%s' "${all}" | sort -u
}

# mutation_workers: how many mutants to test at once. Each worker copies the
# module and runs its tests, so more than four mostly adds contention, and
# contention turns into spurious timeouts.
mutation_workers() {
  if [[ -n "${MUTATION_WORKERS:-}" ]]; then
    echo "${MUTATION_WORKERS}"
    return
  fi
  local n
  n="$(getconf _NPROCESSORS_ONLN 2> /dev/null || echo 2)"
  echo $((n < 4 ? n : 4))
}

# mutation_capped SECONDS COMMAND...: COMMAND, killed after SECONDS. macOS has
# no timeout(1) unless coreutils is installed; the run is then uncapped, with
# a note, rather than refused.
mutation_capped() {
  local secs="$1" t
  shift
  for t in timeout gtimeout; do
    if command -v "${t}" > /dev/null; then
      "${t}" "${secs}" "$@"
      return
    fi
  done
  echo "mutation.sh: no timeout(1): the ${secs}s cap on a package is not enforced" >&2
  "$@"
}

# mutation_build NAME DIR PACKAGE: builds PACKAGE of this module into DIR
# and prints the path. GOWORK=off builds against this module's own go.sum,
# so the workspace's other modules can't move the pinned tool's dependencies.
mutation_build() {
  (cd "${MUTATION_LIB_DIR}" && GOWORK=off go build -o "$2/$1" "$3") >&2 || {
    echo "mutation.sh: building $1 failed" >&2
    return 1
  }
  echo "$2/$1"
}

# mutation_run_package GREMLINS PKG OUT: one gremlins run, on PKG alone: its
# tests are the ones that must catch its mutants, and --coverpkg keeps the
# coverage run to it. Run in the project.
mutation_run_package() {
  local gremlins="$1" pkg="$2" out="$3"
  mutation_capped "${MUTATION_PACKAGE_TIMEOUT:-1500}" "${gremlins}" unleash \
    --workers "$(mutation_workers)" \
    --timeout-coefficient "${MUTATION_TIMEOUT_COEFFICIENT:-10}" \
    --coverpkg "${pkg}" -o "${out}" "${pkg}" > "${out%.json}.log" 2>&1
}

# mutation_go PROJECT ARGS...: mutation.sh for a Go module. A subshell, so
# its cd and trap stay its own. Each step checks its own status: set -e does
# nothing in a function called from an `if` or `||`.
mutation_go() (
  mutation_parse_args "$@" || exit
  report_base="$(mutation_abs "${MUTATION_REPORT_DIR:-${MUTATION_ROOT}/mutation-reports}")"
  project="$(cd "${MUTATION_PROJECT}" 2> /dev/null && pwd -P)" || {
    echo "mutation.sh: no project directory '${MUTATION_PROJECT}'" >&2
    exit 2
  }
  [[ -f "${project}/go.mod" ]] || {
    echo "mutation.sh: ${project} has no go.mod" >&2
    exit 2
  }
  cd "${project}" || exit 2
  patterns="$(mutation_patterns "${project}")" || exit
  mapfile -t patterns <<< "${patterns}"
  pkgs="$(mutation_expand "${patterns[@]}")" || exit
  mapfile -t pkgs <<< "${pkgs}"

  tmp="$(mktemp -d)" || exit 2
  trap 'rm -rf "${tmp}"' EXIT
  gremlins="${MUTATION_GREMLINS_BIN:-}"
  [[ -n "${gremlins}" ]] || gremlins="$(mutation_build gremlins "${tmp}" github.com/go-gremlins/gremlins/cmd/gremlins)" || exit 2
  bin="${MUTATION_BIN:-}"
  [[ -n "${bin}" ]] || bin="$(mutation_build mutation "${tmp}" ./cmd/mutation)" || exit 2

  results=()
  for pkg in "${pkgs[@]}"; do
    out="${tmp}/$(echo "${pkg}" | tr '/.' '--').json"
    echo "== gremlins ${pkg}" >&2
    mutation_run_package "${gremlins}" "${pkg}" "${out}" || {
      echo "mutation.sh: gremlins failed on ${pkg}; its output:" >&2
      tail -n 20 "${out%.json}.log" >&2 || true
      exit 2
    }
    [[ -s "${out}" ]] || {
      echo "mutation.sh: gremlins wrote no report for ${pkg}" >&2
      exit 2
    }
    results+=(--result "${pkg}=${out}")
  done

  args=(summarize --project "${project}" --mode "${MUTATION_MODE}"
    --prune-cmd "$(mutation_prune_cmd "${project}")")
  # Set explicitly, MUTATION_REPORT_DIR also gets the report from a check
  # (CI uploads it); otherwise only --report writes one.
  if [[ "${MUTATION_MODE}" == report || -n "${MUTATION_REPORT_DIR:-}" ]]; then
    args+=(--report-dir "${report_base}/$(mutation_slug "${project}")")
  fi
  "${bin}" "${args[@]}" "${results[@]}"
)

# mutation_rel PROJECT_ABS: the project's path from the repo root, or its
# base name for a checkout elsewhere (a worktree of another branch).
mutation_rel() {
  local root
  root="$(cd "${MUTATION_ROOT}" && pwd -P)"
  case "$1" in
    "${root}"/*) echo "${1#"${root}"/}" ;;
    *) basename "$1" ;;
  esac
}

# mutation_slug PROJECT_ABS: mutation_rel with / as -, as the report's name.
mutation_slug() {
  mutation_rel "$1" | tr '/' '-'
}

mutation_prune_cmd() {
  echo "dev-tools/mutation/mutation.sh $(mutation_rel "$1") --prune"
}
