#!/usr/bin/env bash
# Runs native_test.sh -- the vitest suite, the Storybook browser tests and the
# initial-load bundle budget --
# inside the Dockerfile's test stage, so the result doesn't depend on this
# machine's Node or Playwright browsers. Then nginx.bats, which checks
# nginx.conf.template's forwarding headers against a real nginx container:
# it drives docker itself, so it runs from here rather than inside the test
# container.
#
# Two modes:
#   ./test.sh            fully containerized (the default, ATTACH=0): the
#                        source is baked into the test image and nothing is
#                        mounted, so it runs anywhere a Docker daemon is --
#                        including a CI step, whose checkout the daemon
#                        can't see to mount
#   ATTACH=1 ./test.sh   a shell in the test image with the source
#                        directories mounted over the baked ones, for
#                        iterating: edit here, run `npm test` or
#                        `./native_test.sh` there. Changes to package.json,
#                        the lockfile or the config files need a new shell
#                        (they're baked, not mounted)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
ROOT="$(cd ../.. && pwd)"

# Style guard, on the host: stylelint is a root devDependency, so the same
# `npm ci` that provides bats provides it, and it needs no image build.
node "${ROOT}/dev-tools/style-check/stylelint_ratchet/ratchet.mjs" "${ROOT}/slop-haus/frontend"

# Module-boundary guard, on the host for the same reason (it needs the root
# `typescript`): see tools/import_boundaries.mjs for the rules. The check
# fails on any violation not in import-baseline.json (empty today) and on
# a fixed one still listed.
node tools/import_boundaries.mjs

# Run by the ID this build produced, not a tag: a concurrent run on the same
# daemon (two CI workflows on one agent) would retag a shared name between
# the build and the run, and this run would test the other's source.
IIDFILE="$(mktemp)"
trap 'rm -f "${IIDFILE}"' EXIT
docker build \
  --target test \
  --iidfile "${IIDFILE}" \
  -f Dockerfile \
  "${ROOT}"
IMAGE="$(cat "${IIDFILE}")"

if [[ "${ATTACH:-0}" == "1" ]]; then
  # exec replaces this shell, so the EXIT trap won't clean up after it.
  rm -f "${IIDFILE}"
  # Source directories only: mounting a whole package would hide the
  # node_modules the image installed under it.
  exec docker run --rm -it \
    -v "${ROOT}/slop-haus/frontend/src:/srv/slop-haus/frontend/src" \
    -v "${ROOT}/slop-haus/frontend/.storybook:/srv/slop-haus/frontend/.storybook" \
    -v "${ROOT}/js-components/ui/src:/srv/js-components/ui/src" \
    -v "${ROOT}/js-components/auth/src:/srv/js-components/auth/src" \
    -v "${ROOT}/js-components/router/src:/srv/js-components/router/src" \
    -v "${ROOT}/js-components/storybook-kit/src:/srv/js-components/storybook-kit/src" \
    "${IMAGE}" \
    bash
fi

docker run --rm "${IMAGE}"

"${ROOT}/bats/run.sh" . tools
