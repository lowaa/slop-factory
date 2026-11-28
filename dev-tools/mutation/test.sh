#!/usr/bin/env bash
# Runs the mutation tool's lint, Go tests and shellspec suite: what CI runs.
# Extra arguments go to `go test`, e.g. `./test.sh -run TestCompare ./internal/score`.
# GOWORK=off: the tool is tested as mutation_lib.sh builds it, against its
# own go.sum.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The linters (dev-tools/lint/README.md), on the host.
../../dev-tools/lint/go.sh .

export GOWORK=off
unformatted="$(gofmt -l .)"
if [[ -n ${unformatted} ]]; then
  echo "gofmt needed:" >&2
  echo "${unformatted}" >&2
  exit 1
fi
go vet ./...
if (($#)); then
  go test -race "$@"
else
  go test -race ./...
fi

command -v shellspec > /dev/null || {
  echo "test.sh: shellspec is not installed: see dev-tools/mutation/README.md" >&2
  exit 127
}
shellspec
