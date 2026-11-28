#!/usr/bin/env bash
# Runs slop-bot's lint and Go tests: what CI runs. Extra arguments go to
# `go test`, e.g. `./test.sh -run TestScheduler ./internal/agents`.
# It builds in the repo's go.work, as the repo's other apps do: sibling
# modules resolve there, not through tagged releases.
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
# The race detector sleeps a second at every process exit by default; the
# suite re-runs its own test binary as fake agents and hooks, so each pays it.
export GORACE="${GORACE:-atexit_sleep_ms=0}"
if (($#)); then
  go test -race "$@"
else
  go test -race ./...
fi
