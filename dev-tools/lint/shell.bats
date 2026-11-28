# Tests for shell.sh against a scratch repo, with the pinned shellcheck and
# shfmt images (docker pulls them on the first run).
#
#   bats/run.sh dev-tools/lint/shell.bats   # from the repo root

# shellcheck disable=SC2016 # the scripts under test are written out literally
SHELL_SH="${BATS_TEST_DIRNAME}/shell.sh"

setup() {
  bats_load_library repo_test
  require_command docker git
  REPO="${BATS_TEST_TMPDIR}/repo"
  mkdir -p "${REPO}/dev-tools/lint/baseline"
  git -C "${REPO}" init -q
  script ok.sh '#!/usr/bin/env bash
set -euo pipefail
echo "$1" > /dev/null'
  export LINT_REPO="${REPO}"
}

script() { # script <path under the repo> <content>
  mkdir -p "$(dirname "${REPO}/$1")"
  printf '%s\n' "$2" > "${REPO}/$1"
}

baseline() { cat "${REPO}/dev-tools/lint/baseline/$1.txt"; }

@test "a clean repo passes, and a shellcheck finding fails naming file and code" {
  run "${SHELL_SH}"
  assert_success
  script bad.sh '#!/usr/bin/env bash
rm $1'
  run "${SHELL_SH}" --only=shellcheck
  assert_failure
  assert_output --partial "  bad.sh SC2086"
  assert_output --partial "failed: shellcheck"
}

@test "a baselined finding passes, and is stale once fixed" {
  script bad.sh '#!/usr/bin/env bash
rm $1'
  run "${SHELL_SH}" --rebaseline --only=shellcheck
  assert_success
  assert_equal "$(baseline shellcheck)" "bad.sh SC2086"
  run "${SHELL_SH}" --only=shellcheck
  assert_success
  script bad.sh '#!/usr/bin/env bash
rm "$1"'
  run "${SHELL_SH}" --only=shellcheck
  assert_failure
  assert_output --partial "no longer found"
}

@test "shfmt: a script off the repo's style is one finding, shown as the diff" {
  script f.sh '#!/usr/bin/env bash
case "$1" in
a) echo >/dev/null ;;
esac'
  run "${SHELL_SH}" --only=shfmt
  assert_failure
  assert_output --partial "  f.sh shfmt"
  assert_output --partial "+  a) echo > /dev/null ;;"
  assert_output --partial "-w -i 2 -ci -sr"
  script f.sh '#!/usr/bin/env bash
case "$1" in
  a) echo > /dev/null ;;
esac'
  run "${SHELL_SH}" --only=shfmt
  assert_success
}

@test "scripts by extension, bats files and shebangs count; generated files, links and other files do not" {
  script t/x.bats '@test "x" {
  ! false
  true
}'
  script bin/tool '#!/bin/sh
echo $1'
  script bin/notes 'echo $1 is prose, not a script'
  script golden/g.sh 'echo $1'
  script golden/.gitattributes '* linguist-generated=true'
  # A link is its target, linted once as itself.
  ln -s ../bin/tool "${REPO}/t/link.sh"
  run "${SHELL_SH}" --rebaseline --only=shellcheck
  assert_success
  assert_output --partial "(3 scripts)"
  run baseline shellcheck
  assert_line "bin/tool SC2086"
  assert_line "t/x.bats SC2314"
  refute_output --partial "golden"
  refute_output --partial "notes"
}

@test "shfmt leaves shellspec specs alone; shellcheck still reads them" {
  script spec/x_spec.sh '# shellcheck shell=bash
Describe "x"
  It "y"
    When call echo $1
    The output should eq ""
  End
End'
  run "${SHELL_SH}" --only=shfmt
  assert_success
  run "${SHELL_SH}" --only=shellcheck
  assert_failure
  assert_output --partial "  spec/x_spec.sh SC2086"
}

@test "a script that does not parse fails as a broken run, not as debt" {
  script broken.sh '#!/usr/bin/env bash
if then fi'
  run "${SHELL_SH}" --rebaseline --only=shfmt
  assert_failure
  assert_output --partial "shfmt (did not run)"
  [ ! -s "${REPO}/dev-tools/lint/baseline/shfmt.txt" ]
}

@test "an unknown tool is a usage error" {
  run "${SHELL_SH}" --only=bogus
  assert_failure 2
}
