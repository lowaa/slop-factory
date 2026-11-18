# Tests for ruff.sh against a scratch project, with the pinned ruff (uv
# fetches it on the first run).
#
#   bats/run.sh dev-tools/lint/ruff.bats   # from the repo root

RUFF_SH="${BATS_TEST_DIRNAME}/ruff.sh"

setup() {
  bats_load_library repo_test
  require_command uv git
  PROJ="${BATS_TEST_TMPDIR}/p"
  mkdir -p "${PROJ}"
  git -C "${PROJ}" init -q
  py pyproject.toml '[project]
name = "p"
requires-python = ">=3.12"'
  py a.py 'def add(a: int, b: int) -> int:
    return a + b'
}

py() { # py <path under the project> <content>
  mkdir -p "$(dirname "${PROJ}/$1")"
  printf '%s\n' "$2" > "${PROJ}/$1"
}

# branches <n>: a function whose cyclomatic complexity is n.
branches() {
  local i body=""
  for ((i = 1; i < $1; i++)); do body+="    if x == ${i}:"$'\n'"        return ${i}"$'\n'; done
  printf 'def branchy(x: int) -> int:\n%s    return 0\n' "${body}"
}

@test "a clean project passes, and a new finding fails naming file, rule and name" {
  run "${RUFF_SH}" "${PROJ}"
  assert_success
  py b.py 'import os'
  run "${RUFF_SH}" "${PROJ}"
  assert_failure
  assert_output --partial "  b.py F401 os"
}

@test "a baselined finding passes, and is stale once fixed" {
  py b.py 'import os'
  run "${RUFF_SH}" --rebaseline "${PROJ}"
  assert_success
  assert_equal "$(cat "${PROJ}/lint_baseline/ruff.txt")" "b.py F401 os"
  run "${RUFF_SH}" "${PROJ}"
  assert_success
  py b.py 'x = 1'
  run "${RUFF_SH}" "${PROJ}"
  assert_failure
  assert_output --partial "no longer found"
  run "${RUFF_SH}" --prune "${PROJ}"
  assert_success
  run "${RUFF_SH}" "${PROJ}"
  assert_success
}

@test "complexity over 15 fails, naming the function; 15 passes" {
  branches 15 > "${PROJ}/c.py"
  run "${RUFF_SH}" "${PROJ}"
  assert_success
  branches 16 > "${PROJ}/c.py"
  run "${RUFF_SH}" "${PROJ}"
  assert_failure
  assert_output --partial "  c.py C901 branchy"
}

@test "bugbear, commented-out code and unused function arguments are on; unused method arguments are not" {
  py d.py 'def f(unused: int) -> list[int]:
    # x = compute(1)
    return []


def g(xs: list[int] = []) -> None:
    pass


class C:
    def m(self, unused: int) -> None:
        pass'
  run "${RUFF_SH}" --rebaseline "${PROJ}"
  assert_success
  run cat "${PROJ}/lint_baseline/ruff.txt"
  assert_line "d.py ARG001 unused"
  assert_line "d.py ERA001"
  assert_line "d.py B006"
  refute_line --partial "ARG002"
}

@test "untracked files count; generated files and nested projects do not" {
  py gen/g.py 'import os'
  py gen/.gitattributes 'g.py linguist-generated=true'
  py sub/pyproject.toml '[project]'
  py sub/s.py 'import os'
  run "${RUFF_SH}" "${PROJ}"
  assert_success
  py new.py 'import sys'
  run "${RUFF_SH}" "${PROJ}"
  assert_failure
  assert_output --partial "  new.py F401 sys"
  refute_output --partial "g.py"
  refute_output --partial "s.py"
}

@test "the project's requires-python is the target: 3.12 syntax is a broken run on 3.9" {
  py e.py 'type Number = int'
  run "${RUFF_SH}" "${PROJ}"
  assert_success
  py pyproject.toml '[project]
name = "p"
requires-python = ">=3.9"'
  run "${RUFF_SH}" --rebaseline "${PROJ}"
  assert_failure
  assert_output --partial "unrecognised tool output"
  [ ! -e "${PROJ}/lint_baseline/ruff.txt" ]
}

@test "code that does not parse fails as a broken run, not as debt" {
  py f.py 'def broken(:'
  run "${RUFF_SH}" --rebaseline "${PROJ}"
  assert_failure
  assert_output --partial "did not run cleanly"
  [ ! -e "${PROJ}/lint_baseline/ruff.txt" ]
}
