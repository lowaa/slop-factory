# Shared helpers for this repo's bats suites. Load with:
#   setup() { bats_load_library repo_test; }

bats_load_library bats-support
bats_load_library bats-assert

# require_command <name>... -- fails the test up front, naming what to
# install, rather than letting a missing tool surface as a confusing
# assertion failure halfway through.
require_command() {
  local cmd
  for cmd in "$@"; do
    command -v "${cmd}" > /dev/null || fail "required command not found: ${cmd}"
  done
}

# unique_name <prefix> -- <prefix>-<8 random hex digits>, for anything a test
# makes on a docker daemon it shares with other runs (networks, containers,
# compose projects). Not $$: in a CI step container the PID is the same on
# every run, so concurrent runs collide, and a killed run's leftovers block
# every later one.
unique_name() {
  echo "$1-$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')"
}

# stub_recorder <path> <log> [exit-code] [only-when-call-matches]
#
# Writes an executable at <path> that appends its arguments to <log>, one
# line per call, and exits 0 -- or <exit-code>, when given: always, or only
# for calls whose arguments (joined with spaces) match the glob
# <only-when-call-matches>, e.g. "push *frontend*". Matching the whole call
# rather than the subcommand is what lets a test fail the *second* push, the
# case where a script that bumps tags as it goes would already have written
# one. The point is that assertions are about the exact commands a script
# built, and that nothing real (a registry push, a terraform apply) can run.
stub_recorder() {
  local path="$1" log="$2" code="${3:-0}" when="${4:-*}"
  mkdir -p "$(dirname "${path}")"
  : >> "${log}"
  cat > "${path}" << STUB
#!/usr/bin/env bash
echo "\$*" >> $(printf %q "${log}")
pattern=$(printf %q "${when}")
# shellcheck disable=SC2053 # the pattern is deliberately a glob
if [[ "\$*" == \${pattern} ]]; then
  exit ${code}
fi
exit 0
STUB
  chmod +x "${path}"
}

# stub_on_path <name> [exit-code] [only-when-call-matches]
#
# stub_recorder for a command found on PATH: the stub goes in a per-test bin
# directory that is put first on PATH, and records to
# $BATS_TEST_TMPDIR/<name>.calls.
stub_on_path() {
  local name="$1"
  shift
  local bin="${BATS_TEST_TMPDIR}/bin"
  stub_recorder "${bin}/${name}" "${BATS_TEST_TMPDIR}/${name}.calls" "$@"
  [[ ":${PATH}:" == *":${bin}:"* ]] || PATH="${bin}:${PATH}"
}

# assert_lines_in_order <file> <glob>...
#
# Each glob must match a line of <file>, in the order given -- so "build then
# push" is checked as a sequence, not as two lines that are merely both
# present somewhere.
assert_lines_in_order() {
  local file="$1"
  shift
  local -a lines=()
  mapfile -t lines < "${file}"
  local next=0 pattern i matched
  for pattern in "$@"; do
    matched=""
    for ((i = next; i < ${#lines[@]}; i++)); do
      # shellcheck disable=SC2053 # the pattern is deliberately a glob
      if [[ "${lines[i]}" == ${pattern} ]]; then
        matched=1
        next=$((i + 1))
        break
      fi
    done
    [[ -n "${matched}" ]] || fail "$(printf 'no line matching %s (in order) in %s:\n%s' \
      "${pattern}" "${file}" "$(cat "${file}")")"
  done
}
