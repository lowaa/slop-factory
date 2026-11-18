# Tests for lint.sh: which runners a path or a change maps to, with what
# flags, in a scratch repo with stub runners; and that the real registry
# covers the repo and matches what each project's test.sh runs.
#
#   bats/run.sh dev-tools/lint/lint.bats   # from the repo root

LINT_SH="${BATS_TEST_DIRNAME}/lint.sh"
REAL_REPO="$(cd "${BATS_TEST_DIRNAME}/../.." && pwd)"

setup() {
  bats_load_library repo_test
  REPO="${BATS_TEST_TMPDIR}/repo"
  mkdir -p "${REPO}/dev-tools/lint"
  cat > "${REPO}/dev-tools/lint/projects.txt" << 'EOF'
# a comment, and the nested project first, so the innermost wins by length, not by order
go mod/nested
go mod
oxlint web
ruff py
EOF
  file mod/go.mod 'module m'
  file mod/a.go 'package m'
  file mod/nested/go.mod 'module n'
  file mod/nested/b.go 'package n'
  file web/src/a.ts 'export const a = 1;'
  file py/pyproject.toml '[project]'
  file py/x.py 'x = 1'
  file scripts/run.sh 'echo hi'
  file scripts/tool '#!/usr/bin/env bash'
  file scripts/data 'not a script'
  file docs/readme.md 'docs'
  # What npm_complete.mjs reads: an install that matches its lockfile.
  file package-lock.json '{"packages": {}}'
  file node_modules/.package-lock.json '{"packages": {}}'
  file .gitignore 'node_modules/'
  git -C "${REPO}" init -q -b main
  git -C "${REPO}" add -A
  git -C "${REPO}" -c user.name=t -c user.email=t@t commit -q -m base
  git -C "${REPO}" switch -q -c feature

  for runner in go oxlint ruff repo shell; do
    stub_recorder "${BATS_TEST_TMPDIR}/runners/${runner}.sh" "${BATS_TEST_TMPDIR}/calls"
  done
  export LINT_REPO="${REPO}" LINT_RUNNERS="${BATS_TEST_TMPDIR}/runners"
}

file() { # file <path under the repo> <content>
  mkdir -p "$(dirname "${REPO}/$1")"
  printf '%s\n' "$2" > "${REPO}/$1"
}

commit() { git -C "${REPO}" -c user.name=t -c user.email=t@t commit -q -am "$1"; }

lint() { (cd "${REPO}" && "${LINT_SH}" "$@"); }

@test "with no paths it lints every project and the repo-wide tools" {
  run lint --dry-run
  assert_success
  assert_output "dev-tools/lint/repo.sh
dev-tools/lint/go.sh mod
dev-tools/lint/go.sh mod/nested
dev-tools/lint/oxlint.sh web
dev-tools/lint/ruff.sh py
dev-tools/lint/shell.sh"
}

@test "a file maps to its project's runner by what it is, the innermost project where they nest" {
  run lint --dry-run --no-repo mod/nested/b.go
  assert_output "dev-tools/lint/go.sh mod/nested"
  run lint --dry-run --no-repo mod/a.go
  assert_output "dev-tools/lint/go.sh mod"
  run lint --dry-run --no-repo web/src/a.ts
  assert_output "dev-tools/lint/oxlint.sh web"
  run lint --dry-run --no-repo py/x.py
  assert_output "dev-tools/lint/ruff.sh py"
  file mod/lint_baseline/gosec.txt 'a.go G101'
  file py/lint_baseline/ruff.txt 'x.py F401 os'
  run lint --dry-run --no-repo mod/lint_baseline/gosec.txt py/lint_baseline/ruff.txt
  assert_output "dev-tools/lint/go.sh mod
dev-tools/lint/ruff.sh py"
}

@test "shell scripts, by extension or shebang, map to shell.sh even without the repo-wide tools" {
  run lint --dry-run --no-repo scripts/run.sh
  assert_output "dev-tools/lint/shell.sh"
  run lint --dry-run --no-repo scripts/tool
  assert_output "dev-tools/lint/shell.sh"
  run lint --dry-run --no-repo scripts/data docs/readme.md
  assert_success
  assert_output "lint.sh: nothing to lint"
}

@test "a file no linter reads gets only the repo-wide tools" {
  run lint --dry-run docs/readme.md
  assert_output "dev-tools/lint/repo.sh
dev-tools/lint/shell.sh"
}

@test "a directory takes in every project in or under it, and paths are the caller's" {
  run lint --dry-run --no-repo mod
  assert_output "dev-tools/lint/go.sh mod
dev-tools/lint/go.sh mod/nested
dev-tools/lint/shell.sh"
  run bash -c "cd '${REPO}/mod/nested' && '${LINT_SH}' --dry-run --no-repo b.go ../a.go"
  assert_output "dev-tools/lint/go.sh mod
dev-tools/lint/go.sh mod/nested"
  run lint --dry-run "${BATS_TEST_TMPDIR}"
  assert_failure 2
  assert_output --partial "is outside"
}

@test "a linter's own config lints every project of its runner" {
  file dev-tools/lint/revive.toml ''
  file dev-tools/lint/ruff.toml ''
  run lint --dry-run --no-repo dev-tools/lint/revive.toml
  assert_output "dev-tools/lint/go.sh mod
dev-tools/lint/go.sh mod/nested"
  run lint --dry-run --no-repo dev-tools/lint/ruff.toml
  assert_output "dev-tools/lint/ruff.sh py"
}

@test "--repo-only runs the repo-wide tools alone, and refuses paths" {
  run lint --dry-run --repo-only
  assert_output "dev-tools/lint/repo.sh
dev-tools/lint/shell.sh"
  run lint --dry-run --repo-only mod
  assert_failure 2
}

@test "--changed: committed, uncommitted, untracked and deleted files, and nothing else" {
  file mod/nested/b.go 'package n // edited'
  commit "a commit on the branch"
  file web/src/a.ts 'export const a = 2;'
  file py/new.py 'y = 2'
  rm "${REPO}/scripts/tool"
  run lint --dry-run --changed --no-repo
  assert_success
  assert_output "dev-tools/lint/go.sh mod/nested
dev-tools/lint/oxlint.sh web
dev-tools/lint/ruff.sh py"
  rm "${REPO}/py/new.py" "${REPO}/web/src/a.ts"
  run lint --dry-run --changed --no-repo
  assert_output "dev-tools/lint/go.sh mod/nested
dev-tools/lint/oxlint.sh web"
}

@test "--changed: a deleted script still maps to shell.sh, and no change at all leaves the repo-wide tools" {
  run lint --dry-run --changed
  assert_output "dev-tools/lint/repo.sh
dev-tools/lint/shell.sh"
  git -C "${REPO}" rm -q scripts/run.sh
  run lint --dry-run --changed --no-repo
  assert_output "dev-tools/lint/shell.sh"
}

@test "--changed diffs against the merge-base, so main moving on adds nothing" {
  git -C "${REPO}" switch -q main
  file py/x.py 'x = 3'
  commit "main moves on"
  git -C "${REPO}" switch -q feature
  run lint --dry-run --changed --no-repo
  assert_success
  assert_output "lint.sh: nothing to lint"
}

@test "--prune, --rebaseline and --only pass through; --only drops the runners it leaves no tool" {
  run lint --dry-run --prune
  assert_success
  assert_line "dev-tools/lint/go.sh --prune mod"
  assert_line "dev-tools/lint/repo.sh --prune"
  assert_line "dev-tools/lint/shell.sh --prune"
  run lint --dry-run --rebaseline web
  assert_line "dev-tools/lint/oxlint.sh --rebaseline web"
  run lint --dry-run --only=gosec,errcheck,shfmt,ruff
  assert_output "dev-tools/lint/go.sh --only=gosec,errcheck mod
dev-tools/lint/go.sh --only=gosec,errcheck mod/nested
dev-tools/lint/ruff.sh py
dev-tools/lint/shell.sh --only=shfmt"
  run lint --dry-run --only=bogus
  assert_failure 2
  run lint --dry-run --changed mod
  assert_failure 2
}

@test "every runner runs, and one failing fails the run, naming it" {
  stub_recorder "${LINT_RUNNERS}/go.sh" "${BATS_TEST_TMPDIR}/calls" 1 "mod/nested"
  run lint
  assert_failure 1
  assert_output --partial "lint.sh: failed: go mod/nested"
  assert_line --regexp "^FAIL go mod/nested +[0-9.]+s$"
  assert_line --regexp "^ok   ruff py +[0-9.]+s$"
  # The rest ran all the same: one call each, repo.sh's and shell.sh's
  # without arguments.
  run sort "${BATS_TEST_TMPDIR}/calls"
  assert_output "

mod
mod/nested
py
web"
}

@test "a clean run passes" {
  run lint
  assert_success
  assert_line --regexp "^lint.sh: [0-9.]+s$"
  refute_output --partial "FAIL"
}

@test "oxlint and knip fail, saying why, on an install that is not the lockfile's" {
  file package-lock.json '{"packages": {"node_modules/typescript": {"version": "6.0.2"}}}'
  run lint
  assert_failure
  assert_output --partial "missing node_modules/typescript"
  assert_line --regexp "^FAIL oxlint web +"
  assert_line --regexp "^FAIL repo +"
  assert_line --regexp "^ok   go mod +"
}

# The real repo.

registered() { # registered <runner>: the real registry's directories for it
  awk -v r="$1" '$1 == r { print $2 }' "${REAL_REPO}/dev-tools/lint/projects.txt"
}

@test "the registry has every Go module in go.work" {
  local mods
  mods="$(sed -nE 's|^[[:space:]]+\./([^ ]+)$|\1|p' "${REAL_REPO}/go.work" | sort)"
  [[ -n "${mods}" ]]
  assert_equal "$(registered go | sort)" "${mods}"
}

@test "every Python file is in a ruff project" {
  local f p ok missing=()
  while IFS= read -r f; do
    ok=""
    for p in $(registered ruff); do [[ "${f}" != "${p}/"* ]] || ok=1; done
    [[ -n "${ok}" ]] || missing+=("${f}")
  done < <(git -C "${REAL_REPO}" ls-files '*.py')
  assert_equal "${missing[*]}" ""
}

@test "each registered project's own tests run its runner, so CI does" {
  local runner dir d found missing=()
  while read -r runner dir; do
    [[ -n "${runner}" && "${runner}" != \#* ]] || continue
    # The project's test.sh, or the native_test.sh it runs in its image, or
    # an enclosing project's test.sh (go-db's lints its e2e_test).
    found=""
    d="${dir}"
    while [[ -z "${found}" && "${d}" != . ]]; do
      grep -qsE "lint/${runner}\.sh" "${REAL_REPO}/${d}/test.sh" "${REAL_REPO}/${d}/native_test.sh" && found=1
      d="$(dirname "${d}")"
    done
    [[ -n "${found}" ]] || missing+=("${runner} ${dir}")
  done < "${REAL_REPO}/dev-tools/lint/projects.txt"
  assert_equal "${missing[*]}" ""
}
