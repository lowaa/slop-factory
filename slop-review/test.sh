#!/usr/bin/env bash
# Runs the reviewer's Go tests and lint (what CI runs). Plain Go with no
# Docker: it builds in the repo's root go.work, because it depends
# on slop-review/report and go-llm-usage, which are not tagged releases.
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
go test -race ./... "$@"
# review.sh itself, with go stubbed.
"$(git rev-parse --show-toplevel)/bats/run.sh" review.bats
