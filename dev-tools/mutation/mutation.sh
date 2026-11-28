#!/usr/bin/env bash
# Mutation testing of a Go project's chosen packages, ratcheted against its
# mutation_baseline/; see dev-tools/mutation/README.md for the flags.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=SCRIPTDIR/mutation_lib.sh
. "${here}/mutation_lib.sh"
mutation_go "$@"
