#!/usr/bin/env bash
#
# Every linter a change needs, from one command: the runners of the projects
# it touches, and the repo-wide ones. Run `lint.sh --changed` before pushing;
# see README.md.
#
#   dev-tools/lint/lint.sh [--changed | PATH...] [options]
#
#   --changed      the projects with files changed against the merge-base
#                  with main: commits, staged, unstaged, untracked and
#                  deleted files alike
#   PATH...        the projects those files or directories are in, or under
#   (neither)      every project
#
#   --no-repo      skip the repo-wide tools (knip, jscpd, shellcheck, shfmt),
#                  which otherwise always run; shell scripts among the paths
#                  still get shellcheck and shfmt
#   --repo-only    only the repo-wide tools: what CI's lint job runs, every
#                  project's own lint being in its test.sh
#   --prune, --rebaseline, --only=TOOL[,TOOL]
#                  passed to each runner (README.md); --only also skips the
#                  runners with none of those tools
#   -j N           runners at once (default 4)
#   -v             print every runner's output, not only a failed one's
#   --dry-run      print the commands, run nothing
#
# A project is a line of projects.txt; a path maps to a runner by what the
# file is: a Go file to its module's go.sh, TypeScript or JSON to its area's
# oxlint.sh, Python to its project's ruff.sh, a shell script to shell.sh.
# oxlint runs here on the host, which finds what it finds in CI's test image
# only with the root's full `npm ci` (npm_complete.mjs checks).
set -euo pipefail
export LC_ALL=C
# shellcheck source=SCRIPTDIR/../require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../require_bash.sh"
shopt -s inherit_errexit

LINT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# LINT_REPO and LINT_RUNNERS: the tests run a scratch repo with stub runners.
REPO="$(cd "${LINT_REPO:-${LINT_DIR}/../..}" && pwd)"
RUNNERS="${LINT_RUNNERS:-${LINT_DIR}}"
REGISTRY="${REPO}/dev-tools/lint/projects.txt"
US=$'\x1f'

declare -A TOOLS=(
  [go]="staticcheck errcheck gosec gocognit revive deadcode"
  [oxlint]="oxlint"
  [ruff]="ruff"
  [repo]="knip jscpd"
  [shell]="shellcheck shfmt"
)

usage() {
  sed -n '7,26s/^# \{0,1\}//p' "${BASH_SOURCE[0]}" >&2
  exit 2
}

changed=0 repo_wide=1 repo_only=0 mode_flag="" only="" jobs=4 verbose=0 dry_run=0
paths=()
while (($#)); do
  case "$1" in
    --changed) changed=1 ;;
    --no-repo) repo_wide=0 ;;
    --repo-only) repo_only=1 ;;
    --prune | --rebaseline) mode_flag="$1" ;;
    --only=?*) only="${1#--only=}" ;;
    -j)
      [[ "${2:-}" =~ ^[1-9][0-9]*$ ]] || usage
      jobs="$2"
      shift
      ;;
    -v) verbose=1 ;;
    --dry-run) dry_run=1 ;;
    -h | --help) usage ;;
    -*) usage ;;
    *) paths+=("$1") ;;
  esac
  shift
done
((! changed || ${#paths[@]} == 0)) || {
  echo "lint.sh: --changed or paths, not both" >&2
  exit 2
}
((! repo_only || (repo_wide && ! changed && ${#paths[@]} == 0))) || {
  echo "lint.sh: --repo-only takes no paths, --changed or --no-repo" >&2
  exit 2
}
if [[ -n "${only}" ]]; then
  for t in ${only//,/ }; do
    [[ " ${TOOLS[*]} " == *" ${t} "* ]] || {
      echo "lint.sh: unknown tool '${t}' (have: ${TOOLS[*]})" >&2
      exit 2
    }
  done
fi

PROJECTS=() # "<runner> <dir>", from the registry
while read -r runner dir _; do
  [[ -z "${runner}" || "${runner}" == \#* ]] && continue
  [[ -n "${TOOLS[${runner}]:-}" && -n "${dir}" ]] || {
    echo "lint.sh: ${REGISTRY}: bad line '${runner} ${dir}'" >&2
    exit 2
  }
  PROJECTS+=("${runner} ${dir}")
done < "${REGISTRY}"

# relative_to_repo PATH: PATH relative to the repo root, whether or not it
# exists, with a leading .. if it is outside. Stands in for GNU's
# `realpath -m --relative-to`, which BSD realpath lacks.
relative_to_repo() {
  local p="$1" part root parent out=() parts
  [[ "${p}" == /* ]] || p="$(pwd -P)/${p}"
  # An existing path resolves fully, links in its last part and before a ..
  # included, as GNU's would; plain realpath does that on both.
  if [[ -e "${p}" ]]; then
    p="$(realpath "${p}")"
  fi
  IFS=/ read -r -a parts <<< "${p}"
  for part in "${parts[@]}"; do
    case "${part}" in
      '' | .) ;;
      ..) ((${#out[@]} == 0)) || unset "out[${#out[@]}-1]" ;;
      *) out+=("${part}") ;;
    esac
  done
  p="$(IFS=/ && echo "/${out[*]}")"
  # The directory part is resolved as a real path would be (macOS's /tmp is a
  # link); the last part may not exist.
  if parent="$(cd -P "${p%/*}/" 2> /dev/null && pwd -P)"; then
    p="${parent%/}/${p##*/}"
  fi
  root="$(cd -P "${REPO}" && pwd -P)"
  if [[ "${p}" == "${root}" ]]; then
    echo .
  elif [[ "${p}" == "${root}/"* ]]; then
    echo "${p#"${root}"/}"
  else
    echo "../${p#/}"
  fi
}

# The paths are the caller's, relative to where it ran; from here on they
# are the repo's.
for i in "${!paths[@]}"; do
  rel="$(relative_to_repo "${paths[i]}")"
  [[ "${rel}" != .. && "${rel}" != ../* ]] || {
    echo "lint.sh: ${paths[i]} is outside ${REPO}" >&2
    exit 2
  }
  [[ -e "${paths[i]}" ]] || echo "lint.sh: ${paths[i]} does not exist; linting what it would be part of" >&2
  paths[i]="${rel}"
done
cd "${REPO}"

# merge_base: the merge-base with main, or with origin/main where that is
# nearer (a local main not pulled for a while); LINT_BASE overrides both.
merge_base() {
  [[ -z "${LINT_BASE:-}" ]] || {
    echo "${LINT_BASE}"
    return
  }
  local ref mb best=""
  for ref in main origin/main; do
    git rev-parse -q --verify "${ref}^{commit}" > /dev/null || continue
    mb="$(git merge-base HEAD "${ref}")" || continue
    if [[ -z "${best}" ]] || git merge-base --is-ancestor "${best}" "${mb}"; then best="${mb}"; fi
  done
  [[ -n "${best}" ]] || {
    echo "lint.sh: --changed needs a main or origin/main to diff against (or LINT_BASE)" >&2
    exit 2
  }
  echo "${best}"
}

changed_files() {
  local base
  base="$(merge_base)"
  # The working tree against the base takes in commits, staged and unstaged
  # edits, and deletions; untracked files are the only ones it misses.
  {
    git diff --name-only --no-renames "${base}" --
    git ls-files --others --exclude-standard
  } | sort -u
}

is_shell() { # is_shell REL_PATH
  case "${1##*/}" in
    *.sh | *.bash | *.bats) return 0 ;;
    *.*) return 1 ;;
  esac
  [[ -f "$1" ]] && head -n1 "$1" 2> /dev/null | grep -qE '^#!.*[/ ](ba|da|k)?sh([[:space:]]|$)'
}

# owner RUNNER REL_PATH: the project of that runner the path is in, the
# innermost where they nest (slop-review/report in slop-review).
owner() {
  local p best=""
  for p in "${PROJECTS[@]}"; do
    [[ "${p%% *}" == "$1" ]] || continue
    p="${p#* }"
    [[ "$2" == "${p}/"* && ${#p} -gt ${#best} ]] && best="${p}"
  done
  [[ -z "${best}" ]] || echo "$1 ${best}"
}

all_of() { # all_of RUNNER: every project of that runner
  local p
  for p in "${PROJECTS[@]}"; do [[ "${p%% *}" != "$1" ]] || echo "${p}"; done
}

# route REL_PATH: the parts ("<runner> <dir>", "repo" or "shell") a path
# needs linted.
route() {
  local f="$1" p dir
  [[ "${f}" != "." ]] || {
    printf '%s\n' "${PROJECTS[@]}" shell
    return
  }
  if [[ -d "${f}" ]]; then
    # A directory: every project in or under it, and the shell scripts.
    for p in "${PROJECTS[@]}"; do
      dir="${p#* }"
      [[ "${dir}/" == "${f%/}/"* || "${f%/}/" == "${dir}/"* ]] && echo "${p}"
    done
    echo shell
    return
  fi
  # The linters' own code and config: what they change, they change for
  # every project of the runner.
  case "${f}" in
    dev-tools/lint/go.sh | dev-tools/lint/revive.toml | dev-tools/lint/go.mod | dev-tools/lint/go.sum) all_of go ;;
    dev-tools/lint/oxlint.sh | dev-tools/lint/oxlintrc.base.json | dev-tools/lint/npm_complete.mjs) all_of oxlint ;;
    dev-tools/lint/findings.mjs) all_of oxlint && echo repo ;;
    dev-tools/lint/ruff.sh | dev-tools/lint/ruff.toml | dev-tools/lint/ruff/*) all_of ruff ;;
    dev-tools/lint/repo.sh | dev-tools/lint/baseline/knip.txt | dev-tools/lint/baseline/jscpd.txt) echo repo ;;
    dev-tools/lint/shell.sh | dev-tools/lint/baseline/shellcheck.txt | dev-tools/lint/baseline/shfmt.txt) echo shell ;;
    dev-tools/lint/ratchet.sh | dev-tools/lint/lib.sh | dev-tools/lint/projects.txt)
      printf '%s\n' "${PROJECTS[@]}" repo shell
      ;;
    knip.jsonc | .jscpd.json) echo repo ;;
  esac
  ! is_shell "${f}" || echo shell
  case "${f}" in
    */lint_baseline/ruff.txt | *.py | */pyproject.toml) owner ruff "${f}" ;;
    */lint_baseline/oxlint.txt | *.ts | *.tsx | *.js | *.jsx | *.mjs | *.cjs | *.mts | *.cts | *.json) owner oxlint "${f}" ;;
    */lint_baseline/*.txt | */deadcode/* | *.go | */go.mod | */go.sum) owner go "${f}" ;;
  esac
}

parts=()
if ((repo_only)); then
  parts=(repo shell)
else
  targets=()
  if ((changed)); then
    mapfile -t targets < <(changed_files)
  elif ((${#paths[@]})); then
    targets=("${paths[@]}")
  else
    targets=(.)
  fi
  mapfile -t parts < <(
    for f in ${targets[@]+"${targets[@]}"}; do route "${f}"; done
    ((! repo_wide)) || printf '%s\n' repo shell
  )
fi

# command_for PART: the runner's command line, with the pass-through flags,
# its words joined by US (an argument may hold a space), or nothing when
# --only leaves it no tool.
command_for() {
  local runner="${1%% *}" dir="" t picked=()
  [[ "$1" != *" "* ]] || dir="${1#* }"
  if [[ -n "${only}" ]]; then
    for t in ${only//,/ }; do
      [[ " ${TOOLS[${runner}]} " != *" ${t} "* ]] || picked+=("${t}")
    done
    ((${#picked[@]})) || return 0
  fi
  local cmd=("${RUNNERS}/${runner}.sh")
  [[ -z "${mode_flag}" ]] || cmd+=("${mode_flag}")
  # oxlint.sh and ruff.sh run their one tool, so take no --only.
  if ((${#picked[@]})) && [[ "${runner}" == go || "${runner}" == repo || "${runner}" == shell ]]; then
    cmd+=("--only=$(
      IFS=,
      echo "${picked[*]}"
    )")
  fi
  [[ -z "${dir}" ]] || cmd+=("${dir}")
  (
    IFS="${US}"
    echo "${cmd[*]}"
  )
}

# The slow ones first, so they are not the last to start: knip, and the
# server's Go (most of a minute each).
order() {
  local p
  for p in "$@"; do
    case "${p}" in
      repo | "go slop-haus/server") echo "0 ${p}" ;;
      *) echo "1 ${p}" ;;
    esac
  done | sort -u | cut -d' ' -f2-
}

names=() cmds=()
while IFS= read -r part; do
  [[ -n "${part}" ]] || continue
  line="$(command_for "${part}")"
  [[ -n "${line}" ]] || continue
  names+=("${part}")
  cmds+=("${line}")
done < <(order ${parts[@]+"${parts[@]}"})

shown() { # shown INDEX: that command, for a reader
  local line="${cmds[$1]//"${US}"/ }"
  echo "${line/#"${RUNNERS}/"/dev-tools/lint/}"
}

((${#cmds[@]})) || {
  echo "lint.sh: nothing to lint"
  exit 0
}
if ((dry_run)); then
  for i in "${!cmds[@]}"; do shown "${i}"; done
  exit 0
fi

# oxlint here and knip in repo.sh need every workspace's packages: with
# less (an npm ci of the root's tools only) they report less, not an
# error, so a partial install must not pass for a clean run.
npm_problem=""
if [[ " ${names[*]} " == *" oxlint "* || " ${names[*]} " == *" repo "* ]]; then
  if [[ -z "${only}" || ",${only}," == *,oxlint,* || ",${only}," == *,knip,* ]]; then
    npm_problem="$(node "${LINT_DIR}/npm_complete.mjs" "${REPO}" 2>&1)" || true
  fi
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

run_part() { # run_part INDEX: one runner, its output and status in WORK
  local i="$1" start rc=0 argv=()
  start="${EPOCHREALTIME}"
  {
    if [[ -n "${npm_problem}" && ("${names[i]}" == oxlint\ * || "${names[i]}" == repo) ]]; then
      printf '%s\n' "${npm_problem}"
      rc=2
    else
      IFS="${US}" read -r -a argv <<< "${cmds[i]}"
      "${argv[@]}" || rc=$?
    fi
  } > "${WORK}/${i}.log" 2>&1 < /dev/null
  echo "${rc} $(awk -v a="${start}" -v b="${EPOCHREALTIME}" 'BEGIN { printf "%.1f", b - a }')" > "${WORK}/${i}.status"
}

started="${EPOCHREALTIME}"
for i in "${!cmds[@]}"; do
  while (($(jobs -rp | wc -l) >= jobs)); do wait -n || true; done
  run_part "${i}" &
done
wait

failed=()
for i in "${!cmds[@]}"; do
  read -r rc secs < "${WORK}/${i}.status"
  if ((rc != 0)) || ((verbose)) || [[ -n "${mode_flag}" ]]; then
    echo "---- ${names[i]}: $(shown "${i}")" >&2
    cat "${WORK}/${i}.log" >&2
  fi
  ((rc == 0)) || failed+=("${names[i]}")
  printf '%-4s %-30s %6ss\n' "$( ((rc == 0)) && echo ok || echo FAIL)" "${names[i]}" "${secs}"
done
awk -v a="${started}" -v b="${EPOCHREALTIME}" 'BEGIN { printf "lint.sh: %.1fs\n", b - a }'
if ((${#failed[@]})); then
  echo "lint.sh: failed: $(
    IFS=,
    echo "${failed[*]}"
  )" | sed 's/,/, /g' >&2
  exit 1
fi
