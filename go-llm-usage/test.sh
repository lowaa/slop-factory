#!/usr/bin/env bash
# Runs the Go test suite. Plain `go test`: nothing here needs Postgres,
# Docker or the network (the price generator's tests use a recorded
# response).
#
# GOWORK=off tests go-llm-usage against its own go.mod, as a library,
# rather than against whatever versions the repo workspace's other
# modules pull in.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The linters (dev-tools/lint/README.md), on the host.
../dev-tools/lint/go.sh .

GOWORK=off go vet ./...
GOWORK=off go test ./... "$@"
