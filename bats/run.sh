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

# Inline rather than dev-tools/require_bash.sh: the server's test image copies
# bats/ without dev-tools/. bats and lib/repo_test use bash 4 (mapfile).
if ((BASH_VERSINFO[0] < 4)); then
  echo "run.sh: needs bash >= 4 (this is ${BASH_VERSION}); on macOS \`brew install bash\` and make sure it is first on PATH" >&2
  exit 1
fi

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)
BATS="${ROOT}/node_modules/.bin/bats"

if [[ ! -x "${BATS}" ]]; then
  echo "bats is not installed -- run 'npm ci' in ${ROOT}" >&2
  exit 1
fi

export BATS_LIB_PATH="${ROOT}/bats/lib:${ROOT}/node_modules"
exec "${BATS}" "$@"
