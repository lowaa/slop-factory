#!/usr/bin/env bash
# Runs native_test.sh inside the Dockerfile's stage for a mode, so the
# result doesn't depend on this machine's Node or Playwright browsers, and
# npm ci never writes root-owned node_modules into the checkout.
#
#   ./test.sh unit        lint, typecheck and vitest for every package
#   ./test.sh storybook   the Storybook stories in Chromium, for every
#                         package that has them
#   ./test.sh             both, one after the other
#
# Two ways to run a mode:
#   ./test.sh <mode>          fully containerized (the default, ATTACH=0):
#                             the source is baked into the image and nothing
#                             is mounted, so it runs anywhere a Docker
#                             daemon is -- including a CI step, whose
#                             checkout the daemon can't see to mount
#   ATTACH=1 ./test.sh <mode> a shell in that stage with each package's
#                             sources mounted over the baked ones, for
#                             iterating: edit here, run e.g.
#                             `npm test -w js-components/router` there.
#                             Changes to package.json, the lockfile or the
#                             config files need a new shell (they're baked,
#                             not mounted)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
ROOT="$(cd .. && pwd)"
IIDFILE="$(mktemp)"
trap 'rm -f "${IIDFILE}"' EXIT

usage() {
  echo "usage: [ATTACH=1] $0 [unit|storybook]" >&2
  exit 2
}

run_mode() {
  local mode="$1" image
  local -a mounts=()

  # Run by the ID this build produced, not a tag: a concurrent run on the
  # same daemon (two CI workflows on one agent) would retag a shared name
  # between the build and the run, and this run would test the other's
  # source.
  docker build \
    --target "${mode}" \
    --iidfile "${IIDFILE}" \
    -f Dockerfile \
    "${ROOT}"
  image="$(cat "${IIDFILE}")"

  if [[ ${ATTACH:-0} == "1" ]]; then
    # Source directories only: mounting a whole package would hide the
    # node_modules npm links under it.
    local pkg dir
    for pkg in "${ROOT}"/js-components/*/; do
      pkg="${pkg%/}"
      [[ -f ${pkg}/package.json ]] || continue
      for dir in src .storybook; do
        [[ -d ${pkg}/${dir} ]] || continue
        mounts+=(-v "${pkg}/${dir}:/srv/js-components/${pkg##*/}/${dir}")
      done
    done
    # exec replaces this shell, so the EXIT trap won't clean up after it.
    rm -f "${IIDFILE}"
    exec docker run --rm -it "${mounts[@]}" "${image}" bash
  fi

  docker run --rm "${image}"
}

case "${1:-}" in
  unit | storybook)
    (($# == 1)) || usage
    run_mode "$1"
    ;;
  '')
    (($# == 0)) || usage
    # A shell is in one stage; the unit and storybook ones differ.
    [[ ${ATTACH:-0} != "1" ]] || usage
    run_mode unit
    run_mode storybook
    ;;
  *) usage ;;
esac
