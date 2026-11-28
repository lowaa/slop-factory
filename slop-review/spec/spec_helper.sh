# shellcheck shell=bash
# Loaded by shellspec before every spec (.shellspec's --require).

# REVIEW_SH is the script under test, copied into scratch checkouts.
REVIEW_SH="${SHELLSPEC_PROJECT_ROOT}/review.sh"
export REVIEW_SH

# checkout_setup: a scratch git checkout ($MAIN) with review.sh at
# slop-review/review.sh, a linked worktree of it ($LINKED), and a `go` stub
# on PATH that writes its arguments, one a line, to $GO_ARGS.
checkout_setup() {
  SCRATCH="$(cd "$(mktemp -d)" && pwd -P)"
  MAIN="${SCRATCH}/main"
  LINKED="${SCRATCH}/linked"
  GO_ARGS="${SCRATCH}/go.args"
  export GO_ARGS
  mkdir -p "${MAIN}/slop-review" "${SCRATCH}/bin"
  cp "${REVIEW_SH}" "${MAIN}/slop-review/review.sh"
  printf '.env\n' > "${MAIN}/.gitignore"
  git -C "${MAIN}" init -q -b main
  git -C "${MAIN}" add .
  git -C "${MAIN}" -c user.name=spec -c user.email=spec@example.com commit -q -m init
  git -C "${MAIN}" worktree add -q "${LINKED}"

  cat > "${SCRATCH}/bin/go" << 'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" > "${GO_ARGS}"
EOF
  chmod +x "${SCRATCH}/bin/go"
  PATH="${SCRATCH}/bin:${PATH}"
}

checkout_cleanup() {
  rm -rf "${SCRATCH}"
}

# go_args: what review.sh ran `go` with.
go_args() {
  cat "${GO_ARGS}"
}

# env_file_args: just its --env-file arguments.
env_file_args() {
  grep -e '^--env-file' "${GO_ARGS}" || true
}
