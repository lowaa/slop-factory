# shellcheck shell=bash
# Parameters rows read as commands to shellcheck, and the stubs read the
# STUB_ variables an example sets.
# shellcheck disable=SC2215,SC2034

Describe 'mutation_lib.sh'
  Include ./mutation_lib.sh

  timeout_calls() { calls 'timeout '; }

  Describe 'mutation_parse_args'
    It 'takes the project and defaults to a check'
      When call mutation_parse_args proj
      The status should be success
      The variable MUTATION_PROJECT should eq proj
      The variable MUTATION_MODE should eq check
      The variable MUTATION_PACKAGES should eq ''
    End

    Describe 'a mode flag'
    Parameters
      --prune prune
      --rebaseline rebaseline
      --report report
    End
    It "takes $1 as the mode"
      When call mutation_parse_args proj "$1"
      The status should be success
      The variable MUTATION_MODE should eq "$2"
    End
    End

    It 'takes --packages, in any position'
      When call mutation_parse_args --packages=./a,./b/... proj
      The status should be success
      The variable MUTATION_PROJECT should eq proj
      The variable MUTATION_PACKAGES should eq './a,./b/...'
    End

    It 'refuses two modes'
      When call mutation_parse_args proj --prune --rebaseline
      The status should eq 2
      The stderr should include 'exclusive'
    End

    It 'requires a project'
      When call mutation_parse_args --report
      The status should eq 2
      The stderr should include 'project directory is required'
    End

    It 'refuses two projects'
      When call mutation_parse_args one two
      The status should eq 2
      The stderr should include 'one project directory only'
    End

    Describe 'a bad argument'
    Parameters
      --packages=
      --frobnicate
      -h
    End
    It "refuses $1"
      When call mutation_parse_args proj "$1"
      The status should eq 2
      The stderr should include 'usage: mutation.sh'
    End
    End
  End

  Describe 'mutation_patterns'
    BeforeEach stub_setup
    AfterEach stub_cleanup

    It 'splits --packages on commas'
      MUTATION_PACKAGES='./a,./b/...'
      When call mutation_patterns "${STUB_PROJECT}"
      The status should be success
      The line 1 of output should eq './a'
      The line 2 of output should eq './b/...'
    End

    It 'reads the default list, without comments or blank lines'
      MUTATION_PACKAGES=''
      printf '%s\n' '# the core' './a  # trailing' '' './b/...' > "${STUB_PROJECT}/mutation_baseline/packages.txt"
      When call mutation_patterns "${STUB_PROJECT}"
      The status should be success
      The output should eq './a
./b/...'
    End

    It 'is an error with neither'
      MUTATION_PACKAGES=''
      When call mutation_patterns "${STUB_PROJECT}"
      The status should eq 2
      The stderr should include 'no --packages'
    End
  End

  Describe 'mutation_expand'
    BeforeEach stub_setup
    AfterEach stub_cleanup

    It 'names the packages relative to the module, sorted and unique'
      STUB_GO_DIRS="${STUB_PROJECT}/internal/y
${STUB_PROJECT}/internal/x
${STUB_PROJECT}/internal/y"
      When call mutation_expand './internal/...' ./internal/x
      The status should be success
      The output should eq './internal/x
./internal/y'
    End

    It 'refuses a pattern that matches nothing, rather than measuring less'
      STUB_GO_LIST_EXIT=1
      When call mutation_expand ./typo
      The status should eq 2
      The stderr should include "'./typo' matches no package"
    End
  End

  Describe 'mutation_workers'
    It 'is the override when given'
      MUTATION_WORKERS=7
      When call mutation_workers
      The output should eq 7
    End

    It 'is at most 4 by default'
      MUTATION_WORKERS=''
      When call mutation_workers
      The output should match pattern '[1-4]'
    End
  End

  Describe 'mutation_capped'
    BeforeEach stub_setup
    AfterEach stub_cleanup

    It 'runs the command under timeout with the cap, and passes its status through'
      When call mutation_capped 99 bash -c 'exit 3'
      The status should eq 3
      The result of function timeout_calls should eq 'timeout 99'
    End
  End

  Describe 'mutation_rel and mutation_slug'
    BeforeEach stub_setup
    AfterEach stub_cleanup

    It 'names a project in the repo by its path from the root'
      When call mutation_rel "${MUTATION_ROOT}/slop-bot/sub"
      The output should eq 'slop-bot/sub'
    End

    It 'names a checkout elsewhere by its base name'
      When call mutation_rel /elsewhere/slop-bot
      The output should eq 'slop-bot'
    End

    It 'makes the slug of a nested project'
      When call mutation_slug "${MUTATION_ROOT}/slop-bot/sub"
      The output should eq 'slop-bot-sub'
    End
  End

  Describe 'mutation_go'
    BeforeEach stub_setup
    AfterEach stub_cleanup

    project() { echo "${STUB_PROJECT}"; }
    gremlins_calls() { calls 'gremlins '; }
    go_build_calls() { calls 'go build'; }
    mutation_calls() { calls 'mutation '; }
    all_calls() { cat "${STUB_LOG}"; }
    build_gowork() { calls 'GOWORK='; }

    It 'runs gremlins on each package alone, with bounded timeouts, then summarises'
      MUTATION_WORKERS=2
      When call mutation_go "$(project)" --packages=./internal/x,./internal/y
      The status should be success
      The stderr should include '== gremlins ./internal/x'
      The result of function gremlins_calls should match pattern \
        '*gremlins unleash --workers 2 --timeout-coefficient 10 --coverpkg ./internal/x -o *.json ./internal/x*'
      The result of function gremlins_calls should match pattern \
        '*--coverpkg ./internal/y -o *.json ./internal/y'
      The result of function mutation_calls should match pattern \
        "mutation summarize --project $(project) --mode check --prune-cmd dev-tools/mutation/mutation.sh proj --prune --result ./internal/x=*.json --result ./internal/y=*.json"
    End

    It 'caps each package run, by default and as overridden'
      MUTATION_PACKAGE_TIMEOUT=42
      STUB_GO_DIRS="$(project)/internal/x"
      When call mutation_go "$(project)" --packages=./internal/x
      The status should be success
      The stderr should include 'gremlins'
      The result of function timeout_calls should eq 'timeout 42'
    End

    It 'overrides the timeout coefficient'
      MUTATION_TIMEOUT_COEFFICIENT=4
      When call mutation_go "$(project)" --packages=./internal/x
      The status should be success
      The stderr should include 'gremlins'
      The result of function gremlins_calls should include '--timeout-coefficient 4'
    End

    It 'takes the default packages from the project'
      printf '%s\n' './internal/x' > "$(project)/mutation_baseline/packages.txt"
      STUB_GO_DIRS="$(project)/internal/x"
      When call mutation_go "$(project)"
      The status should be success
      The stderr should include '== gremlins ./internal/x'
      The result of function gremlins_calls should match pattern '*unleash*./internal/x'
    End

    It 'passes the mode through, and the CLIs exit status'
      STUB_MUTATION_EXIT=1
      When call mutation_go "$(project)" --prune --packages=./internal/x
      The status should eq 1
      The stderr should include 'gremlins'
      The result of function mutation_calls should include '--mode prune'
    End

    It 'writes no report unless asked'
      When call mutation_go "$(project)" --packages=./internal/x
      The status should be success
      The stderr should include 'gremlins'
      The result of function mutation_calls should not include '--report-dir'
    End

    It 'writes the report to a directory per project for --report'
      MUTATION_REPORT_DIR="${SCRATCH}/reports"
      When call mutation_go "$(project)" --report --packages=./internal/x
      The status should be success
      The stderr should include 'gremlins'
      The result of function mutation_calls should include "--mode report"
      The result of function mutation_calls should include "--report-dir ${SCRATCH}/reports/proj"
    End

    It 'also writes it from a check when the directory is set explicitly'
      MUTATION_REPORT_DIR="${SCRATCH}/reports"
      When call mutation_go "$(project)" --packages=./internal/x
      The status should be success
      The stderr should include 'gremlins'
      The result of function mutation_calls should include "--mode check"
      The result of function mutation_calls should include "--report-dir ${SCRATCH}/reports/proj"
    End

    It 'stops at a failing gremlins, showing its output, without summarising'
      STUB_GREMLINS_EXIT=1
      When call mutation_go "$(project)" --packages=./internal/x
      The status should eq 2
      The stderr should include 'gremlins failed on ./internal/x'
      The stderr should include 'gremlins output for the log'
      The result of function mutation_calls should eq ''
    End

    It 'stops when gremlins writes no report'
      STUB_GREMLINS_WRITE=0
      When call mutation_go "$(project)" --packages=./internal/x
      The status should eq 2
      The stderr should include 'wrote no report for ./internal/x'
      The result of function mutation_calls should eq ''
    End

    It 'refuses a missing project directory before running anything'
      When call mutation_go "${SCRATCH}/nope" --packages=./internal/x
      The status should eq 2
      The stderr should include 'no project directory'
      The result of function all_calls should eq ''
    End

    It 'refuses a directory that is not a Go module'
      rm "$(project)/go.mod"
      When call mutation_go "$(project)" --packages=./internal/x
      The status should eq 2
      The stderr should include 'has no go.mod'
      The result of function all_calls should eq ''
    End

    It 'refuses a bad argument before running anything'
      When call mutation_go "$(project)" --prune --report
      The status should eq 2
      The stderr should include 'exclusive'
      The result of function all_calls should eq ''
    End

    It 'builds the pinned gremlins and its CLI against its own go.sum when none is given'
      unset MUTATION_BIN MUTATION_GREMLINS_BIN
      unset GOWORK
      When call mutation_go "$(project)" --packages=./internal/x
      The status should be success
      The stderr should include 'gremlins'
      The result of function go_build_calls should match pattern '*go build -o */gremlins github.com/go-gremlins/gremlins/cmd/gremlins*'
      The result of function go_build_calls should include './cmd/mutation'
      The result of function build_gowork should eq 'GOWORK=off
GOWORK=off'
    End

    It 'stops when a tool does not build'
      unset MUTATION_BIN MUTATION_GREMLINS_BIN
      STUB_GO_BUILD_EXIT=1
      When call mutation_go "$(project)" --packages=./internal/x
      The status should eq 2
      The stderr should include 'building gremlins failed'
      The result of function gremlins_calls should eq ''
    End

    It 'removes its scratch directory'
      When call mutation_go "$(project)" --packages=./internal/x
      The status should be success
      The stderr should include 'gremlins'
      The path "$(calls 'mutation summarize' | sed -E 's/.*--result [^=]+=(.*)\/[^/]+\.json.*/\1/')" should not be exist
    End
  End

  Describe 'the pinned tool'
    It 'is a tool directive in go.mod'
      When run grep -c '^tool github.com/go-gremlins/gremlins/cmd/gremlins$' go.mod
      The output should eq 1
    End
  End
End
