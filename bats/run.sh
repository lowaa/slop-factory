#!/usr/bin/env bash
# Runs bats for this repo: the version pinned in the root package.json, with
# bats-support, bats-assert and this repo's own helpers (lib/repo_test) on
# the library path. Every test.sh runs its suites through here, so no suite
# depends on whatever bats a machine happens to have installed.
#
#   bats/run.sh dev-tools/reap          # every .bats file under a dir
#   bats/run.sh -f label dev-tools/reap # only tests matching a name
#
# A suite loads its helpers with `bats_load_library repo_test`, which only
# resolves through this script's BATS_LIB_PATH.
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)
BATS="${ROOT}/node_modules/.bin/bats"

if [[ ! -x "${BATS}" ]]; then
  echo "bats is not installed -- run 'npm ci' in ${ROOT}" >&2
  exit 1
fi

export BATS_LIB_PATH="${ROOT}/bats/lib:${ROOT}/node_modules"
exec "${BATS}" "$@"
