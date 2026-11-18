bats_require_minimum_version 1.5.0

# Tests for run_remote_control.sh, the Remote Control launcher.
#
# Runs against a throwaway workspace ($ROOT) with stubbed token.sh, update.sh,
# install-app.sh and uv -- so nothing here needs a slop-haus server. What the
# host does once started (harness config, run.env, the harness check) is
# tested in the supervisor package.
#
#   bats/run.sh slop-haus/server/internal/workspace/assets/sh/run_remote_control.bats

setup() {
  bats_load_library repo_test

  ROOT="${BATS_TEST_TMPDIR}/workspace"
  mkdir -p "${ROOT}/.slop-haus"
  cp "${BATS_TEST_DIRNAME}/run_remote_control.sh" "${ROOT}/.slop-haus/run_remote_control.sh"
  RUN_REMOTE_CONTROL_SH="${ROOT}/.slop-haus/run_remote_control.sh"

  CALLER_DIR="${BATS_TEST_TMPDIR}/caller"
  mkdir -p "${CALLER_DIR}"

  STATE="${BATS_TEST_TMPDIR}/state"
  mkdir -p "${STATE}"
  export STATE

  stub_token_sh 0
  stub_updater
  stub_uv
  stub_app_installer
  printf '%s\n' 'https://slop-haus.example' > "${ROOT}/.slop-haus/server-url"
}

# stub_updater -- update.sh as run_remote_control.sh sees it: records each call and
# the SLOP_HAUS_UPDATE_LOCK_TIMEOUT and FROM_RUN_ENV it was given, and
# succeeds. A test changes what it does by writing ${STATE}/update.hook (a
# shell snippet it runs) or ${STATE}/update.exit.
stub_updater() {
  cat > "${ROOT}/.slop-haus/update.sh" << 'STUB'
#!/usr/bin/env bash
env | grep '^SLOP_HAUS_' > "${STATE}/update.env"
echo update >> "${STATE}/order"
echo "args=$* lock_timeout=${SLOP_HAUS_UPDATE_LOCK_TIMEOUT:-} from_run_env=${FROM_RUN_ENV:-}" >> "${STATE}/update.calls"
# A launcher that never stops updating would hang the suite: fail it instead.
if [ "$(wc -l < "${STATE}/update.calls")" -gt 40 ]; then
  echo "slop-haus: update loop" >&2
  exit 1
fi
[ -e "${STATE}/update.hook" ] && . "${STATE}/update.hook"
if [ -e "${STATE}/update.exit" ]; then
  echo "slop-haus: the updater's own explanation" >&2
  exit "$(cat "${STATE}/update.exit")"
fi
STUB
  chmod +x "${ROOT}/.slop-haus/update.sh"
}

update_calls() {
  [ -e "${STATE}/update.calls" ] && wc -l < "${STATE}/update.calls" || echo 0
}

# stub_token_sh <exit code> -- token.sh as run_remote_control.sh sees it: prints a token
# and succeeds, or explains itself on stderr and fails. Records each call.
stub_token_sh() {
  cat > "${ROOT}/.slop-haus/token.sh" << STUB
#!/usr/bin/env bash
echo "\$*" >> "\${STATE}/token.calls"
if [ "$1" != 0 ]; then
  echo "slop-haus: this workspace's sign-in has expired" >&2
  exit $1
fi
printf 'a-token'
STUB
  chmod +x "${ROOT}/.slop-haus/token.sh"
}

# stub_uv -- uv as run_remote_control.sh's Remote Control sees it: records how the host
# was started (what uv was told, the arguments the host got after --workspace,
# the directory and the environment) instead of starting it.
stub_uv() {
  local bin="${BATS_TEST_TMPDIR}/bin"
  mkdir -p "${bin}"
  cat > "${bin}/uv" << 'STUB'
#!/usr/bin/env bash
{
  echo "uv=$*"
  echo "cwd=$(pwd)"
  seen=0
  host_args=()
  while (($#)); do
    if ((seen)); then host_args+=("$1"); fi
    if [ "$1" = --workspace ]; then seen=1; shift; fi
    shift
  done
  (IFS='|'; echo "args=${host_args[*]}")
  env | sort | sed 's/^/env /'
} > "${STATE}/host.out"
pwd > "${STATE}/host.pwd"
echo started >> "${STATE}/host.starts"
printf '%s\n' "${host_args[@]}" > "${STATE}/host.args"
env > "${STATE}/host.env"
STUB
  chmod +x "${bin}/uv"
  [[ ":${PATH}:" == *":${bin}:"* ]] || PATH="${bin}:${PATH}"
}

# stub_app_installer -- install-app.sh as run_remote_control.sh sees it: records each
# call and leaves the package where the host is run from. A test changes what
# it does with ${STATE}/app.exit.
stub_app_installer() {
  cat > "${ROOT}/.slop-haus/install-app.sh" << 'STUB'
#!/usr/bin/env bash
echo "called" >> "${STATE}/app.calls"
echo install >> "${STATE}/order"
if [ -e "${STATE}/app.exit" ]; then
  echo "slop-haus: the installer's own explanation" >&2
  exit "$(cat "${STATE}/app.exit")"
fi
mkdir -p "${0%/*}/app"
: > "${0%/*}/app/pyproject.toml"
STUB
  chmod +x "${ROOT}/.slop-haus/install-app.sh"
  mkdir -p "${ROOT}/.slop-haus/app"
  : > "${ROOT}/.slop-haus/app/pyproject.toml"
}

app_calls() {
  [ -e "${STATE}/app.calls" ] && wc -l < "${STATE}/app.calls" || echo 0
}

# launch [VAR=value...] [-- arg...] -- runs run_remote_control.sh from $CALLER_DIR, never
# from $ROOT, so a launcher that forgot to cd into the workspace shows up.
# A leading -N is passed to bats' `run`, for a launch that must exit N: without it bats
# warns (BW01) on 127 and exits 1 for the whole file even though every test is ok.
launch() {
  local -a vars=() args=() runflags=()
  if [[ "${1:-}" =~ ^-[0-9]+$ ]]; then
    runflags=("$1")
    shift
  fi
  while (($#)); do
    if [[ "$1" == "--" ]]; then
      shift
      args=("$@")
      break
    fi
    vars+=("$1")
    shift
  done
  run "${runflags[@]}" env "${vars[@]}" bash -c 'cd "$1" && shift && exec "$@"' _ "${CALLER_DIR}" "${RUN_REMOTE_CONTROL_SH}" "${args[@]}" < /dev/null
}

host_env() {
  grep "^$1=" "${STATE}/host.env" | head -1 | cut -d= -f2-
}

@test "starts the package's host under uv, frozen, in the workspace, doing its own launch steps" {
  launch -- --idle-timeout 60
  assert_success
  assert_equal "$(grep -E '^(uv|cwd|args)=' "${STATE}/host.out")" "$(printf '%s\n' "uv=run --frozen --no-dev --project ${ROOT}/.slop-haus/app slop-haus-remote-control --workspace ${ROOT} --python-launch --idle-timeout 60" "cwd=${ROOT}" "args=--python-launch|--idle-timeout|60")"
}

@test "the harness and its arguments pass through untouched" {
  launch -- --harness pi --provider anthropic "two words"
  assert_success
  assert_equal "$(cat "${STATE}/host.args")" "$(printf '%s\n' --python-launch --harness pi --provider anthropic "two words")"
}

@test "a shell's leftover SLOP_HAUS_* settings never reach the host" {
  launch SLOP_HAUS_TOKEN=dead-static-token SLOP_HAUS_URL=http://another-env.example
  assert_success
  run grep '^SLOP_HAUS_' "${STATE}/host.env"
  assert_failure
}

@test "the shell's PYTHONPATH and PYTHONHOME do not reach the host" {
  launch PYTHONPATH=/tmp/evil PYTHONHOME=/tmp/evil
  assert_success
  run grep -E '^PYTHON(PATH|HOME)=' "${STATE}/host.env"
  assert_failure
}

@test "a start updates the workspace, then installs the package, once, and with no arguments" {
  launch
  assert_success
  assert_equal "$(cat "${STATE}/update.calls")" "args= lock_timeout= from_run_env="
  assert_equal "$(app_calls)" 1
  assert_equal "$(cat "${STATE}/order")" "$(printf '%s\n' update install)"
  assert [ -e "${STATE}/host.pwd" ]
}

@test "a missing installer stops the start" {
  rm "${ROOT}/.slop-haus/install-app.sh"
  launch
  assert_failure
  assert_output --partial "install-app.sh is missing"
  refute [ -e "${STATE}/host.pwd" ]
}

@test "a symlinked server-url is refused: the host would send the token to what it names" {
  mv "${ROOT}/.slop-haus/server-url" "${BATS_TEST_TMPDIR}/elsewhere-url"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere-url" "${ROOT}/.slop-haus/server-url"
  launch
  assert_failure
  assert_output --partial "is a symlink"
  refute [ -e "${STATE}/host.pwd" ]
}

@test "a launcher from before the shim passes its marker: no second update, and the host never sees it" {
  launch -- --slop-haus-updated --resume x
  assert_success
  assert_equal "$(update_calls)" 0
  assert_equal "$(app_calls)" 1
  assert_equal "$(cat "${STATE}/host.args")" "$(printf '%s\n' --python-launch --resume x)"
}

@test "an updater that fails stops the start, says why, and never starts the host" {
  echo 1 > "${STATE}/update.exit"
  launch
  assert_failure
  assert_output --partial "the updater's own explanation"
  assert_output --partial "not starting"
  assert_output --partial "--no-update"
  assert_equal "$(app_calls)" 0
  refute [ -e "${STATE}/host.pwd" ]
}

@test "a missing updater stops the start" {
  rm "${ROOT}/.slop-haus/update.sh"
  launch
  assert_failure
  assert_output --partial "update.sh is missing"
  refute [ -e "${STATE}/host.pwd" ]
}

@test "a symlinked updater is refused, and not run" {
  mv "${ROOT}/.slop-haus/update.sh" "${BATS_TEST_TMPDIR}/elsewhere.sh"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere.sh" "${ROOT}/.slop-haus/update.sh"
  launch
  assert_failure
  assert_output --partial "is a symlink"
  assert_equal "$(update_calls)" 0
}

@test "a package that could not be installed stops the start, and says what to do" {
  echo 1 > "${STATE}/app.exit"
  launch
  assert_failure
  assert_output --partial "the installer's own explanation"
  assert_output --partial "not starting"
  refute [ -e "${STATE}/host.pwd" ]
}

@test "a symlinked token.sh is refused before it runs" {
  mv "${ROOT}/.slop-haus/token.sh" "${BATS_TEST_TMPDIR}/elsewhere.sh"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere.sh" "${ROOT}/.slop-haus/token.sh"
  launch
  assert_failure
  assert_output --partial "is a symlink"
  refute [ -e "${STATE}/token.calls" ]
  refute [ -e "${STATE}/host.pwd" ]
}

@test "dead credentials stop the start before any update, and say how to fix it" {
  stub_token_sh 1
  launch
  assert_failure
  assert_output --partial "token.sh login"
  assert_equal "$(update_calls)" 0
  refute [ -e "${STATE}/host.pwd" ]
}

@test "--no-update warns, neither updates nor installs, starts, and is not passed to the host" {
  launch -- --no-update --resume x
  assert_success
  assert_output --partial "may be out of date"
  assert_equal "$(update_calls)" 0
  assert_equal "$(app_calls)" 0
  assert_equal "$(cat "${STATE}/host.args")" "$(printf '%s\n' --python-launch --resume x)"
}

@test "a missing package is reported, with how to fix it" {
  rm -rf "${ROOT}/.slop-haus/app"
  launch -- --no-update
  assert_failure
  assert_output --partial "is missing"
  assert_output --partial "not with --no-update"
  refute [ -e "${STATE}/host.pwd" ]
}

@test "a linked package is refused: it would run with this workspace's credentials in reach" {
  mv "${ROOT}/.slop-haus/app" "${BATS_TEST_TMPDIR}/elsewhere-app"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere-app" "${ROOT}/.slop-haus/app"
  launch -- --no-update
  assert_failure
  assert_output --partial "is a symlink"
  refute [ -e "${STATE}/host.pwd" ]
}

@test "uv is required, and its absence is said before anything starts" {
  rm "${BATS_TEST_TMPDIR}/bin/uv"
  local bin="${BATS_TEST_TMPDIR}/nouv" tool
  mkdir -p "${bin}"
  for tool in bash env cat dirname grep wc mkdir sort sed; do ln -sf "$(command -v "${tool}")" "${bin}/${tool}"; done
  PATH="${bin}" launch -127 -- --no-update
  assert_failure 127
  assert_output --partial "uv is required"
}

@test "an update lock timeout set for the launcher reaches the updater, which is run with SLOP_HAUS_* cleared" {
  launch SLOP_HAUS_UPDATE_LOCK_TIMEOUT=7 SLOP_HAUS_TOKEN=dead-static-token
  assert_success
  assert_equal "$(cat "${STATE}/update.calls")" "args= lock_timeout=7 from_run_env="
  assert_equal "$(cat "${STATE}/update.env")" "SLOP_HAUS_UPDATE_LOCK_TIMEOUT=7"
}
