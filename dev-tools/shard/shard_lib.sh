# shellcheck shell=bash
# Runs a test suite split N ways, each shard a whole run of its own, all at
# once: for a suite that can't run in parallel against one stack (its
# fixtures truncate the one database between tests), a stack per shard is
# what lets it run in parallel at all. Sourced, not run:
#
#   source dev-tools/shard/shard_lib.sh
#   shard_check_count <n>          fails unless <n> is a positive integer
#   shard_run_all <n> <script>     <script> N times concurrently, each with
#                                  SHARDS=<n> and SHARD_INDEX=1..<n> set;
#                                  returns the lowest-numbered failing
#                                  shard's status
#   shard_run_each <name>... -- <command> [<arg>...]
#                                  <command> once per name, concurrently, each
#                                  with SHARD_NAME=<name> set (and SHARDS and
#                                  SHARD_INDEX, as above); returns the status
#                                  of the first-named that failed. For a split
#                                  that isn't numbered (a run per harness)
#   shard_share_images <from> <to> the images compose project <from> built,
#                                  tagged as project <to>'s
#   shard_playwright_command <index> <n> <project> [<once-project>...]
#                                  sets SHARD_COMMAND to what shard <index>
#                                  of <n> runs: <project>'s tests split <n>
#                                  ways, and in shard 1 the once-projects'
#                                  too, whole
#
# Every shard's stack is built from the same images, so the parent builds
# them once and each shard tags them as its own project's (compose names a
# built image <project>-<service> and only builds what is missing): N
# concurrent builds of one stack, each loading its contexts and exporting
# its images, took a CI agent ~6 minutes before any test ran, where one
# build takes under one.
#
# Each shard's output goes to a log of its own and is printed whole, shard
# by shard, rather than N streams interleaved.

# shellcheck source=SCRIPTDIR/../require_bash.sh
. "$(dirname "${BASH_SOURCE[0]}")/../require_bash.sh"

# file_mtime <file> -- its modification time in epoch seconds: GNU stat, then
# BSD stat. (`date -r <file>` is GNU only; BSD's -r takes epoch seconds.)
file_mtime() {
  stat -c %Y "$1" 2> /dev/null || stat -f %m "$1"
}

# shard_check_count <n>
shard_check_count() {
  if [[ ! "$1" =~ ^[1-9][0-9]*$ ]]; then
    echo "SHARDS must be a positive integer, got '$1'" >&2
    return 2
  fi
}

# shard_run_all <n> <script> -- shard_run_each with the shards' numbers for
# names.
shard_run_all() {
  local shards="$1" script="$2" names=() i
  shard_check_count "${shards}" || return
  for ((i = 1; i <= shards; i++)); do
    names+=("shard ${i}/${shards}")
  done
  shard_run_each "${names[@]}" -- "${script}"
}

# shard_run_each <name>... -- <command> [<arg>...] -- sets INT and TERM traps
# of its own, so the caller is expected to exit with its status rather than
# carry on.
shard_run_each() {
  local names=()
  while (($#)) && [[ "$1" != -- ]]; do
    names+=("$1")
    shift
  done
  if ((! ${#names[@]})) || [[ "${1-}" != -- ]] || (($# < 2)); then
    echo "usage: shard_run_each <name>... -- <command> [<arg>...]" >&2
    return 2
  fi
  shift
  local shards="${#names[@]}" logs i status=0 shard_status start summary=()
  logs="$(mktemp -d)"
  start="$(date +%s)"
  local pids=()
  # A Ctrl-C reaches the shards on its own (they share this process group),
  # and each takes its stack down from its own trap; a TERM is passed on.
  # Either way, this keeps waiting until every shard has tidied up. Set
  # before the first shard starts, so a TERM can't arrive between and leave
  # it running.
  trap 'kill -TERM "${pids[@]}" 2> /dev/null || true' TERM
  trap ':' INT
  for ((i = 1; i <= shards; i++)); do
    # Logs by number: a name is free text, "shard 1/2" included.
    SHARDS="${shards}" SHARD_INDEX="${i}" SHARD_NAME="${names[i - 1]}" "$@" > "${logs}/${i}.log" 2>&1 &
    pids[i]=$!
  done
  # In shard order, not as they finish: `wait -n` would print each sooner,
  # but bash 5.1's loses children that exit early when this runs in a
  # subshell, and returns 127 for them instead of their status.
  for ((i = 1; i <= shards; i++)); do
    # A trapped signal ends a wait early, with the shard still running.
    while kill -0 "${pids[i]}" 2> /dev/null; do
      wait "${pids[i]}" 2> /dev/null || true
    done
    shard_status=0
    wait "${pids[i]}" || shard_status=$?
    # When its log was last written, not now: printed in shard order, a shard
    # that finished early is printed late.
    echo "::group::${names[i - 1]} (exit ${shard_status}, done after $(($(file_mtime "${logs}/${i}.log") - start))s)"
    cat "${logs}/${i}.log"
    echo "::endgroup::"
    summary+=("${names[i - 1]}: ${shard_status}")
    if [[ "${shard_status}" -ne 0 && "${status}" -eq 0 ]]; then
      status="${shard_status}"
    fi
  done
  rm -rf "${logs}"
  # Last, after every shard's log: the tail of a CI step's log is all that
  # is shown of a failure, and the shards' headers are far above it.
  echo "shards: returning ${status} ($(
    IFS=,
    echo "${summary[*]}"
  ))" >&2
  return "${status}"
}

# shard_share_images <from> <to> -- each image labelled as built by compose
# project <from> and named <from>-<service>, tagged <to>-<service>. A tag is
# only a name: <to>'s teardown removing its images leaves <from>'s.
shard_share_images() {
  local from="$1" to="$2" refs ref
  # Listed first, not read from a process substitution, so a docker that
  # fails here fails the run rather than leaving every shard to build its
  # own images unnoticed.
  refs="$(docker image ls --filter "label=com.docker.compose.project=${from}" --format '{{.Repository}}:{{.Tag}}')" || return
  for ref in ${refs}; do
    [[ "${ref}" == "${from}-"* ]] || continue
    docker image tag "${ref}" "${to}-${ref#"${from}"-}" || return
  done
}

# shard_playwright_command <index> <n> <project> [<once-project>...] -- sets
# the array SHARD_COMMAND. Playwright's --shard splits by test count across
# every project it runs, not by time: a project of near-instant specs with no
# stack behind them counts as much as the stack-backed ones, and left the
# last shard with a fraction of the real work (an e2e_support project of 45 of
# 182 tests left shard 3/3 done in 37s against shard 1's 2.6m). So only
# <project> is split, and the once-projects run whole in shard 1, before its
# share: each runs whatever the other's result, and the command fails if
# either does.
shard_playwright_command() {
  local index="$1" shards="$2" project="$3" once_project
  shift 3
  SHARD_COMMAND=(npx playwright test --project="${project}" --shard="${index}/${shards}")
  if [[ "${index}" != 1 || $# -eq 0 ]]; then
    return
  fi
  local once=(npx playwright test)
  for once_project in "$@"; do
    once+=(--project="${once_project}")
  done
  # One command, since the shard runs one container command: a script for
  # the container's bash, its arguments quoted for it.
  SHARD_COMMAND=(bash -c "status=0; $(printf '%q ' "${once[@]}")|| status=\$?; $(printf '%q ' "${SHARD_COMMAND[@]}")|| exit; exit \"\${status}\"")
}
