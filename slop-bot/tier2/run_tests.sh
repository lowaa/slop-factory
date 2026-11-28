#!/usr/bin/env bash
# Tier 2's default run, from slop-bot's directory, natively or in the image:
# the harness packages, then the daemon scenarios in internal/cli. Those are
# a filter on a package whose other tests are tier 1's, which test.sh runs,
# so they get a go test of their own. Both run; either failing fails it.
set -uo pipefail

status=0
go test -race -count=1 -timeout 20m ./internal/scriptedmodel/... ./internal/harness/... || status=1
go test -race -count=1 -timeout 20m -run '^TestTier2' ./internal/cli || status=1
exit "${status}"
