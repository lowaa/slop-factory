bats_require_minimum_version 1.5.0

# Tests for install-app.sh.
#
# A throwaway workspace ($ROOT) with its own copy of install-app.sh, a stub
# token.sh and a stub curl that answers with a canned package (a real tarball)
# and status, so nothing reaches a server. What the host does with the package
# is the supervisor's own tests.
#
#   bats/run.sh slop-haus/server/internal/workspace/assets/sh/install-app.bats

setup() {
  bats_load_library repo_test

  ROOT="${BATS_TEST_TMPDIR}/workspace"
  mkdir -p "${ROOT}/.slop-haus"
  cp "${BATS_TEST_DIRNAME}/install-app.sh" "${ROOT}/.slop-haus/install-app.sh"
  INSTALL="${ROOT}/.slop-haus/install-app.sh"
  APP="${ROOT}/.slop-haus/app"
  echo "http://fake-slop-haus.example" > "${ROOT}/.slop-haus/server-url"

  cat > "${ROOT}/.slop-haus/token.sh" << 'STUB'
#!/usr/bin/env bash
printf '%s' test-agent-token
STUB
  chmod +x "${ROOT}/.slop-haus/token.sh"

  CURL_CALLS="${BATS_TEST_TMPDIR}/curl.calls"
  CURL_STDIN="${BATS_TEST_TMPDIR}/curl.stdin"
  : > "${CURL_CALLS}"
  make_package v1 abc123
  stub_curl

  CALLER_DIR="${BATS_TEST_TMPDIR}/caller"
  mkdir -p "${CALLER_DIR}"
  TMPDIR="${BATS_TEST_TMPDIR}/tmp"
  mkdir -p "${TMPDIR}"
  export TMPDIR
}

# make_package <marker> <version> -- the package curl will serve: a pyproject.toml
# and a module naming <marker>, at <version>.
make_package() {
  local pkg="${BATS_TEST_TMPDIR}/pkg"
  rm -rf "${pkg}"
  mkdir -p "${pkg}/src/slop_haus_supervisor"
  : > "${pkg}/pyproject.toml"
  echo "$1" > "${pkg}/src/slop_haus_supervisor/marker"
  tar -czf "${BATS_TEST_TMPDIR}/package.tar.gz" -C "${pkg}" .
  VERSION="$2"
}

stub_curl() {
  local bin="${BATS_TEST_TMPDIR}/bin"
  mkdir -p "${bin}"
  cat > "${bin}/curl" << STUB
#!/usr/bin/env bash
echo "\$*" >> $(printf %q "${CURL_CALLS}")
if [[ -n "\${CURL_FAIL:-}" ]]; then exit 7; fi
prev=""; out=""; headers=""
for arg in "\$@"; do
  [[ "\$prev" == "-o" ]] && out="\$arg"
  [[ "\$prev" == "-D" ]] && headers="\$arg"
  [[ "\$prev" == "-H" && "\$arg" == "@-" ]] && cat > $(printf %q "${CURL_STDIN}")
  prev="\$arg"
done
cp $(printf %q "${BATS_TEST_TMPDIR}/package.tar.gz") "\$out"
printf 'HTTP/1.1 %s\r\n' "\${CURL_STATUS:-200}" > "\$headers"
if [[ -z "\${CURL_NO_VERSION:-}" ]]; then
  printf 'X-Supervisor-Version: %s\r\n' "\${CURL_VERSION:-${VERSION}}" >> "\$headers"
fi
printf '%s' "\${CURL_STATUS:-200}"
STUB
  chmod +x "${bin}/curl"
  [[ ":${PATH}:" == *":${bin}:"* ]] || PATH="${bin}:${PATH}"
}

install_app() {
  run env "$@" bash -c 'cd "$1" && shift && exec "$@"' _ "${CALLER_DIR}" "${INSTALL}" < /dev/null
}

@test "installs the served package into .slop-haus/app and records its version" {
  install_app SLOP_HAUS_URL=
  assert_success
  assert_equal "$(cat "${APP}/src/slop_haus_supervisor/marker")" "v1"
  assert_equal "$(cat "${ROOT}/.slop-haus/app.version")" "abc123"
  assert_equal "$(cat "${CURL_CALLS}" | grep -c 'http://fake-slop-haus.example/api/supervisor/package')" 1
}

@test "asks as this agent, with the token on stdin and not in the arguments" {
  install_app SLOP_HAUS_URL=
  assert_success
  assert_equal "$(cat "${CURL_STDIN}")" "Authorization: Bearer test-agent-token"
  run ! grep -q test-agent-token "${CURL_CALLS}"
}

@test "a package at the version already installed is left alone" {
  install_app SLOP_HAUS_URL=
  assert_success
  echo "local edit" > "${APP}/src/slop_haus_supervisor/marker"
  install_app SLOP_HAUS_URL=
  assert_success
  assert_equal "$(cat "${APP}/src/slop_haus_supervisor/marker")" "local edit"
}

@test "a newer version replaces the old tree whole" {
  install_app SLOP_HAUS_URL=
  touch "${APP}/stale-file"
  make_package v2 def456
  install_app CURL_VERSION=def456
  assert_success
  assert_equal "$(cat "${APP}/src/slop_haus_supervisor/marker")" "v2"
  assert [ ! -e "${APP}/stale-file" ]
  assert_equal "$(cat "${ROOT}/.slop-haus/app.version")" "def456"
  assert [ ! -e "${ROOT}/.slop-haus/.app-old" ]
}

@test "an install that finds no app directory installs even at the recorded version" {
  install_app SLOP_HAUS_URL=
  rm -r "${APP}"
  install_app SLOP_HAUS_URL=
  assert_success
  assert [ -f "${APP}/pyproject.toml" ]
}

@test "the single-file host an older update wrote is removed" {
  echo old > "${ROOT}/.slop-haus/conductor-host.py"
  install_app SLOP_HAUS_URL=
  assert_success
  assert [ ! -e "${ROOT}/.slop-haus/conductor-host.py" ]
}

@test "a server that ships no package is reported, and nothing is installed" {
  install_app CURL_STATUS=404
  assert_failure
  assert_output --partial "does not ship the Remote Control host"
  assert [ ! -e "${APP}" ]
}

@test "a rejected token is reported" {
  install_app CURL_STATUS=401
  assert_failure
  assert_output --partial "rejected this agent's token"
}

@test "an unreachable server is reported, and the installed package is kept" {
  install_app SLOP_HAUS_URL=
  install_app CURL_FAIL=1
  assert_failure
  assert_output --partial "could not reach"
  assert [ -f "${APP}/pyproject.toml" ]
}

@test "a reply with no version is not a package" {
  install_app CURL_NO_VERSION=1
  assert_failure
  assert_output --partial "no version"
  assert [ ! -e "${APP}" ]
}

@test "a version that is not a bare digest is refused" {
  install_app 'CURL_VERSION=../x'
  assert_failure
  assert_output --partial "unusable version"
  assert [ ! -e "${APP}" ]
}

@test "an archive with a link in it is refused, and the installed package kept" {
  install_app SLOP_HAUS_URL=
  local pkg="${BATS_TEST_TMPDIR}/evil"
  mkdir -p "${pkg}"
  : > "${pkg}/pyproject.toml"
  ln -s /etc "${pkg}/link"
  tar -czf "${BATS_TEST_TMPDIR}/package.tar.gz" -C "${pkg}" .
  install_app CURL_VERSION=bad000
  assert_failure
  assert_output --partial "something other than files and directories"
  assert_equal "$(cat "${APP}/src/slop_haus_supervisor/marker")" "v1"
}

# A listing far longer than a pipe's buffer, with the bad entry first: grep
# has its answer at once, so a `tar | grep -q` check under pipefail sees tar
# die of SIGPIPE and lets the package through. These hold whatever the timing.
big_package() {
  local pkg="${BATS_TEST_TMPDIR}/big" i
  mkdir -p "${pkg}"
  : > "${pkg}/pyproject.toml"
  for ((i = 0; i < 3000; i++)); do : > "${pkg}/file-with-a-longish-name-${i}"; done
  echo "${pkg}"
}

@test "an archive whose first entry is a link is refused however long its listing" {
  install_app SLOP_HAUS_URL=
  local pkg
  pkg="$(big_package)"
  ln -s /etc "${pkg}/aaa-link"
  (cd "${pkg}" && tar -czf "${BATS_TEST_TMPDIR}/package.tar.gz" aaa-link pyproject.toml file-with-a-longish-name-*)
  install_app CURL_VERSION=bad001
  assert_failure
  assert_output --partial "something other than files and directories"
  assert_equal "$(cat "${APP}/src/slop_haus_supervisor/marker")" "v1"
}

@test "an archive whose first entry leaves it is refused however long its listing" {
  install_app SLOP_HAUS_URL=
  local pkg
  pkg="$(big_package)"
  mkdir -p "${BATS_TEST_TMPDIR}/outside"
  : > "${BATS_TEST_TMPDIR}/outside/escape"
  # -P keeps the ../: tar strips it from member names otherwise.
  (cd "${pkg}" && tar -czPf "${BATS_TEST_TMPDIR}/package.tar.gz" ../outside/escape pyproject.toml file-with-a-longish-name-*)
  install_app CURL_VERSION=bad002
  assert_failure
  assert_output --partial "names a path outside itself"
  assert_equal "$(cat "${APP}/src/slop_haus_supervisor/marker")" "v1"
}

@test "an archive naming a path outside itself is refused" {
  python3 - "${BATS_TEST_TMPDIR}/package.tar.gz" << 'PY'
import io, sys, tarfile
with tarfile.open(sys.argv[1], "w:gz") as tar:
    for name in ("pyproject.toml", "../escape"):
        info = tarfile.TarInfo(name)
        tar.addfile(info, io.BytesIO(b""))
PY
  install_app SLOP_HAUS_URL=
  assert_failure
  assert_output --partial "outside itself"
  assert [ ! -e "${APP}" ]
}

@test "an archive that is not a package is refused" {
  local pkg="${BATS_TEST_TMPDIR}/empty"
  mkdir -p "${pkg}"
  echo hi > "${pkg}/README"
  tar -czf "${BATS_TEST_TMPDIR}/package.tar.gz" -C "${pkg}" .
  install_app SLOP_HAUS_URL=
  assert_failure
  assert_output --partial "could not unpack"
  assert [ ! -e "${APP}" ]
}

@test "a linked .slop-haus/app is refused" {
  mkdir "${BATS_TEST_TMPDIR}/elsewhere"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere" "${APP}"
  install_app SLOP_HAUS_URL=
  assert_failure
  assert_output --partial "is a symlink"
  assert [ ! -s "${CURL_CALLS}" ]
}

@test "a missing token.sh stops it before any request" {
  rm "${ROOT}/.slop-haus/token.sh"
  install_app SLOP_HAUS_URL=
  assert_failure
  assert_output --partial "token.sh is missing"
  assert [ ! -s "${CURL_CALLS}" ]
}

@test "a second install waits for the lock, and gives up with a message when it is held too long" {
  install_app SLOP_HAUS_URL=
  flock "${ROOT}/.slop-haus/update.lock" sleep 5 &
  holder=$!
  sleep 0.3
  install_app SLOP_HAUS_UPDATE_LOCK_TIMEOUT=1
  kill "${holder}" 2> /dev/null || true
  assert_failure
  assert_output --partial "timed out after 1s waiting for another update"
}

@test "a linked update.lock is refused" {
  mkdir "${BATS_TEST_TMPDIR}/elsewhere"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere/lock" "${ROOT}/.slop-haus/update.lock"
  install_app SLOP_HAUS_URL=
  assert_failure
  assert_output --partial "is a symlink"
  assert [ ! -e "${BATS_TEST_TMPDIR}/elsewhere/lock" ]
}

@test "an install interrupted between its two moves is put back before anything else" {
  install_app SLOP_HAUS_URL=
  mv "${APP}" "${ROOT}/.slop-haus/.app-old"
  install_app CURL_FAIL=1
  assert_failure
  assert [ -f "${APP}/pyproject.toml" ]
}
