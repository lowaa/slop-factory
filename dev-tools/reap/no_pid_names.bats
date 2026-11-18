#!/usr/bin/env bats
# A CI step's shell has the same PID on every run, so a docker object named
# after $$ collides between two runs on one daemon, and a killed run's
# leftovers block every later run. Name them with reap_compose_project.

setup() {
  bats_load_library repo_test
}

@test "no bats suite names anything after \$\$" {
  cd "${BATS_TEST_DIRNAME}/../.."
  run bash -c "git ls-files -z -- '*.bats' | xargs -0 grep -n -F '\$\$' | grep -v -e '^dev-tools/reap/no_pid_names.bats:' | grep -v ':[0-9]*: *#'"
  assert_failure
  assert_output ""
}
