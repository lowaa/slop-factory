# shellcheck shell=bash
# Parameters rows read as commands to shellcheck, and the stubs read the
# STUB_ variables an example sets.
# shellcheck disable=SC2215,SC2034

Describe 'quality_lib.sh'
  Include ./quality_lib.sh

  profiles() { echo "${QUALITY_PROFILES[*]}"; }
  coverdirs() { echo "${QUALITY_COVERDIRS[*]}"; }

  Describe 'quality_parse_args'
    It 'defaults to a check of every measure'
      When call quality_parse_args
      The status should be success
      The variable QUALITY_MODE should eq check
      The variable QUALITY_ONLY should eq coverage,complexity,testtime
      The variable QUALITY_MUTATION should eq ''
      The result of function profiles should eq ''
    End

    Describe 'a mode flag'
    Parameters
      --prune prune
      --rebaseline rebaseline
      --report report
    End
    It "takes $1 as the mode"
      When call quality_parse_args "$1"
      The status should be success
      The variable QUALITY_MODE should eq "$2"
    End
    End

    It 'refuses two modes'
      When call quality_parse_args --prune --rebaseline
      The status should eq 2
      The stderr should include 'exclusive'
    End

    It 'takes --only'
      When call quality_parse_args --only=coverage,testtime
      The status should be success
      The variable QUALITY_ONLY should eq coverage,testtime
    End

    It 'refuses an unknown measure'
      When call quality_parse_args --only=coverage,speed
      The status should eq 2
      The stderr should include "unknown measure 'speed'"
    End

    It 'makes --coverprofile and --coverdir absolute, and takes several'
      When call quality_parse_args --coverprofile=a.out --coverprofile=/abs/b.out --coverdir=cov
      The status should be success
      The result of function profiles should eq "${PWD}/a.out /abs/b.out"
      The result of function coverdirs should eq "${PWD}/cov"
    End

    Describe 'a mutation flag'
    Parameters
      --mutation changed
      --mutation=changed changed
      --mutation=all all
    End
    It "takes $1"
      When call quality_parse_args "$1"
      The status should be success
      The variable QUALITY_MUTATION should eq "$2"
    End
    End

    Describe 'a bad argument'
    Parameters
      --mutation=some
      --coverprofile=
      --only=
      --frobnicate
      positional
    End
    It "refuses $1"
      When call quality_parse_args "$1"
      The status should eq 2
      The stderr should include 'usage: quality.sh'
    End
    End
  End

  Describe 'quality_runs_tests'
    It 'runs them by default'
      quality_parse_args
      When call quality_runs_tests
      The status should be success
    End

    It 'runs them for testtime alone'
      quality_parse_args --only=testtime
      When call quality_runs_tests
      The status should be success
    End

    It 'skips them for complexity alone'
      quality_parse_args --only=complexity
      When call quality_runs_tests
      The status should be failure
    End

    It 'skips them for a given cover profile'
      quality_parse_args --coverprofile=c.out
      When call quality_runs_tests
      The status should be failure
    End
  End

  Describe 'quality_go'
    BeforeEach stub_setup
    AfterEach stub_cleanup

    project() { echo "${SCRATCH}/repo/proj"; }
    go_calls() { calls 'go '; }
    go_test_calls() { calls 'go test'; }
    quality_calls() { calls 'quality '; }
    all_calls() { cat "${STUB_LOG}"; }

    It 'runs the suite once, with -race and every package covered, and feeds both measures'
      When call quality_go "$(project)"
      The status should be success
      The stderr should include 'go test -race'
      The result of function go_test_calls should eq \
        "go test -race -count=1 -coverprofile=$(calls 'quality measure' | sed -E 's/.*--coverprofile ([^ ]+).*/\1/') -coverpkg=example.com/proj,example.com/proj/internal/x -json ./..."
      The result of function quality_calls should match pattern \
        'quality measure --module . --mode check --only coverage,complexity,testtime --prune-cmd proj/quality.sh --prune --coverprofile */cover.out --test-json */test.json'
    End

    It 'runs from the project directory, wherever it was called from'
      cd "${SCRATCH}" || return
      When call quality_go "$(project)" --coverprofile=c.out
      The status should be success
      The result of function quality_calls should include "--coverprofile ${SCRATCH}/c.out"
    End

    It 'lets go test cache results when nothing is timed'
      When call quality_go "$(project)" --only=coverage
      The status should be success
      The stderr should include 'go test'
      The result of function go_test_calls should start with 'go test -race -coverprofile='
    End

    It 'passes the CLI exit status through'
      STUB_QUALITY_EXIT=1
      When call quality_go "$(project)"
      The status should eq 1
      The stderr should include 'go test'
    End

    It 'reuses a cover profile instead of running the tests'
      When call quality_go "$(project)" --coverprofile=/given.out --rebaseline
      The status should be success
      The result of function go_calls should eq ''
      The result of function quality_calls should eq \
        'quality measure --module . --mode rebaseline --only coverage,complexity,testtime --prune-cmd proj/quality.sh --prune --coverprofile /given.out'
    End

    It 'skips the tests for complexity alone'
      When call quality_go "$(project)" --only=complexity
      The status should be success
      The result of function go_calls should eq ''
      The result of function quality_calls should include '--only complexity'
    End

    It 'adds a GOCOVERDIR to the test run'
      When call quality_go "$(project)" --coverdir=/e2e
      The status should be success
      The stderr should include 'go test'
      The result of function quality_calls should match pattern '* --coverdir /e2e --coverprofile */cover.out --test-json */test.json'
    End

    It 'writes reports to a directory per project'
      When call quality_go "${SCRATCH}/repo/proj/sub" --report --only=complexity
      The status should be success
      The result of function quality_calls should include "--mode report --only complexity --prune-cmd proj/sub/quality.sh --prune --report-dir ${SCRATCH}/reports/proj-sub"
    End

    It 'stops at a failing suite and shows the failures'
      STUB_GO_TEST_EXIT=1
      When call quality_go "$(project)"
      The status should eq 1
      The stderr should include 'go test failed (exit 1)'
      The result of function quality_calls should match pattern 'quality failures */test.json'
    End

    It 'fails when go list finds no packages'
      STUB_GO_PACKAGES=''
      When call quality_go "$(project)"
      The status should eq 1
      The stderr should include 'no packages'
      The result of function quality_calls should eq ''
    End

    It 'removes its scratch directory'
      When call quality_go "$(project)"
      The status should be success
      The stderr should include 'go test'
      The path "$(calls 'quality measure' | sed -E 's/.*--test-json ([^ ]+)\/test.json.*/\1/')" should not be exist
    End

    It 'stubs mutation testing, running nothing'
      When call quality_go "$(project)" --mutation=all
      The status should eq 2
      The stderr should eq 'quality.sh: --mutation=all: not implemented yet (plan task 10.1)'
      The result of function all_calls should eq ''
    End

    It 'refuses a bad argument before running anything'
      When call quality_go "$(project)" --prune --report
      The status should eq 2
      The stderr should include 'exclusive'
      The result of function all_calls should eq ''
    End
  End
End
