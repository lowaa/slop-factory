#!/usr/bin/env bash
# Runs the lint and the Go test suite: plain `go test`, with nothing here
# needing Docker or the network (the S3 tests mock the client). It builds
# in the repo's root go.work: the module keeps no go.sum of its own.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

../dev-tools/lint/go.sh .
go vet ./...
go test -race ./... "$@"
