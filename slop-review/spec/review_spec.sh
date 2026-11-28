# shellcheck shell=bash

# review.sh's choice of .env, with `go` stubbed so nothing runs.
Describe 'review.sh'
  BeforeEach checkout_setup
  AfterEach checkout_cleanup

  Describe 'the .env it passes slop-review'
    It "is its own checkout's"
      : > "${MAIN}/.env"
      When run "${MAIN}/slop-review/review.sh"
      The status should be success
      The result of function env_file_args should eq "--env-file=${MAIN}/.env"
    End

    It "is the main worktree's in a linked worktree, which has no copy of the git-ignored file"
      : > "${MAIN}/.env"
      When run "${LINKED}/slop-review/review.sh"
      The status should be success
      The result of function env_file_args should eq "--env-file=${MAIN}/.env"
    End

    It "is a linked worktree's own, if it has one"
      : > "${MAIN}/.env"
      : > "${LINKED}/.env"
      When run "${LINKED}/slop-review/review.sh"
      The status should be success
      The result of function env_file_args should eq "--env-file=${LINKED}/.env"
    End

    It "is never the reviewed repository's"
      reviewed="${SCRATCH}/reviewed"
      mkdir -p "${reviewed}"
      git -C "${reviewed}" init -q
      : > "${reviewed}/.env"
      review_from_reviewed() { cd "${reviewed}" && "${MAIN}/slop-review/review.sh"; }
      When run review_from_reviewed
      The status should be success
      The result of function env_file_args should eq ''
    End

    It 'is left out when there is none'
      When run "${MAIN}/slop-review/review.sh"
      The status should be success
      The result of function env_file_args should eq ''
    End

    It "comes before the caller's arguments, so an explicit --env-file wins"
      : > "${MAIN}/.env"
      When run "${MAIN}/slop-review/review.sh" --env-file=/elsewhere/.env
      The status should be success
      The result of function env_file_args should eq "--env-file=${MAIN}/.env
--env-file=/elsewhere/.env"
    End

    It 'is passed to the label subcommand too, after its name'
      : > "${MAIN}/.env"
      When run "${MAIN}/slop-review/review.sh" label some-id --verdict=confirmed
      The status should be success
      The result of function go_args should eq "run
./cmd/slop-review
label
--env-file=${MAIN}/.env
some-id
--verdict=confirmed"
    End
  End
End
