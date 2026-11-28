# shellcheck shell=bash
# shellcheck disable=SC2034

# The real pinned gremlins on a tiny module whose only test asserts nothing:
# the stubbed specs can't show that the tool's quirks (a cached coverage run,
# a package main, a package with nothing to mutate) don't turn into scores.
Describe 'mutation.sh with the real gremlins'
  setup() {
    FIXTURE="$(mktemp -d)"
    REPORTS="${FIXTURE}/reports"
    mkdir -p "${FIXTURE}/mod/calc" "${FIXTURE}/mod/cmd/tool" "${FIXTURE}/mod/types" "${FIXTURE}/mod/mutation_baseline"
    printf 'module example.com/mod\n\ngo 1.22\n' > "${FIXTURE}/mod/go.mod"
    printf 'package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Max(a, b int) int {\n\tif a > b {\n\t\treturn a\n\t}\n\treturn b\n}\n' > "${FIXTURE}/mod/calc/calc.go"
    printf 'package calc\n\nimport "testing"\n\nfunc TestRuns(t *testing.T) { Add(1, 2); Max(1, 2) }\n' > "${FIXTURE}/mod/calc/calc_test.go"
    printf 'package main\n\nfunc main() { _ = 1 + 2 }\n' > "${FIXTURE}/mod/cmd/tool/main.go"
    printf 'package types\n\ntype T struct{ A int }\n' > "${FIXTURE}/mod/types/types.go"
    printf 'package types\n\nimport "testing"\n\nfunc TestT(t *testing.T) { _ = T{} }\n' > "${FIXTURE}/mod/types/types_test.go"
    export MUTATION_REPORT_DIR="${REPORTS}" MUTATION_WORKERS=2
  }
  cleanup() { rm -rf "${FIXTURE}"; }
  BeforeEach setup
  AfterEach cleanup

  survivors() { cat "${REPORTS}/mod/survivors.txt"; }

  It 'reports the survivors of a test that asserts nothing, again on a warm cache'
    run_twice() {
      ./mutation.sh "${FIXTURE}/mod" --packages=./calc --report > /dev/null 2>&1
      ./mutation.sh "${FIXTURE}/mod" --packages=./calc --report > /dev/null 2>&1
    }
    When call run_twice
    The status should be success
    The result of function survivors should include './calc/calc.go'
  End

  It 'keeps going past a package with nothing to mutate'
    When run ./mutation.sh "${FIXTURE}/mod" --packages=./types,./calc --report
    The status should be success
    The output should include './types'
    The output should include 'n/a'
    The stderr should include '== gremlins ./calc'
  End

  It 'skips package main rather than scoring it 100%'
    When run ./mutation.sh "${FIXTURE}/mod" --packages=./cmd/...,./calc --report
    The status should be success
    The output should not include './cmd/tool'
    The stderr should include 'skipping /cmd/tool'
  End
End
