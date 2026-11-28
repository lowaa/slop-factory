# shellcheck shell=bash
# Loaded by shellspec before every spec (.shellspec's --require).

# stub_setup: a scratch repo with a Go project in it, and stubs for `go`,
# gremlins, the mutation CLI and timeout(1) on PATH. Each stub appends its
# arguments, one call a line, to $STUB_LOG, and does what the STUB_*
# variables say:
#   STUB_GO_DIRS         what `go list -f {{.Dir}} PATTERN` prints (default: two packages)
#   STUB_GO_NAME         the package name `go list` gives every dir (default: its base name)
#   STUB_GO_LIST_EXIT    what `go list` exits with (default 0)
#   STUB_GO_BUILD_EXIT   what `go build` exits with (default 0)
#   STUB_GREMLINS_EXIT   what gremlins exits with (default 0)
#   STUB_GREMLINS_WRITE  0: gremlins writes no report file (default 1)
#   STUB_MUTATION_EXIT   what the mutation CLI exits with (default 0)
stub_setup() {
  SCRATCH="$(cd "$(mktemp -d)" && pwd -P)"
  mkdir -p "${SCRATCH}/repo/proj/mutation_baseline" "${SCRATCH}/bin"
  : > "${SCRATCH}/repo/proj/go.mod"
  STUB_LOG="${SCRATCH}/log"
  : > "${STUB_LOG}"
  export STUB_LOG STUB_GO_LIST_EXIT=0 STUB_GO_BUILD_EXIT=0 STUB_GREMLINS_EXIT=0 \
    STUB_GREMLINS_WRITE=1 STUB_GREMLINS_SAY='' STUB_GO_NAME='' STUB_MUTATION_EXIT=0 STUB_PROJECT="${SCRATCH}/repo/proj"
  STUB_GO_DIRS="${STUB_PROJECT}/internal/x
${STUB_PROJECT}/internal/y"
  export STUB_GO_DIRS

  cat > "${SCRATCH}/bin/go" << 'EOF'
#!/usr/bin/env bash
echo "go $*" >> "${STUB_LOG}"
case "$1" in
  env) echo "${STUB_PROJECT}/go.mod" ;;
  list)
    ((STUB_GO_LIST_EXIT == 0)) || exit "${STUB_GO_LIST_EXIT}"
    while IFS= read -r d; do
      echo "${STUB_GO_NAME:-$(basename "${d}")} ${d}"
    done <<< "${STUB_GO_DIRS}"
    ;;
  build)
    echo "GOWORK=${GOWORK-unset}" >> "${STUB_LOG}"
    ((STUB_GO_BUILD_EXIT == 0)) || exit "${STUB_GO_BUILD_EXIT}"
    while (($#)); do
      if [[ "$1" == -o ]]; then
        case "$2" in
          */gremlins) cp "${STUB_LOG%/log}/bin/gremlins_stub" "$2" ;;
          */mutation) cp "${STUB_LOG%/log}/bin/mutation_stub" "$2" ;;
        esac
      fi
      shift
    done
    ;;
esac
EOF
  cat > "${SCRATCH}/bin/gremlins_stub" << 'EOF'
#!/usr/bin/env bash
echo "gremlins $*" >> "${STUB_LOG}"
echo "gremlins-env GOWORK=${GOWORK-unset} GOFLAGS=${GOFLAGS-unset}" >> "${STUB_LOG}"
echo "gremlins output for the log ${STUB_GREMLINS_SAY:-}"
while (($#)); do
  [[ "$1" != -o ]] || { [[ "${STUB_GREMLINS_WRITE}" == 0 ]] || echo '{"files":[]}' > "$2"; }
  shift
done
exit "${STUB_GREMLINS_EXIT}"
EOF
  cat > "${SCRATCH}/bin/mutation_stub" << 'EOF'
#!/usr/bin/env bash
echo "mutation $*" >> "${STUB_LOG}"
exit "${STUB_MUTATION_EXIT}"
EOF
  cat > "${SCRATCH}/bin/timeout" << 'EOF'
#!/usr/bin/env bash
echo "timeout $1" >> "${STUB_LOG}"
shift
exec "$@"
EOF
  chmod +x "${SCRATCH}"/bin/*
  PATH="${SCRATCH}/bin:${PATH}"
  # The lib's globals, as if it lived in the scratch repo.
  # shellcheck disable=SC2034 # mutation_lib.sh reads it
  MUTATION_ROOT="${SCRATCH}/repo"
  export MUTATION_BIN="${SCRATCH}/bin/mutation_stub" MUTATION_GREMLINS_BIN="${SCRATCH}/bin/gremlins_stub"
  unset MUTATION_REPORT_DIR MUTATION_WORKERS MUTATION_TIMEOUT_COEFFICIENT MUTATION_PACKAGE_TIMEOUT
}

stub_cleanup() {
  rm -rf "${SCRATCH}"
}

# calls PREFIX: the logged calls that start with PREFIX.
calls() {
  grep -e "^$1" "${STUB_LOG}" || true
}
