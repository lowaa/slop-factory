#!/usr/bin/env bash
# Runs the harness e2e suite: real harness binaries, in containers, against a
# real slop-haus stack and a scripted model server -- see README.md.
#
#   ./test.sh                          every harness, every scenario
#   ./test.sh --harness claude         one harness
#   ./test.sh --scenario ticket_to_review   only the scenarios whose name matches
#                                      (pytest -k)
#   HARNESS_LATEST=1 ./test.sh         each harness at its latest version
#                                      instead of the pin (the scheduled run)
#   ./test.sh --capture-hook-payloads DIR   run only the interactive hook scenario (Claude Code) and
#                                      copy the hook payloads it recorded, sanitized, into DIR
#                                      (server/internal/hookserver/testdata/claude)
#   HARNESS_E2E_INTERACTIVE_HOOKS=1 ./test.sh   also run the interactive hook scenario (the @latest run does)
#   HARNESS_E2E_ARTIFACTS=dir ./test.sh   where a failed run's logs are kept
#                                      (default harness_e2e/artifacts)
#   HARNESS_E2E_REQUIRE_SANDBOX=1 ./test.sh   fail, not skip, the Codex steward scenarios where the container cannot run bubblewrap
#   HARNESS_PARALLEL=0 ./test.sh       the harnesses one after another, stopping at the first
#                                      that fails, instead of all at once (the default, 1)
#
# Containerized like server/test.sh: every image carries what it needs and
# nothing is mounted from the checkout, so it runs anywhere a Docker daemon
# is -- including a CI step, whose checkout the daemon cannot see. Every
# stack is taken down on exit, whether the suite passed or not, and this
# script's exit code is the suite's.
#
# Each harness runs against a stack of its own: this script builds every
# image once, then runs itself again per harness (../../dev-tools/shard/shard_lib.sh),
# and each of those tags the images as its own project's and brings its stack
# up from them. A stack per harness is also what gives each a fresh api, whose
# rate limits are per signed-in user and in memory: one harness's device
# sign-ins (20 lookups in 10 minutes) would leave the next none on a shared one.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The run per harness is this script again, with the same arguments.
SELF_SCRIPT="${HERE}/$(basename "${BASH_SOURCE[0]}")"
ARGS=("$@")
# The compose files' directory: build paths in the overlay are relative to it.
cd "${HERE}/.."

# Every harness there is an adapter and a compose service for.
ALL_HARNESSES=(claude pi codex)
harnesses=()
pytest_args=()
capture_dir=""
interactive_hooks="${HARNESS_E2E_INTERACTIVE_HOOKS:-0}"
while (($#)); do
  case "$1" in
    --harness)
      [[ " ${ALL_HARNESSES[*]} " == *" ${2:?--harness needs a name} "* ]] || {
        echo "harness_e2e: unknown harness '$2' (have: ${ALL_HARNESSES[*]})" >&2
        exit 2
      }
      harnesses+=("$2")
      shift 2
      ;;
    --scenario)
      pytest_args+=(-k "${2:?--scenario needs a name}")
      shift 2
      ;;
    --capture-hook-payloads)
      capture_dir="$(mkdir -p "${2:?--capture-hook-payloads needs a directory}" && cd "$2" && pwd)"
      harnesses=(claude)
      interactive_hooks=1
      pytest_args+=(-k test_session_start_stop_and_usage_hooks)
      shift 2
      ;;
    *)
      pytest_args+=("$1")
      shift
      ;;
  esac
done
((${#harnesses[@]})) || harnesses=("${ALL_HARNESSES[@]}")
HARNESS_PARALLEL="${HARNESS_PARALLEL:-1}"
if [[ "${HARNESS_PARALLEL}" != 0 && "${HARNESS_PARALLEL}" != 1 ]]; then
  echo "harness_e2e: HARNESS_PARALLEL must be 0 or 1, got '${HARNESS_PARALLEL}'" >&2
  exit 2
fi
# A run of one harness, which the build run below starts on the images it built.
if [[ -n "${SHARD_IMAGES_FROM:-}" ]]; then
  [[ " ${ALL_HARNESSES[*]} " == *" ${SHARD_NAME:-} "* ]] || {
    echo "harness_e2e: SHARD_IMAGES_FROM is set, but SHARD_NAME '${SHARD_NAME:-}' names no harness" >&2
    exit 2
  }
  harnesses=("${SHARD_NAME}")
fi
# The interactive hook scenario is opt-in until it has earned a place in the gating run.
if [[ "${HARNESS_LATEST:-0}" == "1" ]]; then interactive_hooks=1; fi
export HARNESS_E2E_INTERACTIVE_HOOKS="${interactive_hooks}"

set -a
# shellcheck source=SCRIPTDIR/harness_versions.env
source "${HERE}/harness_versions.env"
set +a
if [[ "${HARNESS_LATEST:-0}" == "1" ]]; then
  CLAUDE_CODE_VERSION=latest
  PI_VERSION=latest
  CODEX_VERSION=latest
fi
export CLAUDE_CODE_VERSION PI_VERSION CODEX_VERSION

# Ephemeral stack, not a real secret: default it, as e2e_playwright_ts does.
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-e2e_test_password}"

# Image builds resolve base images (ghcr.io, Docker Hub) over the network, and a
# registry timeout there is not a test failure: retry the build, never the tests.
retry_build() {
  local attempt
  for attempt in 1 2 3; do
    "$@" && return 0
    echo "build failed (attempt ${attempt}/3): $*" >&2
    sleep $((attempt * 10))
  done
  return 1
}

ARTIFACTS="${HARNESS_E2E_ARTIFACTS:-${HERE}/artifacts}"

# A unique compose project per run, carrying the CI step container it is in,
# so a later run can reap what a killed one left (see e2e_playwright_ts/test.sh).
# shellcheck source=SCRIPTDIR/../../dev-tools/reap/reap_lib.sh
source ../dev-tools/reap/reap_lib.sh
# shellcheck source=SCRIPTDIR/../../dev-tools/shard/shard_lib.sh
source ../dev-tools/shard/shard_lib.sh
SELF="$(reap_self_container)"
PROJECT="$(reap_compose_project slop-haus-harness "${SELF}")"
export HARNESS_E2E_API_IMAGE="${PROJECT}-api"

compose() {
  docker compose -p "${PROJECT}" -f docker-compose-dev.yaml -f harness_e2e/docker-compose.yaml "$@"
}

# The run container first: down cannot remove a network a container is still
# on. --rmi local removes the images named after this project (the build
# run's images, or a harness run's tags of them); the api's is named in the
# overlay, so it goes by name.
cleanup() {
  for harness in "${harnesses[@]}"; do
    docker rm -f "${PROJECT}-run-${harness}" > /dev/null 2>&1 || true
  done
  compose --profile harness down -v --rmi local > /dev/null 2>&1 || true
  docker image rm "${HARNESS_E2E_API_IMAGE}" > /dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The build run: every image once, then a run per harness (this script again)
# on them, and its cleanup removes the images once they are all done.
if [[ -z "${SHARD_IMAGES_FROM:-}" ]]; then
  # ruff over the suite's Python (../../dev-tools/lint/README.md), on the
  # host and once, before anything is built.
  ../dev-tools/lint/ruff.sh harness_e2e
  # The scripted model server's own tests, in the image that serves it, first:
  # a break there should not surface as a confusing scenario failure.
  IIDFILE="$(mktemp)"
  retry_build docker build --target scripted-model-test --iidfile "${IIDFILE}" -f "${HERE}/Dockerfile" "${HERE}"
  docker run --rm "$(cat "${IIDFILE}")"
  rm -f "${IIDFILE}"

  reap_compose slop-haus-harness "${PROJECT}" "${SELF}"
  retry_build compose --profile harness build api frontend scripted-model "${harnesses[@]/#/harness-}"
  export SHARD_IMAGES_FROM="${PROJECT}"
  status=0
  if ((HARNESS_PARALLEL && ${#harnesses[@]} > 1)); then
    shard_run_each "${harnesses[@]}" -- "${SELF_SCRIPT}" ${ARGS[@]+"${ARGS[@]}"} || status=$?
  else
    for harness in "${harnesses[@]}"; do
      SHARD_NAME="${harness}" "${SELF_SCRIPT}" ${ARGS[@]+"${ARGS[@]}"} || {
        status=$?
        break
      }
    done
  fi
  exit "${status}"
fi

harness="${harnesses[0]}"
echo "=== harness: ${harness} ==="
shard_share_images "${SHARD_IMAGES_FROM}" "${PROJECT}"
# Not --build: the images are the build run's, tagged as this project's above.
# Still retried, for the base images up pulls (Postgres).
retry_build compose up -d api seed frontend scripted-model
# As in ../e2e_playwright_ts/test.sh: the overlay's in-memory Postgres.
# shellcheck disable=SC2016 # PGDATA is the container's, expanded there
compose exec -T postgres sh -c 'grep -q " $PGDATA tmpfs " /proc/mounts' || {
  echo "test.sh: Postgres's PGDATA is not on tmpfs" >&2
  exit 1
}

status=0
container="${PROJECT}-run-${harness}"
# Not --rm: a failed run's logs are still in the container to be copied out.
# In the background and waited on: bash runs a trap only once the foreground
# command is done, so a TERM (passed on by the build run) would otherwise wait
# out the whole suite before cleanup removed the container.
compose run --name "${container}" "harness-${harness}" \
  python3 -m pytest -p no:cacheprovider -v scenarios ${pytest_args[@]+"${pytest_args[@]}"} &
wait "$!" || status=$?
if [[ -n "${capture_dir}" && ${status} -eq 0 ]]; then
  docker cp "${container}:/logs/test_session_start_stop_and_usage_hooks/fixtures/." "${capture_dir}/"
fi
if ((status != 0)); then
  echo "=== ${harness} failed: collecting logs into ${ARTIFACTS}/${harness} ===" >&2
  mkdir -p "${ARTIFACTS}/${harness}"
  docker cp "${container}:/logs/." "${ARTIFACTS}/${harness}/" > /dev/null 2>&1 || true
  # The server side, as the CI step's own log is where a failed pipeline is read.
  for service in api frontend scripted-model; do
    compose logs --no-color "${service}" > "${ARTIFACTS}/${harness}/${service}.log" 2>&1 || true
  done
  for log in "${ARTIFACTS}/${harness}"/*.log "${ARTIFACTS}/${harness}"/*/*.log; do
    [[ -f "${log}" ]] || continue
    echo "--- ${log#"${ARTIFACTS}"/} (last 80 lines) ---" >&2
    tail -n 80 "${log}" >&2
  done
fi
exit "${status}"
