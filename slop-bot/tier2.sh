#!/usr/bin/env bash
# Runs tier 2: the real harness CLIs (Claude Code, Codex, pi, agy) driven through
# slop-bot's adapters against the scripted model, which answers for the
# model, so no credential is used and no token spent. The tests are opt-in
# (SLOP_BOT_TIER2=1), so test.sh doesn't run them.
#
#   ./tier2.sh [go test args]   in Docker (the default, ATTACH=0, what CI runs):
#                               each harness at its pin (tier2/harness_versions.env),
#                               the source baked into the image, and no network:
#                               the scripted model is in the test process, so it
#                               is all a harness can reach (TestContainerNetworkIsolation)
#   ATTACH=1 ./tier2.sh         a shell in that image, offline too, with slop-bot and
#                               go-clock mounted, for iterating: edit on the host,
#                               run `go test ./internal/harness/conformance/scripted` in it
#   NATIVE=1 ./tier2.sh [args]  on this machine, with the harnesses installed here
#                               (a missing one skips); each gets a temp config dir
#                               or home, so the user's own are never touched
#   HARNESS_LATEST=1 ./tier2.sh each harness at its latest release instead of the pin
#
# go test args default to every tier 2 package; e.g.
#   ./tier2.sh -run TestPiConformance ./internal/harness/conformance/scripted
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACKAGES=(./internal/scriptedmodel/... ./internal/harness/...)

if [[ "${NATIVE:-0}" == "1" ]]; then
  cd "${HERE}"
  if (($#)); then
    SLOP_BOT_TIER2=1 exec go test -race -count=1 -timeout 20m "$@"
  fi
  SLOP_BOT_TIER2=1 exec go test -race -count=1 -timeout 20m "${PACKAGES[@]}"
fi

# The repo root is the build context: slop-bot builds through go.work with
# go-clock (tier2/Dockerfile).
cd "${HERE}/.."

set -a
# shellcheck source=SCRIPTDIR/tier2/harness_versions.env
source "${HERE}/tier2/harness_versions.env"
set +a
BUILD_ARGS=()
if [[ "${HARNESS_LATEST:-0}" == "1" ]]; then
  CLAUDE_CODE_VERSION=latest
  CODEX_VERSION=latest
  PI_VERSION=latest
  AGY_VERSION=latest
  # "latest" is the same build arg every time, so a cached install layer
  # would be reused as if it were still the latest release.
  BUILD_ARGS+=(--no-cache --pull)
fi

WORK="$(mktemp -d)"
# Every container this run starts carries the label, so cleanup can remove
# them: a docker client killed with this script leaves its container running.
RUN_LABEL="slop-bot-tier2=${WORK##*/}"
# shellcheck disable=SC2329 # run by the EXIT trap
cleanup() {
  docker ps -aq --filter "label=${RUN_LABEL}" | xargs -r docker rm -f > /dev/null 2>&1 || true
  rm -rf "${WORK}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Image builds resolve base images and npm packages over the network, and a
# registry timeout there is not a test failure: retry the build, never the tests.
build() {
  local attempt
  for attempt in 1 2 3; do
    docker build --target test --iidfile "${WORK}/image.iid" ${BUILD_ARGS[@]+"${BUILD_ARGS[@]}"} \
      --build-arg "CLAUDE_CODE_VERSION=${CLAUDE_CODE_VERSION}" \
      --build-arg "CODEX_VERSION=${CODEX_VERSION}" \
      --build-arg "PI_VERSION=${PI_VERSION}" \
      --build-arg "AGY_VERSION=${AGY_VERSION}" \
      --build-arg "AGY_RELEASE=${AGY_RELEASE}" \
      --build-arg "AGY_SHA512_AMD64=${AGY_SHA512_AMD64}" \
      --build-arg "AGY_SHA512_ARM64=${AGY_SHA512_ARM64}" \
      -f slop-bot/tier2/Dockerfile . && return 0
    echo "tier2.sh: build failed (attempt ${attempt}/3)" >&2
    sleep $((attempt * 10))
  done
  return 1
}
build
# Run by the ID this build produced, not a tag: a concurrent run on the same
# daemon would retag a shared name between the build and the run.
IMAGE="$(cat "${WORK}/image.iid")"
# --init: an init as pid 1 to reap orphans. The harnesses' killed children
# are reparented to pid 1, and without one they stay zombies, which still
# answer kill(pid, 0) (agentproc's TestKillReachesTheWholeGroup).
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
status=0
if (($#)); then
  "${RUN[@]}" "${IMAGE}" \
    go test -race -count=1 -timeout 20m "$@" &
else
  "${RUN[@]}" "${IMAGE}" &
fi
wait "$!" || status=$?
exit "${status}"
