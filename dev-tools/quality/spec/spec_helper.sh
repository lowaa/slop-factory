# shellcheck shell=bash
# Loaded by shellspec before every spec (.shellspec's --require).

# QUALITY_REAL_ROOT is the repo the specs run in, for the specs about the
# real registry; the lib's own QUALITY_ROOT is pointed at a scratch repo.
QUALITY_REAL_ROOT="$(cd "${SHELLSPEC_PROJECT_ROOT}/../.." && pwd)"
export QUALITY_REAL_ROOT

# stub_setup: a scratch repo with a project in it, and stubs for `go` and
# the quality CLI on PATH. Each stub appends its arguments, one call a
# line, to $STUB_LOG, and does what the STUB_* variables say:
#   STUB_GO_PACKAGES   what `go list` prints (default two packages)
#   STUB_GO_TEST_EXIT  what `go test` exits with (default 0)
#   STUB_QUALITY_EXIT  what the quality CLI exits with (default 0)
stub_setup() {
  SCRATCH="$(mktemp -d)"
  mkdir -p "${SCRATCH}/repo/proj/sub" "${SCRATCH}/bin"
  STUB_LOG="${SCRATCH}/log"
  : > "${STUB_LOG}"
  export STUB_LOG STUB_GO_PACKAGES="example.com/proj
example.com/proj/internal/x" STUB_GO_TEST_EXIT=0 STUB_QUALITY_EXIT=0

  cat > "${SCRATCH}/bin/go" << 'EOF'
#!/usr/bin/env bash
echo "go $*" >> "${STUB_LOG}"
case "$1" in
  list) [[ -z "${STUB_GO_PACKAGES}" ]] || printf '%s\n' "${STUB_GO_PACKAGES}" ;;
  test)
    for a in "$@"; do
      case "${a}" in -coverprofile=*) echo "mode: atomic" > "${a#*=}" ;; esac
    done
    echo '{"Action":"pass"}'
    exit "${STUB_GO_TEST_EXIT}"
    ;;
esac
EOF
  cat > "${SCRATCH}/bin/quality" << 'EOF'
#!/usr/bin/env bash
echo "quality $*" >> "${STUB_LOG}"
exit "${STUB_QUALITY_EXIT}"
EOF
  chmod +x "${SCRATCH}/bin/go" "${SCRATCH}/bin/quality"
  PATH="${SCRATCH}/bin:${PATH}"
  export QUALITY_BIN="${SCRATCH}/bin/quality"
  # The lib's globals, as if it lived in the scratch repo.
  # shellcheck disable=SC2034 # quality_lib.sh reads it
  QUALITY_ROOT="${SCRATCH}/repo"
  # shellcheck disable=SC2034 # quality_lib.sh reads it
  QUALITY_REPORT_DIR="${SCRATCH}/reports"
}

stub_cleanup() {
  rm -rf "${SCRATCH}"
}

# calls PREFIX: the logged calls that start with PREFIX.
calls() {
  grep -e "^$1" "${STUB_LOG}" || true
}
