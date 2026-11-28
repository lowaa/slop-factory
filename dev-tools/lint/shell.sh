#!/usr/bin/env bash
#
# Lints every shell script in the repo, each tool through ratchet.sh against
# dev-tools/lint/baseline/<tool>.txt: shellcheck with its every check,
# following `source`d files (-x), and shfmt check-only (-d: the diff it would
# make, never a rewrite).
#
#   dev-tools/lint/shell.sh [--prune|--rebaseline] [--only=TOOL[,TOOL]]
#
# A shell script is a tracked or untracked *.sh, *.bash or *.bats, or a file
# whose first line is a sh/bash shebang, less those marked
# linguist-generated (the golden files under testdata/golden). shfmt skips
# shellspec specs (*_spec.sh), whose blocks it can't see. Both tools run
# from pinned images, as one pin covers every platform; see README.md.
set -euo pipefail
export LC_ALL=C
# shellcheck source=SCRIPTDIR/../require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../require_bash.sh"
shopt -s inherit_errexit

LINT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# LINT_REPO: the tests lint a scratch repo.
REPO="$(cd "${LINT_REPO:-${LINT_DIR}/../..}" && pwd)"
# shellcheck source=SCRIPTDIR/lib.sh
. "${LINT_DIR}/lib.sh"
ALL_TOOLS=(shellcheck shfmt)
SHELLCHECK_IMAGE="koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d"
SHFMT_IMAGE="mvdan/shfmt:v3.14.1@sha256:8c06884a35683d8763fba6c0d9484d11a61a96da65f8d497d6f625825a6043f2"
# The repo's style: two-space indents, case items indented, a space after a
# redirect (`> file`). Run with -w in place of -d to apply it.
SHFMT_FLAGS=(-i 2 -ci -sr)
SHELLCHECK_JOBS="${SHELLCHECK_JOBS:-8}"

usage() {
  echo "usage: shell.sh [--prune|--rebaseline] [--only=TOOL[,TOOL]]   tools: ${ALL_TOOLS[*]}" >&2
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
    echo "shell.sh: unknown tool '${t}'" >&2
    usage
  }
done
command -v docker > /dev/null || {
  echo "shell.sh: needs docker, to run ${ALL_TOOLS[*]} from their pinned images" >&2
  exit 2
}

cd "${REPO}"
FILES=()
while IFS= read -r f; do
  case "${f##*/}" in
    *.sh | *.bash | *.bats) FILES+=("${f}") ;;
    # Only a name without an extension is worth opening for its shebang.
    *.*) ;;
    *) head -n1 "${f}" 2> /dev/null | grep -qE '^#!.*[/ ](ba|da|k)?sh([[:space:]]|$)' && FILES+=("${f}") ;;
  esac
done < <(lint_files .)
# shellspec's Describe/It/End blocks are plain commands to shfmt, which
# would flatten every spec's nesting to the left margin. shellcheck still
# reads them: the code inside the blocks is ordinary shell.
FMT_FILES=()
for f in "${FILES[@]}"; do
  [[ "${f}" == *_spec.sh ]] || FMT_FILES+=("${f}")
done

# in_docker IMAGE ARGS...: the tool over the repo, mounted read-only where
# its paths stay the ones the findings name.
in_docker() {
  local image="$1"
  shift
  docker run --rm --network none -v "${REPO}:/mnt:ro" -w /mnt "${image}" "$@"
}

# Each run_<tool> prints the findings, one "<file> <rule>" line each, and
# fails if the tool itself did not run cleanly.

run_shellcheck() {
  local dir size i rc status=0
  ((${#FILES[@]})) || return 0
  # In slices, a container each, at once: shellcheck is single-threaded and
  # takes most of a second a script, which in one process is the slowest
  # part of lint.sh.
  dir="$(mktemp -d)"
  size=$(((${#FILES[@]} + SHELLCHECK_JOBS - 1) / SHELLCHECK_JOBS))
  for ((i = 0; i * size < ${#FILES[@]}; i++)); do
    (
      rc=0
      in_docker "${SHELLCHECK_IMAGE}" -x -f gcc "${FILES[@]:i*size:size}" > "${dir}/${i}.out" 2>&1 || rc=$?
      echo "${rc}" > "${dir}/${i}.rc"
    ) &
  done
  wait
  for ((i = 0; i * size < ${#FILES[@]}; i++)); do
    # 1 means findings; above it, a file it could not read or a bad option.
    rc="$(cat "${dir}/${i}.rc")"
    ((rc <= 1)) || {
      cat "${dir}/${i}.out" >&2
      status=1
    }
  done
  ((status == 0)) && cat "${dir}"/*.out |
    normalise 's/^([^:]+):[0-9]+:[0-9]+: [a-z]+: .* \[(SC[0-9]+)\]$/\1 \2/' || status=1
  rm -rf "${dir}"
  return "${status}"
}

run_shfmt() {
  local out err rc=0
  ((${#FMT_FILES[@]})) || return 0
  err="$(mktemp)"
  out="$(in_docker "${SHFMT_IMAGE}" -d "${SHFMT_FLAGS[@]}" "${FMT_FILES[@]}" 2> "${err}")" || rc=$?
  # It exits 1 both for a diff and for a file it cannot parse; only the
  # latter writes to stderr.
  if ((rc > 1)) || [[ -s "${err}" ]]; then
    cat "${err}" >&2
    rm -f "${err}"
    return 1
  fi
  rm -f "${err}"
  # The diff goes to stderr, for the reader; a file is one finding however
  # many lines it would change, so one fix (shfmt -w) clears it.
  [[ -z "${out}" ]] || printf '%s\n' "${out}" >&2
  # A file's header is a "--- " line and then a "+++ " one; an added line
  # that happens to start "++ " is not.
  awk 'prev ~ /^--- / && /^\+\+\+ / { print substr($0, 5) " shfmt" } { prev = $0 }' <<< "${out}"
}

# image_of TOOL: the pinned image the tool runs in.
image_of() {
  case "$1" in
    shellcheck) echo "${SHELLCHECK_IMAGE}" ;;
    shfmt) echo "${SHFMT_IMAGE}" ;;
  esac
}

# Pulled up front, quietly: `docker run` pulling on first use writes its
# progress to the output the findings are read from (shellcheck's) and to the
# stderr that means "could not parse" (shfmt's), so a fresh machine reported
# both tools as not having run.
pull_image() {
  docker image inspect "$1" > /dev/null 2>&1 || docker pull -q "$1" > /dev/null
}

failed=()
for tool in "${tools[@]}"; do
  if ! pull_image "$(image_of "${tool}")"; then
    failed+=("${tool} (image $(image_of "${tool}") could not be pulled)")
    continue
  fi
  echo "== ${tool} (${#FILES[@]} scripts)" >&2
  if ! findings="$("run_${tool}")"; then
    failed+=("${tool} (did not run)")
    continue
  fi
  printf '%s\n' "${findings}" |
    "${LINT_DIR}/ratchet.sh" ${mode_flag:+"${mode_flag}"} \
      --regen "dev-tools/lint/shell.sh --prune --only=${tool}" \
      "dev-tools/lint/baseline/${tool}.txt" ||
    failed+=("${tool}")
done

if ((${#failed[@]})); then
  [[ " ${failed[*]} " != *" shfmt "* ]] ||
    echo "shell.sh: to format a script: docker run --rm -v \"\$PWD:/mnt\" -w /mnt ${SHFMT_IMAGE} -w ${SHFMT_FLAGS[*]} <file>" >&2
  echo "shell.sh: failed: ${failed[*]}" >&2
  exit 1
fi
