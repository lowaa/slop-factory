#!/usr/bin/env bash
# Runs the run-report contract's tests: plain Go, no Docker or network. It
# builds in the repo's root go.work, since it depends on go-llm-usage.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The linters (dev-tools/lint/README.md).
../../dev-tools/lint/go.sh .

unformatted="$(gofmt -l .)"
if [[ -n ${unformatted} ]]; then
  echo "gofmt needed:" >&2
  echo "${unformatted}" >&2
  exit 1
fi
go vet ./...
go test -race ./... "$@"
