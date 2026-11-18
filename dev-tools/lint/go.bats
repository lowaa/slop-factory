# Tests for go.sh against a scratch module, with the real tools (built once
# into go.sh's cache, so the first run takes a minute).
#
#   bats/run.sh dev-tools/lint/go.bats   # from the repo root

GO_SH="${BATS_TEST_DIRNAME}/go.sh"

setup() {
  bats_load_library repo_test
  require_command go
  MOD="${BATS_TEST_TMPDIR}/m"
  mkdir -p "${MOD}/cmd/m"
  printf 'module example.com/m\n\ngo 1.27.4\n' > "${MOD}/go.mod"
  # Outside the repo's go.work, so linted on its own.
  export LINT_GOWORK=off
  write a.go 'package m

import "os"

// Read drops the Close error, which errcheck reports.
func Read(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return nil
}'
  write cmd/m/main.go 'package main

import "example.com/m"

func main() { _ = m.Read("x") }'
}

write() { # write <path under the module> <content>
  mkdir -p "$(dirname "${MOD}/$1")"
  printf '%s\n' "$2" > "${MOD}/$1"
}

lint() { "${GO_SH}" "$@" "${MOD}"; }

@test "an unchanged module passes once baselined" {
  run lint --only=errcheck --rebaseline
  assert_success
  assert_equal "$(cat "${MOD}/lint_baseline/errcheck.txt")" "a.go errcheck defer f.Close()"
  run lint --only=errcheck
  assert_success
}

@test "a new finding fails and names the file and rule" {
  lint --only=staticcheck --rebaseline
  write b.go 'package m

import "errors"

var ErrBad = errors.New("Bad thing.")'
  run lint --only=staticcheck
  assert_failure
  assert_output --partial "  b.go ST1005"
  assert_output --partial "failed: staticcheck"
}

@test "a fixed finding left in the baseline fails until --prune removes it" {
  lint --only=errcheck --rebaseline
  write a.go 'package m

func Read(string) error { return nil }'
  run lint --only=errcheck
  assert_failure
  assert_output --partial "  a.go errcheck defer f.Close()"
  assert_output --partial "dev-tools/lint/go.sh --prune --only=errcheck"
  run lint --only=errcheck --prune
  assert_success
  run lint --only=errcheck
  assert_success
}

@test "code that does not build fails as a broken run, not as debt" {
  write c.go 'package m

func Broken() { undefinedFunction() }'
  run lint --only=staticcheck --rebaseline
  assert_failure
  assert_output --partial "staticcheck (did not run)"
  [ ! -e "${MOD}/lint_baseline/staticcheck.txt" ]
}

@test "gosec findings carry the rule, with the path relative to the module" {
  write d.go 'package m

import "os"

func Write(b []byte) error { return os.WriteFile("out", b, 0o644) }'
  run lint --only=gosec --rebaseline
  assert_success
  # a.go's os.Open of a variable path is G304.
  assert_equal "$(cat "${MOD}/lint_baseline/gosec.txt")" $'a.go G304\nd.go G306'
}

@test "gocognit names the function, skips tests, and leaves a nested module to itself" {
  local body='	for i := 0; i < n; i++ {
		if i%2 == 0 {
			n++
		}
	}
	return n'
  write e.go "package m

func Busy(n int) int {
${body}
}"
  write e_test.go "package m

func busyInTest(n int) int {
${body}
}"
  write nested/go.mod 'module example.com/nested

go 1.27.4'
  write nested/f.go "package nested

func Busy(n int) int {
${body}
}"
  GOCOGNIT_OVER=2 run lint --only=gocognit --rebaseline
  assert_success
  assert_equal "$(cat "${MOD}/lint_baseline/gocognit.txt")" "e.go gocognit Busy"
}

@test "revive's argument limit is a finding" {
  write g.go 'package m

func Many(a, b, c, d, e, f, g int) int { return a + b + c + d + e + f + g }'
  run lint --only=revive --rebaseline
  assert_success
  assert_equal "$(cat "${MOD}/lint_baseline/revive.txt")" "g.go argument-limit"
}

@test "deadcode runs only with a deadcode/ directory, against its allow-list" {
  write h.go 'package m

func Unwired() {}'
  run lint --only=deadcode
  assert_success
  assert_output --partial "no deadcode/ directory; skipped"

  mkdir "${MOD}/deadcode"
  run lint --only=deadcode
  assert_failure
  assert_output --partial "  h.go Unwired"

  printf 'h.go Unwired #42\n' > "${MOD}/deadcode/allowlist.txt"
  run lint --only=deadcode
  assert_success
}

@test "an unknown tool is a usage error" {
  run lint --only=golint
  assert_failure 2
  assert_output --partial "unknown tool 'golint'"
}

@test "an empty --only is a usage error, not a run of nothing" {
  run lint --only=
  assert_failure 2
}

@test "revive's function length, nesting and context rules are findings, and long tests are not" {
  local stmts="" i
  for i in $(seq 61); do stmts+=$'\tn++\n'; done
  write long.go "package m

func Long(n int) int {
${stmts}	return n
}"
  write long_test.go "package m

func longInTest(n int) int {
${stmts}	return n
}"
  write nest.go 'package m

func Nested(n int) int {
	if n > 0 {
		if n > 1 {
			if n > 2 {
				if n > 3 {
					if n > 4 {
						return 5
					}
				}
			}
		}
	}
	return 0
}'
  write ctx.go 'package m

import "context"

func Late(n int, ctx context.Context) int { _ = ctx; return n }'
  run lint --only=revive --rebaseline
  assert_success
  assert_equal "$(cat "${MOD}/lint_baseline/revive.txt")" $'ctx.go context-as-argument\nlong.go function-length\nnest.go max-control-nesting'
}

@test "deadcode reports an unimported package, and not unexported functions" {
  mkdir "${MOD}/deadcode"
  write h.go 'package m

func unwiredHelper() {}

var _ = unwiredHelper'
  write sub/sub.go 'package sub

func Orphan() {}'
  run lint --only=deadcode --rebaseline
  assert_success
  assert_equal "$(cat "${MOD}/deadcode/baseline.txt")" "sub/ package"
}

@test "a file gocognit cannot parse fails as a broken run" {
  write broken.go 'package m

func F( {'
  run lint --only=gocognit --rebaseline
  assert_failure
  assert_output --partial "gocognit (did not run)"
  [ ! -e "${MOD}/lint_baseline/gocognit.txt" ]
}
