#!/usr/bin/env bash
# Runs the dockerized TypeScript Playwright e2e suite against the full dev
# stack (Postgres + api + frontend), using the e2e overlay
# (docker-compose-dev.e2e.yaml) to add the e2e-tests-ts service on top of
# ../docker-compose-dev.yaml. The stack (including volumes) is torn down on
# exit whether the tests pass or fail, and this script's exit code matches
# the e2e-tests-ts container's (playwright test's) exit code, so it's
# CI-friendly.
#
# Two modes:
#   ./test.sh [cmd]      fully containerized (the default, ATTACH=0): every
#                        image carries what it needs and nothing is mounted
#                        from the checkout, so it runs anywhere a Docker
#                        daemon is -- including a CI step, whose checkout
#                        the daemon can't see to mount
#   SCALE=1 ./test.sh    the lists-at-scale render guard instead of the suite:
#                        seeds 5,000 tickets, then runs tests/lists_at_scale.spec.ts
#                        (the "scale" Playwright project, which a plain run
#                        leaves out). Nightly in CI, on demand here.
#   ATTACH=1 ./test.sh   the same stack with the tests, the Playwright config
#                        and Dex's config mounted from here
#                        (docker-compose-dev.e2e.attach.yaml), and a shell in
#                        the e2e-tests-ts container instead of a run: edit
#                        here, `npx playwright test` there. The stack comes
#                        down when the shell exits.
#   E2E_RESULTS_OUT=dir ./test.sh
#                        a failed run copies each shard's Playwright
#                        test-results (traces, screenshots) to dir/<shard>/,
#                        before the stack and its volumes are torn down, for
#                        CI to keep as an artifact. A passing run
#                        copies nothing.
#   SHARDS=N ./test.sh   the suite split N ways (Playwright's --shard), each
#                        shard against a whole stack of its own, all at once.
#                        The suite is serial because cleanDatabase truncates
#                        the one database before every test, so a stack per
#                        shard is what lets it run in parallel at all
#                        (../../dev-tools/shard/shard_lib.sh).
set -euo pipefail

# Before the cd: shard_run_all runs this script again, and a relative path to
# it would no longer resolve from there.
SELF_SCRIPT="$(realpath "${BASH_SOURCE[0]}")"
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# Repo-root .env (gitignored), sourced the way dev_env.sh does -- not required for this stack (it has no worker/LLM
# service), but kept for consistency and in case a future service needs it.
if [[ -f ../.env ]]; then
  set -a
  # shellcheck disable=SC1091
  source ../.env
  set +a
fi

# Ephemeral, docker-compose-only stack -- not a real secret -- so default it
# instead of forcing every local run to set it, but still let CI/devs
# override it if needed.
export POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-e2e_test_password}"

# shellcheck source=SCRIPTDIR/../../dev-tools/shard/shard_lib.sh
source ../dev-tools/shard/shard_lib.sh
SHARDS="${SHARDS:-1}"
shard_check_count "${SHARDS}" || exit
if ((SHARDS > 1)) && [[ "${ATTACH:-0}" == "1" || "${SCALE:-0}" == "1" || $# -gt 0 ]]; then
  echo "test.sh: SHARDS splits the plain suite run; it can't be combined with ATTACH, SCALE or a command" >&2
  exit 2
fi

# A unique compose project per run. Without it two concurrent runs share
# one project name, and the first to finish tears down the network the
# other is still using -- which surfaces as every test failing instantly on
# "getaddrinfo EAI_AGAIN api", looking like a code failure rather than two
# suites colliding. The overlay already publishes no host ports, so with a
# distinct project name runs are fully independent. The name also carries
# the CI step container the run is in, if any, so a later run can tell when
# the stack's owner is gone (see reap_compose below).
#
# The api image is named after the project too (see
# docker-compose-dev.e2e.yaml), so two runs can't retag each other's build.
# shellcheck source=SCRIPTDIR/../../dev-tools/reap/reap_lib.sh
source ../dev-tools/reap/reap_lib.sh
SELF="$(reap_self_container)"
PROJECT="$(reap_compose_project slop-haus-e2e "${SELF}")"
export E2E_API_IMAGE="${PROJECT}-api"

COMPOSE_FILES=(-f docker-compose-dev.yaml -f e2e_playwright_ts/docker-compose-dev.e2e.yaml)
if [[ "${ATTACH:-0}" == "1" ]]; then
  COMPOSE_FILES+=(-f e2e_playwright_ts/docker-compose-dev.e2e.attach.yaml)
fi

compose() {
  docker compose -p "${PROJECT}" "${COMPOSE_FILES[@]}" "$@"
}

# --rmi local removes the images compose named after this project (frontend,
# dex, the test runner); the api image has a name set in the overlay, which
# --rmi local leaves. The profile, because down leaves alone services
# outside the active profiles -- and the test runner is only in e2e-ts.
#
# Teardown must not change the result: under `set -e` a failing `down` (an
# image or network still in use, a daemon hiccup) would replace the exit
# status of this EXIT trap's script, failing a fully green run. So its
# failure is reported loudly on stderr but the run's own status is kept.
# The last line says both statuses, since the log tail is what a failed CI
# step shows.
cleanup() {
  local run_status=$? cleanup_status=0
  compose --profile e2e-ts down -v --rmi local || cleanup_status=$?
  if ((cleanup_status != 0)); then
    echo "test.sh: cleanup failed (exit ${cleanup_status}): compose down; the run's status is kept" >&2
  fi
  docker image rm "${E2E_API_IMAGE}" > /dev/null 2>&1 || true
  echo "test.sh: exiting ${run_status} (run: ${run_status}, cleanup: ${cleanup_status}${SHARD_NAME:+, ${SHARD_NAME}})" >&2
  exit "${run_status}"
}
# INT and TERM go through exit, so that they tidy up too. A SIGKILL can't:
# a CI step killed outright (a CI system cancelling a superseded run)
# leaves its stack running. So first, take down what such runs left behind
# -- the api image included, since compose labels what it builds with the
# project whatever the image's name.
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
reap_compose slop-haus-e2e "${PROJECT}" "${SELF}"

# Sharded, this run only builds: every image once, which each shard (this
# script again) tags as its own and starts from -- see shard_lib.sh. Its
# cleanup above removes the images once the shards are done with them.
if ((SHARDS > 1)) && [[ -z "${SHARD_INDEX:-}" ]]; then
  compose --profile e2e-ts build
  export SHARD_IMAGES_FROM="${PROJECT}"
  status=0
  shard_run_all "${SHARDS}" "${SELF_SCRIPT}" || status=$?
  exit "${status}"
fi
BUILD=(--build)
if [[ -n "${SHARD_IMAGES_FROM:-}" ]]; then
  shard_share_images "${SHARD_IMAGES_FROM}" "${PROJECT}"
  BUILD=()
fi

# Logs of the services under test, to the failed run's output. Best effort:
# a log that can't be read must not replace the suite's exit code.
dump_service_logs() {
  local service
  for service in api frontend; do
    echo "::group::${service} logs"
    compose logs --no-color --timestamps --tail=2000 "${service}" || true
    echo "::endgroup::"
  done
}

# copy_results -- the failed run's test-results out of the volume the test
# runner wrote them to (docker-compose-dev.e2e.yaml), into
# E2E_RESULTS_OUT/<shard>/. A container only to docker cp from: its volume
# holds the files after the runner is gone, and the CI step has to get them
# through the daemon, which cannot see this checkout to bind-mount it.
# Best effort, like dump_service_logs: it must not replace the suite's exit
# code. The container goes before the stack does, or `down -v` could not
# remove the volume; its compose project label lets the next run's
# reap_compose clear one a killed run left behind.
copy_results() {
  [[ -n "${E2E_RESULTS_OUT:-}" ]] || return 0
  local holder="${PROJECT}-results" dest="${E2E_RESULTS_OUT}/${SHARD_INDEX:-1}"
  mkdir -p "${dest}" || return 0
  if docker create --name "${holder}" --label "com.docker.compose.project=${PROJECT}" -v "${PROJECT}_e2e-results:/results" "${E2E_API_IMAGE}" > /dev/null &&
    docker cp "${holder}:/results/." "${dest}"; then
    echo "test.sh: test-results copied to ${dest}"
  else
    echo "test.sh: could not copy the test-results out" >&2
  fi
  docker rm -f "${holder}" > /dev/null 2>&1 || true
}

# Brings up (and waits on) everything except e2e-tests-ts itself, which is
# excluded from a plain `up` by its "e2e-ts" profile (see
# docker-compose-dev.e2e.yaml).
compose up ${BUILD[@]+"${BUILD[@]}"} -d
echo "stack up after ${SECONDS}s"
# The overlay keeps Postgres in memory (see its postgres service). Losing
# that -- a dropped override, or an image that moves PGDATA -- would show
# only as slow, flaky runs on a btrfs agent, so it fails the run instead.
# shellcheck disable=SC2016 # PGDATA is the container's, expanded there
compose exec -T postgres sh -c 'grep -q " $PGDATA tmpfs " /proc/mounts' || {
  echo "test.sh: Postgres's PGDATA is not on tmpfs" >&2
  exit 1
}

SCALE_ENV=()
if [[ "${SCALE:-0}" == "1" ]]; then
  compose --profile e2e-scale run --rm seed-scale
  SCALE_ENV=(-e SCALE_TESTS=1)
  if [[ $# -eq 0 ]]; then
    set -- npx playwright test --project=scale
  fi
fi
if [[ -n "${SHARD_INDEX:-}" ]]; then
  # The stack-backed project split, the stack-free one run whole in one
  # shard. SCALE, the other project, never shards (refused above).
  shard_playwright_command "${SHARD_INDEX}" "${SHARDS}" e2e e2e_support
  set -- "${SHARD_COMMAND[@]}"
fi

# `run` starts (only) e2e-tests-ts, reusing the already-running dependencies
# above, and its own exit code becomes this script's (via set -e).
#
# Any arguments are passed through as the container's command, overriding
# its default, e.g. to narrow a run while iterating on one test:
#   ./test.sh npx playwright test --grep "files a ticket"
# E2E_RUN_ARGS adds `compose run` options (volumes, env) rather than
# container arguments -- dev-tools/style-check/coverage/README.md uses it to
# mount the directory CSS coverage captures are written to.
if [[ "${ATTACH:-0}" == "1" ]]; then
  # Not exec: the EXIT trap has to run after the shell to take the stack down.
  # shellcheck disable=SC2086
  compose run --build --rm ${E2E_RUN_ARGS:-} e2e-tests-ts bash
else
  # The stack goes down on exit, taking its logs with it, and the suite's
  # own output says only what the client saw (a "socket hang up" does not
  # say whether the server, a proxy or the client dropped the request). So a
  # failed run prints the services' logs first, while they are still there.
  status=0
  # shellcheck disable=SC2086
  compose run ${BUILD[@]+"${BUILD[@]}"} --rm ${E2E_RUN_ARGS:-} ${SCALE_ENV[@]+"${SCALE_ENV[@]}"} e2e-tests-ts "$@" || status=$?
  if [[ "${status}" -ne 0 ]]; then
    dump_service_logs
    copy_results
  fi
  exit "${status}"
fi
