#!/usr/bin/env bash
#
# Fails on an exported function or method that no main under cmd/ reaches and
# that is in neither baseline.txt nor allowlist.txt. test.sh runs it with the
# other linters (dev-tools/lint/go.sh); this runs it alone.
#
#   deadcode/check.sh           compare against the baseline and allow-list
#   deadcode/check.sh prune     delete baseline lines that are no longer dead
#   deadcode/check.sh baseline  rewrite baseline.txt from the current output
#
# Code a split lands unwired (docs: splitting.md) shows up here until its wiring
# ticket lands. Its allowlist.txt line is "<file> <symbol> #<ticket>", the
# ticket being the one that wires it, and is deleted when that ticket merges.
# baseline.txt is the generated list of what was already unreachable when the
# guard arrived; it only shrinks. Lines carry no line numbers, so edits
# elsewhere in a file don't churn them.
#
# Both ways: a baseline or allow-list line that is no longer unreachable (the
# code was wired or deleted) also fails, so the lists can only shrink and a
# deadcode run that silently found nothing cannot pass.
#
# Test-only helpers (e.g. clock.Fake) are unreachable from the mains too and
# are in the baseline for that reason.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
case "${1:-}" in
  '') exec dev-tools/lint/go.sh --only=deadcode slop-haus/server ;;
  prune) exec dev-tools/lint/go.sh --prune --only=deadcode slop-haus/server ;;
  baseline) exec dev-tools/lint/go.sh --rebaseline --only=deadcode slop-haus/server ;;
  *)
    echo "usage: deadcode/check.sh [prune|baseline]" >&2
    exit 2
    ;;
esac
