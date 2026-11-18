#!/usr/bin/env bash
# Scores slop-review configurations against a benchmark case -- a real
# change from this repo with known bugs planted in it. Builds the current
# slop-review first, so it always benchmarks the working tree.
#
#   bench.sh --case=bench/cases/<name>/case.json --runs=3
#   bench.sh --case=... --variant='single=' --variant='all=--persona=all'
#   bench.sh --case=... --variant-set=providers   # OpenRouter vs ChatGPT subscription
#
# Needs each variant's provider set up, as review.sh does: OPENROUTER_API_KEY
# for openrouter, a Sign in with ChatGPT login in pi for chatgpt.
# Prints one JSON object on stdout; .markdown is the human-readable table.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_REPO="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
BIN_DIR="$(mktemp -d)"
trap 'rm -rf "$BIN_DIR"' EXIT

cd "$SCRIPT_DIR"
go build -o "$BIN_DIR/slop-review" ./cmd/slop-review
go run ./cmd/slop-review-bench \
  --slop-review-bin="$BIN_DIR/slop-review" \
  --persona-dir="$SCRIPT_DIR/personas" \
  --source-repo="$SOURCE_REPO" \
  "$@"
