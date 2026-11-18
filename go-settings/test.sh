#!/usr/bin/env bash
# Runs the Go test suite inside a container. The tests start their own
# Postgres containers with testcontainers (see ../go-db/dbtest), so the
# container gets the host's Docker socket (Docker-outside-of-Docker) and the
# host network to reach those sibling containers' mapped ports.
#
# The source is baked into the image rather than mounted, so this runs
# anywhere a Docker daemon is, a CI step included. The build context is the
# repo root: go-settings builds against go-db through go.work.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# The linters (dev-tools/lint/README.md), on the host: they need no database.
dev-tools/lint/go.sh go-settings

# Run by the ID this build produced, not a tag: a concurrent run on the same
# daemon would retag a shared name between the build and the run.
IIDFILE="$(mktemp)"
trap 'rm -f "${IIDFILE}"' EXIT
docker build --target test --iidfile "${IIDFILE}" -f go-settings/Dockerfile .

docker run --rm \
  --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  "$(cat "${IIDFILE}")" \
  bash -c 'go vet ./... && exec go test ./... "$@"' bash "$@"
