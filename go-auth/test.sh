#!/usr/bin/env bash
# Runs the Go test suite. Unlike go-db's test.sh, go-auth's tests have no
# Postgres/testcontainers dependency, so this is a plain `go test` rather
# than a docker-run wrapper.
#
# GOWORK=off tests go-auth against its own go.mod, as a library, rather
# than against whatever versions the repo workspace's other modules
# pull in.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The linters (dev-tools/lint/README.md), on the host.
../dev-tools/lint/go.sh .

GOWORK=off go test ./... "$@"
