#!/usr/bin/env bash
# Runs slop-bot's lint and Go tests: what CI runs. Extra arguments go to
# `go test`, e.g. `./test.sh -run TestScheduler ./internal/scheduler`.
# It builds in the repo's go.work, because it depends on sibling modules
# (go-clock) that are not tagged releases.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The linters and the deadcode check (dev-tools/lint/README.md).
../dev-tools/lint/go.sh .

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
