# Tests for review.sh's handling of --intent, with `go` stubbed so nothing
# runs: the script cd's into its own directory before calling go, so a
# relative path only keeps meaning if review.sh resolves it first.
#
#   bats/run.sh slop-review/review.bats   # from the repo root

setup() {
  bats_load_library repo_test
  mkdir -p "${BATS_TEST_TMPDIR}/bin" "${BATS_TEST_TMPDIR}/work/sub"
  cat > "${BATS_TEST_TMPDIR}/bin/go" << 'STUB'
#!/usr/bin/env bash
printf 'cwd=%s\n' "$(pwd -P)" > "${GO_CALLS}"
printf 'env=%s\n' "${SLOP_REVIEW_INTENT:-}" >> "${GO_CALLS}"
printf 'arg=%s\n' "$@" >> "${GO_CALLS}"
STUB
  chmod +x "${BATS_TEST_TMPDIR}/bin/go"
  export GO_CALLS="${BATS_TEST_TMPDIR}/go.calls"
  export PATH="${BATS_TEST_TMPDIR}/bin:${PATH}"
  unset SLOP_REVIEW_INTENT
  WORK="$(cd "${BATS_TEST_TMPDIR}/work" && pwd -P)"
}

@test "a relative --intent <file> is resolved against the caller's directory" {
  cd "${WORK}/sub"
  run "${BATS_TEST_DIRNAME}/review.sh" --intent ../pr.md
  assert_success
  grep -Fx "arg=--intent=${WORK}/sub/../pr.md" "${GO_CALLS}"
}

@test "a relative --intent=<file> is resolved against the caller's directory" {
  cd "${WORK}"
  run "${BATS_TEST_DIRNAME}/review.sh" --intent=pr.md
  assert_success
  grep -Fx "arg=--intent=${WORK}/pr.md" "${GO_CALLS}"
}

@test "an absolute --intent is left alone" {
  cd "${WORK}"
  run "${BATS_TEST_DIRNAME}/review.sh" --intent=/tmp/pr.md
  assert_success
  grep -Fx "arg=--intent=/tmp/pr.md" "${GO_CALLS}"
}

@test "a relative SLOP_REVIEW_INTENT is resolved against the caller's directory" {
  cd "${WORK}"
  SLOP_REVIEW_INTENT=pr.md run "${BATS_TEST_DIRNAME}/review.sh"
  assert_success
  grep -Fx "env=${WORK}/pr.md" "${GO_CALLS}"
}

@test "--intent without a file is a usage error" {
  run "${BATS_TEST_DIRNAME}/review.sh" --intent
  assert_failure 2
}
