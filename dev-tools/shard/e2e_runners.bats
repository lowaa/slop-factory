#!/usr/bin/env bats
# The e2e suite's test.sh, sharded, against a docker whose test container
# runs its command here with an npx that only records: which Playwright
# projects each shard runs is the runner's own wiring, and needs no stack to
# show. Whether the names are still the playwright.config.ts projects is not
# checked here (that takes the suite's node_modules): a project added there
# and not named in its test.sh would silently go unrun when sharded.

setup() {
  bats_load_library repo_test
  load fake_npx
  DIR="${BATS_TEST_TMPDIR}"
  export DOCKER_CALLS="${DIR}/docker_calls"
  ROOT="$(cd "${BATS_TEST_DIRNAME}/../.." && pwd)"
  fake_npx "${DIR}"
  # `compose ... run ... e2e-tests-ts [cmd...]` runs the command (the image's
  # CMD when none is given), tagged with the shard it came from; anything
  # else succeeds, saying nothing -- but `down` exits DOWN_EXIT (default 0).
  # Other docker calls are logged to docker_calls, and `create` fails when
  # FAIL_CREATE is set.
  cat > "${DIR}/bin/docker" << 'EOF_DOCKER'
#!/usr/bin/env bash
if [[ "${1:-}" == compose ]]; then
  args=("$@")
  seen_run=0
  for i in "${!args[@]}"; do
    [[ "${args[i]}" == down ]] && exit "${DOWN_EXIT:-0}"
    [[ "${args[i]}" == run ]] && seen_run=1
    if ((seen_run)) && [[ "${args[i]}" == e2e-tests-ts ]]; then
      cmd=("${args[@]:i+1}")
      ((${#cmd[@]})) || cmd=(npx playwright test)
      NPX_TAG="${SHARD_INDEX:-0}" exec "${cmd[@]}"
    fi
  done
fi
echo "$*" >> "${DOCKER_CALLS}"
[[ "${1:-}" == create && -n "${FAIL_CREATE:-}" ]] && exit 1
exit 0
EOF_DOCKER
  chmod +x "${DIR}/bin/docker"
}

# The npx runs, in a stable order: shards record theirs concurrently.
calls() {
  sort "${DIR}/npx_calls"
}

@test "slop-haus: e2e is split, e2e_support runs whole in shard 1 only" {
  run env SHARDS=2 "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 0 ]
  [ "$(calls)" = "$(printf '%s\n' \
    "1:playwright test --project=e2e --shard=1/2" \
    "1:playwright test --project=e2e_support" \
    "2:playwright test --project=e2e --shard=2/2")" ]
}

@test "unsharded, the suite runs the image's own command, every project" {
  run "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 0 ]
  [ "$(calls)" = "0:playwright test" ]
}

@test "an e2e_support failure fails the sharded run, reported in shard 1's output" {
  run env SHARDS=2 FAIL_e2e_support=1 "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 1 ]
  [[ "${output}" == *"::group::shard 1/2 (exit 1, "* ]]
  [[ "${output}" == *"::group::shard 2/2 (exit 0, "* ]]
  # Shard 1's own share ran all the same.
  grep -qx "1:playwright test --project=e2e --shard=1/2" "${DIR}/npx_calls"
}

@test "a failing teardown does not fail a green run, but says so" {
  run env DOWN_EXIT=7 "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 0 ]
  [[ "${output}" == *"cleanup failed (exit 7)"* ]]
  [ "${lines[-1]}" = "test.sh: exiting 0 (run: 0, cleanup: 7)" ]
}

@test "slop-haus: a failing teardown leaves a failed run's own status, and the last line gives both" {
  run env DOWN_EXIT=7 SHARDS=2 FAIL_e2e_support=1 "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 1 ]
  [ "${lines[-1]}" = "test.sh: exiting 1 (run: 1, cleanup: 7)" ]
}

@test "slop-haus: a failed shard copies its test-results out of the volume into E2E_RESULTS_OUT/<shard>, before teardown" {
  run env SHARDS=2 FAIL_e2e_support=1 E2E_RESULTS_OUT="${DIR}/out" "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 1 ]
  # Shard 1 ran the failing project; shard 2 passed and copies nothing.
  grep -q "^create --name .*-results --label com.docker.compose.project=.* -v .*_e2e-results:/results " "${DOCKER_CALLS}"
  [ "$(grep -c '^create ' "${DOCKER_CALLS}")" -eq 1 ]
  grep -q "^cp .*-results:/results/. ${DIR}/out/1\$" "${DOCKER_CALLS}"
  grep -q "^rm -f .*-results\$" "${DOCKER_CALLS}"
}

@test "slop-haus: a green run copies nothing" {
  run env E2E_RESULTS_OUT="${DIR}/out" "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 0 ]
  ! grep -q '^create ' "${DOCKER_CALLS}"
  [ ! -e "${DIR}/out" ]
}

@test "slop-haus: a failed copy-out does not change the failed run's status" {
  run env FAIL_CREATE=1 SHARDS=2 FAIL_e2e_support=1 E2E_RESULTS_OUT="${DIR}/out" "${ROOT}/slop-haus/e2e_playwright_ts/test.sh"
  [ "${status}" -eq 1 ]
  [[ "${output}" == *"could not copy the test-results out"* ]]
}
