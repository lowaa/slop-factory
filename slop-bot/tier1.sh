#!/usr/bin/env bash
# Runs tier 1 (unit and e2e tests) in a Docker container with no network:
# the same tests test.sh runs, on a clean Linux with git and tmux, as an
# unprivileged user, plus the tests that need root (a second uid for the
# control socket's peer check), which skip on a developer's machine.
#
#   ./tier1.sh [go test args]   in Docker (the default, ATTACH=0, what CI runs)
#   ATTACH=1 ./tier1.sh         a shell in that image, offline too, with slop-bot and
#                               go-clock mounted, for iterating on the host's edits
#
# go test args default to every package, e.g.
#   ./tier1.sh -run TestScheduler ./internal/agents
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${HERE}/.."

WORK="$(mktemp -d)"
# Every container this run starts carries the label, so cleanup can remove
# them: a docker client killed with this script leaves its container running.
RUN_LABEL="slop-bot-tier1=${WORK##*/}"
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
  docker ps -aq --filter "label=${RUN_LABEL}" | xargs -r docker rm -f > /dev/null 2>&1 || true
  rm -rf "${WORK}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Image builds resolve the base image and modules over the network, and a
# registry timeout there is not a test failure: retry the build, never the tests.
build() {
  local attempt
  for attempt in 1 2 3; do
    docker build --iidfile "${WORK}/image.iid" -f slop-bot/tier1/Dockerfile . && return 0
    echo "tier1.sh: build failed (attempt ${attempt}/3)" >&2
    sleep $((attempt * 10))
  done
  return 1
}
build
# Run by the ID this build produced, not a tag: a concurrent run on the same
# daemon would retag a shared name between the build and the run.
IMAGE="$(cat "${WORK}/image.iid")"
# --init: an init as pid 1 to reap orphans, which stay zombies otherwise and
# still answer kill(pid, 0).
RUN=(docker run --rm --init --label "${RUN_LABEL}" --network none)

if [[ "${ATTACH:-0}" == "1" ]]; then
  # Only the two modules are mounted, over the baked-in copies, so the
  # image's own go.work (and its downloaded modules) still apply offline.
  "${RUN[@]}" -it \
    -v "$(pwd)/slop-bot":/srv/slop-bot \
    -v "$(pwd)/go-clock":/srv/go-clock \
    "${IMAGE}" bash
  exit
fi

# In the background and waited on: bash runs a trap only once the foreground
# command is done, so a TERM would otherwise wait out the whole run.
args=("$@")
if ((! $#)); then args=(./...); fi
status=0
"${RUN[@]}" "${IMAGE}" go test -race -count=1 "${args[@]}" &
wait "$!" || status=$?
if ((status)); then exit "${status}"; fi

# The tests that need root run with the whole suite only: a filtered run is
# the caller iterating on something else.
if (($#)); then exit 0; fi
"${RUN[@]}" --user root "${IMAGE}" \
  go test -race -count=1 -run '^TestPeerUIDOfAnotherUser$' -v ./internal/control &
wait "$!" || status=$?
exit "${status}"
