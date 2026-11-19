# Tests for lib.sh's install_dir: how go.sh puts its tool directory in place
# when another run may already have.
#
#   bats/run.sh dev-tools/lint/lib.bats   # from the repo root

setup() {
  bats_load_library repo_test
  # shellcheck source=SCRIPTDIR/lib.sh
  . "${BATS_TEST_DIRNAME}/lib.sh"
  CACHE="${BATS_TEST_TMPDIR}/cache"
  mkdir -p "${CACHE}"
  DIR="${CACHE}/lint-go-key"
  TMP="${CACHE}/lint-go-key.AbC123"
  mkdir "${TMP}"
  touch "${TMP}/.built"
  echo ours > "${TMP}/tool"
}

@test "install_dir moves the build into place when nobody got there first" {
  install_dir "${TMP}" "${DIR}"
  assert [ -f "${DIR}/.built" ]
  assert_equal "$(cat "${DIR}/tool")" ours
  assert [ ! -e "${TMP}" ]
}

@test "install_dir keeps another run's copy, drops ours, and leaves nothing nested" {
  mkdir "${DIR}"
  touch "${DIR}/.built"
  echo theirs > "${DIR}/tool"
  install_dir "${TMP}" "${DIR}"
  assert_equal "$(cat "${DIR}/tool")" theirs
  assert [ -f "${DIR}/.built" ]
  assert [ ! -e "${TMP}" ]
  assert_equal "$(ls -A "${DIR}")" ".built
tool"
}

@test "install_dir with a trailing slash on the build still keeps another run's copy" {
  mkdir "${DIR}"
  touch "${DIR}/.built"
  echo theirs > "${DIR}/tool"
  install_dir "${TMP}/" "${DIR}"
  assert_equal "$(cat "${DIR}/tool")" theirs
  assert [ -f "${DIR}/.built" ]
  assert [ ! -e "${TMP}" ]
}
