bats_require_minimum_version 1.5.0

# Tests for supervisor-setup.sh, the body of the supervisor setup script.
#
# The four variables SupervisorScript puts above it are written by hand
# here, around a tiny stand-in package whose __main__ reports what it was
# asked to do. uv is a stub that runs that package with python3, so the
# test covers what the script decides (uv check, tenant suggestion, arguments,
# unpacking) and not uv itself.
#
#   bats/run.sh slop-haus/server/internal/workspace/assets/sh/supervisor-setup.bats

setup() {
  bats_load_library repo_test

  PKG="${BATS_TEST_TMPDIR}/pkg"
  mkdir -p "${PKG}/src/slop_haus_supervisor"
  : > "${PKG}/pyproject.toml"
  cat > "${PKG}/src/slop_haus_supervisor/__main__.py" << 'PY'
import sys
print("ARGS", *sys.argv[1:])
print("FROM", __file__)
PY
  tar -czf "${BATS_TEST_TMPDIR}/package.tar.gz" -C "${PKG}" .

  BIN="${BATS_TEST_TMPDIR}/bin"
  mkdir -p "${BIN}"
  # Skips `run --no-project --python SPEC` and runs the rest with python3.
  cat > "${BIN}/uv" << 'UV'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${UV_CALLS}"
shift 5
exec python3 "$@"
UV
  chmod +x "${BIN}/uv"
  UV_CALLS="${BATS_TEST_TMPDIR}/uv.calls"
  export UV_CALLS
  : > "${UV_CALLS}"

  render "https://slop-haus.example.com" "tenant-uuid"
}

# render <server> <tenant id> [tenant name] -- the script as SupervisorScript lays it out.
render() {
  SCRIPT="${BATS_TEST_TMPDIR}/setup.sh"
  {
    echo "server='$1'"
    echo "tenant_id='$2'"
    echo "tenant_name='${3:-}'"
    echo "archive_b64='$(base64 -w0 "${BATS_TEST_TMPDIR}/package.tar.gz")'"
    cat "${BATS_TEST_DIRNAME}/supervisor-setup.sh"
  } > "${SCRIPT}"
}

# run_script [args] -- the script with the stub uv first on PATH.
run_script() {
  PATH="${BIN}:${PATH}" run bash "${SCRIPT}" "$@"
}

@test "stops with a clear message when uv is missing, before writing anything" {
  rm "${BIN}/uv"
  # A PATH of the tools the script needs and nothing else, so a uv installed
  # on this machine cannot be found.
  mkdir "${BATS_TEST_TMPDIR}/tools"
  for tool in bash mktemp rm base64 tar cat printf; do
    ln -s "$(command -v "${tool}")" "${BATS_TEST_TMPDIR}/tools/${tool}"
  done

  PATH="${BATS_TEST_TMPDIR}/tools" run "$(command -v bash)" "${SCRIPT}" --root "${BATS_TEST_TMPDIR}/root"

  assert_failure 1
  assert_output --partial "uv is required"
  assert_output --partial "docs.astral.sh/uv"
  [ ! -e "${BATS_TEST_TMPDIR}/root" ]
}

@test "hands the server, tenant id, tenant and package to init" {
  run_script --root "${BATS_TEST_TMPDIR}/root" --tenant acme

  assert_success
  assert_output --partial "ARGS init --server https://slop-haus.example.com --tenant-id tenant-uuid --tenant acme --package"
  assert_output --partial "--harness claude --variant  --root ${BATS_TEST_TMPDIR}/root"
}

@test "with no tenant name from the server the suggestion is the first label of its host" {
  render "http://localhost:8190" "t"
  run_script
  assert_success
  assert_output --partial "--default-tenant localhost "

  render "https://slop-haus.example.com:8443/sub" "t"
  run_script
  assert_output --partial "--default-tenant slop-haus "
}

@test "the server's tenant name is suggested, and an explicit --tenant replaces the suggestion" {
  render "https://slop-haus.example.com" "t" "acme-corp"
  run_script
  assert_output --partial "--tenant-id t --default-tenant acme-corp --package"
  refute_output --partial " --tenant "

  run_script --tenant mine
  assert_output --partial "--tenant-id t --tenant mine --package"
  refute_output --partial "--default-tenant"
}

@test "init runs from an unpacked copy of the package, under uv with no project" {
  run_script
  assert_success
  assert_output --regexp "FROM /.*/bootstrap/src/slop_haus_supervisor/__main__.py"
  [[ "$(cat "${UV_CALLS}")" == "run --no-project --python >=3.12 python -m slop_haus_supervisor init "* ]]
}

@test "scratch files are removed afterwards" {
  TMPDIR="${BATS_TEST_TMPDIR}/scratch"
  mkdir "${TMPDIR}"
  export TMPDIR
  run_script
  assert_success
  [ -z "$(ls -A "${TMPDIR}")" ]
}

@test "scratch files are removed even when TMPDIR has a quote in it" {
  TMPDIR="${BATS_TEST_TMPDIR}/it's here"
  mkdir "${TMPDIR}"
  export TMPDIR
  run_script
  assert_success
  [ -z "$(ls -A "${TMPDIR}")" ]
}

@test "a flag without its value, and an unknown flag, are usage errors" {
  run_script --root
  assert_failure 2
  run_script --nonsense
  assert_failure 2
  assert_output --partial "usage:"
  [ ! -s "${UV_CALLS}" ]
}

@test "the next steps lead with the guided setup and start, not the individual commands" {
  run_script --tenant acme
  assert_output --partial "slop-haus setup"
  assert_output --partial "slop-haus start"
  assert_output --partial "slop-haus status"
  refute_output --partial "slop-haus slot add"
}
