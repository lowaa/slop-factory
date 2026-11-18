# Tests for ratchet.sh: findings on stdin against a baseline file.
#
#   bats/run.sh dev-tools/lint/ratchet.bats   # from the repo root

RATCHET="${BATS_TEST_DIRNAME}/ratchet.sh"

setup() {
  bats_load_library repo_test
  BASE="${BATS_TEST_TMPDIR}/lint_baseline/tool.txt"
  ALLOW="${BATS_TEST_TMPDIR}/allowlist.txt"
}

ratchet() { # ratchet <findings> [args...]
  local findings="$1"
  shift
  printf '%s' "${findings}" | "${RATCHET}" "$@"
}

baseline() {
  mkdir -p "$(dirname "${BASE}")"
  printf '%s' "$1" > "${BASE}"
}

@test "no findings and no baseline passes, and writes nothing" {
  run ratchet "" "${BASE}"
  assert_success
  run ratchet "" --rebaseline "${BASE}"
  assert_success
  [ ! -e "${BASE}" ]
}

@test "an unchanged tree passes" {
  baseline $'a.go SA1000\nb.go G101\n'
  run ratchet $'b.go G101\na.go SA1000\n' "${BASE}"
  assert_success
  assert_output ""
}

@test "a new finding fails and is named" {
  baseline $'a.go SA1000\n'
  run ratchet $'a.go SA1000\nc.go ST1005\n' --regen "regen-cmd" "${BASE}"
  assert_failure
  assert_output --partial "New findings, not in ${BASE}:"
  assert_output --partial "  c.go ST1005"
  refute_output --partial "  a.go SA1000"
}

@test "a fixed finding left in the baseline fails, naming the regeneration command" {
  baseline $'a.go SA1000\nb.go G101\n'
  run ratchet $'a.go SA1000\n' --regen "dev-tools/lint/go.sh --prune --only=gosec m" "${BASE}"
  assert_failure
  assert_output --partial "but no longer found:"
  assert_output --partial "  b.go G101"
  assert_output --partial "or run: dev-tools/lint/go.sh --prune --only=gosec m"
}

@test "a tool that silently finds nothing fails against a non-empty baseline" {
  baseline $'a.go SA1000\n'
  run ratchet "" "${BASE}"
  assert_failure
  assert_output --partial "no longer found"
}

@test "the baseline is a multiset: one more of a finding it lists fails, one fewer is stale" {
  baseline $'a.go G101\na.go G101\n'
  run ratchet $'a.go G101\na.go G101\na.go G101\n' "${BASE}"
  assert_failure
  assert_output --partial "  a.go G101"
  assert_output --partial "New findings"
  run ratchet $'a.go G101\n' "${BASE}"
  assert_failure
  assert_output --partial "no longer found"
}

@test "comments and blank lines in the baseline are not entries" {
  baseline $'# a header\n\na.go SA1000\n'
  run ratchet $'a.go SA1000\n' "${BASE}"
  assert_success
}

@test "trailing whitespace on a finding does not make it new" {
  baseline $'a.go SA1000\n'
  run ratchet $'a.go SA1000  \n' "${BASE}"
  assert_success
}

@test "an allow-listed finding passes, and a stale allow-list line fails" {
  printf '# header\nx.go Wire #123\n' > "${ALLOW}"
  run ratchet $'x.go Wire\n' --allowlist "${ALLOW}" "${BASE}"
  assert_success
  run ratchet "" --allowlist "${ALLOW}" "${BASE}"
  assert_failure
  assert_output --partial "  x.go Wire"
  assert_output --partial "or ${ALLOW}"
}

@test "a finding in both the baseline and the allow-list is one too many" {
  baseline $'x.go Wire\n'
  printf 'x.go Wire #123\n' > "${ALLOW}"
  run ratchet $'x.go Wire\n' --allowlist "${ALLOW}" "${BASE}"
  assert_failure
  assert_output --partial "no longer found"
}

@test "a baseline line with trailing whitespace still matches" {
  baseline $'a.go SA1000   \n'
  run ratchet $'a.go SA1000\n' "${BASE}"
  assert_success
}

@test "an allow-list line without a ticket fails" {
  printf 'x.go Wire\n' > "${ALLOW}"
  run ratchet $'x.go Wire\n' --allowlist "${ALLOW}" "${BASE}"
  assert_failure
  assert_output --partial "must read '<finding> #<ticket>'"
}

@test "--prune drops the fixed entries, keeps the header, and never adds" {
  baseline $'# header line\na.go SA1000\nb.go G101\nb.go G101\n'
  run ratchet $'b.go G101\n' --prune "${BASE}"
  assert_success
  assert_output --partial "removed 2 fixed entries"
  assert_equal "$(cat "${BASE}")" $'# header line\nb.go G101'
}

@test "--prune still fails on a new finding, and does not baseline it" {
  baseline $'a.go SA1000\nb.go G101\n'
  run ratchet $'a.go SA1000\nc.go ST1005\n' --prune "${BASE}"
  assert_failure
  assert_output --partial "  c.go ST1005"
  assert_equal "$(cat "${BASE}")" "a.go SA1000"
}

@test "--rebaseline writes every finding, sorted, minus the allow-listed ones" {
  printf 'x.go Wire #7\n' > "${ALLOW}"
  run ratchet $'z.go G101\na.go SA1000\nx.go Wire\nz.go G101\n' --rebaseline --allowlist "${ALLOW}" "${BASE}"
  assert_success
  assert_equal "$(cat "${BASE}")" $'a.go SA1000\nz.go G101\nz.go G101'
  run ratchet $'z.go G101\na.go SA1000\nx.go Wire\nz.go G101\n' --allowlist "${ALLOW}" "${BASE}"
  assert_success
}

@test "--rebaseline to no findings empties an existing baseline but keeps its header" {
  baseline $'# keep me\na.go SA1000\n'
  run ratchet "" --rebaseline "${BASE}"
  assert_success
  assert_equal "$(cat "${BASE}")" "# keep me"
}

@test "an unknown option or a missing baseline argument is a usage error" {
  run ratchet "" --bogus "${BASE}"
  assert_failure 2
  run ratchet ""
  assert_failure 2
}
