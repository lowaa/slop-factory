#!/usr/bin/env bash
# Runs the dockerized Playwright e2e suite against the full dev stack
# (Postgres + demo-api), using the e2e overlay
# (docker-compose-dev.e2e.yaml) to add the e2e-tests service on top of
# ../docker-compose-dev.yaml. The stack (including volumes) is torn down on
# exit whether the tests pass or fail, and this script's exit code matches
# the e2e-tests container's (pytest's) exit code, so it's CI-friendly.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Ephemeral, docker-compose-only stack -- not a real secret -- so default it
# instead of forcing every local run to set it, but still let CI/devs
# override it if needed.
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-e2e_test_password}"

compose() {
  docker compose -f docker-compose-dev.yaml -f e2e_test/docker-compose-dev.e2e.yaml "$@"
}

cleanup() {
  compose down -v
}
trap cleanup EXIT

compose up --build --abort-on-container-exit --exit-code-from e2e-tests
