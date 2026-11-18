#!/usr/bin/env bash
# docker compose up/down for docker-compose-dev.yaml -- see that file's
# top-of-file comment for what it stands up and how to use it once running.
# Meant for interactive local dev, not CI: e2e_playwright_ts/test.sh is the
# ephemeral, tears-itself-down-every-run equivalent for the Playwright
# suite.
#
# Usage: ./dev_env.sh up|down [extra docker compose args]
#   ./dev_env.sh up             start the stack (rebuilding images)
#   ./dev_env.sh down           stop it, keeping the data volume
#   ./dev_env.sh down -v        stop it and wipe the data volume too
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)
cd "$SCRIPT_DIR"

usage() {
  echo "usage: $(basename "${BASH_SOURCE[0]}") up|down [extra docker compose args]" >&2
  exit 1
}

[[ $# -ge 1 ]] || usage
action="$1"
shift
case "$action" in
  up | down) ;;
  *) usage ;;
esac

if ! command -v docker &> /dev/null; then
  echo "docker is required but not on PATH -- see https://docs.docker.com/get-docker/" >&2
  exit 1
fi

if ! docker compose version &> /dev/null; then
  echo "docker compose (the v2 CLI plugin) is required -- 'docker-compose' (v1, hyphenated) isn't enough" >&2
  exit 1
fi

if ! docker info &> /dev/null; then
  echo "docker daemon isn't reachable -- is Docker (Desktop/Engine) running?" >&2
  exit 1
fi

# POSTGRES_PASSWORD is the one variable docker-compose-dev.yaml requires
# with no default (${VAR:?...}). Compose interpolates the whole file for
# `down` as well as `up`, so both actions need it set -- export your own
# first if you want something other than this fixed dev value.
if [[ -z "${POSTGRES_PASSWORD:-}" ]]; then
  export POSTGRES_PASSWORD="devpassword"
  echo "POSTGRES_PASSWORD not set -- defaulting to '${POSTGRES_PASSWORD}' (local dev only, not a real secret)"
fi

if [[ "$action" == "up" ]]; then
  cat << INFO

Starting slop-haus dev environment. Once up:
  Board:    http://localhost:${FRONTEND_PORT:-5273}/
  API:      http://localhost:${API_PORT:-8190}/api/tickets
  MailHog:  http://localhost:${MAILHOG_PORT:-8126}/ (activation/password-reset links land here)

Sign in at the Board URL with the seeded dev account (see docker-compose-dev.yaml's
seed service; override with DEV_USER_EMAIL/DEV_USER_PASSWORD):
  email:    ${DEV_USER_EMAIL:-dev@slop-haus.local}
  password: ${DEV_USER_PASSWORD:-slop-haus-dev-password}

Ports are overridable -- to run a second slop-haus workspace beside this
one, give it its own FRONTEND_PORT/API_PORT/POSTGRES_PORT/MAILHOG_PORT.

INFO
  docker compose -f docker-compose-dev.yaml up --build "$@"
else
  echo "Stopping slop-haus dev environment..."
  docker compose -f docker-compose-dev.yaml down "$@"
fi
