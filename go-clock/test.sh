#!/usr/bin/env bash
# Runs the Go test suite: plain `go test`, with nothing here needing Docker or
# the network. GOWORK=off tests it against its own go.mod, as a library,
# rather than against whatever versions the repo workspace pulls in.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The linters (dev-tools/lint/README.md), on the host.
../dev-tools/lint/go.sh .

export GOWORK=off
go vet ./...
go test -race ./... "$@"
