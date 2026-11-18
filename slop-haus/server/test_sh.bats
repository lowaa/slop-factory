bats_load_library repo_test
bats_require_minimum_version 1.5.0

# test.sh's default mode (run_all), against a fake docker: each suite is the
# fake's `docker run` of the image its `docker build` named.

SCRIPT="${BATS_TEST_DIRNAME}/test.sh"

setup() {
  FAKE_DIR=$(mktemp -d)
  mkdir "${FAKE_DIR}/bin"
  cat > "${FAKE_DIR}/bin/docker" << 'FAKE'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  build)
    shift
    while (($#)); do
      case "$1" in
        --target) target=$2; shift ;;
        --iidfile) iidfile=$2; shift ;;
      esac
      shift
    done
    echo "img-${target}" > "${iidfile}"
    ;;
  run)
    for arg in "$@"; do
      case "${arg}" in
        img-test) suite=go ;;
        img-workspace-test) suite=workspace ;;
        img-supervisor-test) suite=supervisor ;;
      esac
    done
    echo "$*" > "${FAKE_DIR}/args-${suite}"
    touch "${FAKE_DIR}/started-${suite}"
    # The barrier: no suite finishes until all three have started, which
    # only happens if they run at once. The bound fails a serial run rather
    # than hanging it.
    for _ in $(seq 100); do
      [[ -e "${FAKE_DIR}/started-go" && -e "${FAKE_DIR}/started-workspace" && -e "${FAKE_DIR}/started-supervisor" ]] && break
      sleep 0.1
    done
    [[ -e "${FAKE_DIR}/started-go" && -e "${FAKE_DIR}/started-workspace" && -e "${FAKE_DIR}/started-supervisor" ]] || { echo "${suite}: ran alone"; exit 99; }
    echo "${suite} output"
    for i in $(seq "${FAKE_EXTRA_LINES:-0}"); do echo "${suite} line ${i}"; done
    [[ "${FAKE_FAIL:-}" != "${suite}" ]] || exit 3
    ;;
  ps)
    echo "$*" >> "${FAKE_DIR}/ps"
    echo cid-1
    ;;
  rm) echo "$*" >> "${FAKE_DIR}/rm" ;;
  # reap_self_container: not in a container, so nothing to reap.
  container) exit 1 ;;
esac
FAKE
  chmod +x "${FAKE_DIR}/bin/docker"
  # The lint suite runs on the host, not through docker.
  cat > "${FAKE_DIR}/bin/fake_lint" << 'FAKE'
#!/usr/bin/env bash
echo "$*" > "${FAKE_DIR}/args-lint"
echo "lint output"
for i in $(seq "${FAKE_EXTRA_LINES:-0}"); do echo "lint line ${i}"; done
[[ "${FAKE_FAIL:-}" != lint ]] || exit 3
FAKE
  chmod +x "${FAKE_DIR}/bin/fake_lint"
  export FAKE_DIR PATH="${FAKE_DIR}/bin:${PATH}" REAP_SELF_CONTAINER= LINT_GO_SH="${FAKE_DIR}/bin/fake_lint" LINT_RUFF_SH="${FAKE_DIR}/bin/fake_lint"
}

teardown() { rm -rf "${FAKE_DIR}"; }

@test "the three suites run at once and a green run exits 0" {
  run "${SCRIPT}"
  assert_success
  refute_output --partial "ran alone"
  assert_line --index -1 "== go: ok, workspace: ok, supervisor: ok, lint: ok"
}

@test "a failing suite fails the run and is named, and the others still finish" {
  local suite
  for suite in go workspace supervisor lint; do
    rm -f "${FAKE_DIR}"/started-*
    FAKE_FAIL=${suite} run "${SCRIPT}"
    assert_failure
    assert_line --index -1 --partial "${suite}: FAIL (exit 3)"
    refute_output --partial "ran alone"
  done
}

@test "the side suites print whole under their header, after the Go suite's output" {
  run "${SCRIPT}"
  assert_success
  local go ws
  go=$(grep -n '^go output$' <<< "${output}" | cut -d: -f1)
  ws=$(grep -n '^== workspace (exit 0)$' <<< "${output}" | cut -d: -f1)
  ((go < ws))
  [ "$(sed -n "$((ws + 1))p" <<< "${output}")" = "workspace output" ]
  assert_line "== supervisor (exit 0)"
  assert_line "== lint (exit 0)"
}

@test "the lint suite lints the server module" {
  run "${SCRIPT}"
  assert_success
  [ "$(cat "${FAKE_DIR}/args-lint")" = "slop-haus/server" ]
}

@test "a failed side suite's log tail is repeated just before the summary" {
  local suite
  for suite in workspace supervisor lint; do
    rm -f "${FAKE_DIR}"/started-*
    FAKE_FAIL=${suite} run "${SCRIPT}"
    assert_failure
    assert_line --index -3 "== ${suite} failed: last 100 lines"
    assert_line --index -2 "${suite} output"
    assert_line --index -1 --partial "${suite}: FAIL (exit 3)"
  done
  rm -f "${FAKE_DIR}"/started-*
  FAKE_FAIL=go run "${SCRIPT}"
  refute_output --partial "failed: last 100 lines"
}

@test "the repeated tail is the last 100 lines of a long log" {
  FAKE_EXTRA_LINES=150 FAKE_FAIL=workspace run "${SCRIPT}"
  assert_failure
  local hdr
  hdr=$(grep -n '^== workspace failed: last 100 lines$' <<< "${output}" | cut -d: -f1)
  # header, 100 lines, summary
  [ "${hdr}" -eq "$((${#lines[@]} - 101))" ]
  [ "${lines[hdr]}" = "workspace line 51" ]
  assert_line --index -2 "workspace line 150"
}

@test "the side suites run at a low CPU weight, the Go suite at the default" {
  run "${SCRIPT}"
  assert_success
  grep -q -- '--cpu-shares=128' "${FAKE_DIR}/args-workspace"
  grep -q -- '--cpu-shares=128' "${FAKE_DIR}/args-supervisor"
  run ! grep -q -- '--cpu-shares' "${FAKE_DIR}/args-go"
}

@test "SOAK_SEED and SOAK_STEPS reach the Go suite's container only when set" {
  run "${SCRIPT}"
  assert_success
  run ! grep -q -- 'SOAK_' "${FAKE_DIR}/args-go"
  rm "${FAKE_DIR}"/started-* "${FAKE_DIR}"/args-*
  SOAK_SEED=random SOAK_STEPS=3000 run "${SCRIPT}"
  assert_success
  grep -q -- '-e SOAK_SEED=random' "${FAKE_DIR}/args-go"
  grep -q -- '-e SOAK_STEPS=3000' "${FAKE_DIR}/args-go"
  run ! grep -q -- 'SOAK_' "${FAKE_DIR}/args-workspace"
}

@test "on exit, the run removes the containers it labelled" {
  run "${SCRIPT}"
  assert_success
  local label
  label=$(grep -o -- '--label [^ ]*' "${FAKE_DIR}/args-go" | cut -d' ' -f2)
  [[ "${label}" == slophaus-server-test=* ]]
  grep -q -- "--label ${label}" "${FAKE_DIR}/args-workspace"
  grep -q -- "--filter label=${label}" "${FAKE_DIR}/ps"
  grep -qx -- "rm -f cid-1" "${FAKE_DIR}/rm"
}
