#!/usr/bin/env bash
# Runs the Go test suite inside a container. The tests themselves spin up
# their own Postgres containers via testcontainers-go, so this container
# needs the host's Docker socket mounted (Docker-outside-of-Docker) rather
# than needing Docker installed inside the image, and --network host so it
# can reach those sibling Postgres containers' mapped ports the same way a
# host-run `go test` would.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# The linters (dev-tools/lint/README.md), on the host: the Go ones, and ruff
# over the e2e suite's Python, which CI does not run but lints here.
../dev-tools/lint/go.sh .
../dev-tools/lint/ruff.sh e2e_test

docker run --rm \
  --network host \
  -v "$(pwd)":/srv \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -w /srv \
  golang:1.27-bookworm \
  go test ./... "$@"
