#!/usr/bin/env bash
# Runs the frontend's tests directly on this machine: the oxlint ratchet,
# the vitest suite (jsdom + testing-library), then the Storybook stories
# as browser tests (vitest.storybook.config.ts). test.sh runs this same
# script inside a container; use this one to skip the image build while
# iterating. The story tests need Playwright's Chromium installed once
# beforehand:
#   npx playwright install chromium
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

# oxlint, type-aware, through its ratchet (dev-tools/lint/README.md). Here
# rather than on the host in test.sh: the type-aware rules need the
# workspace packages' types, which the CI job's host install does not have.
../../dev-tools/lint/oxlint.sh .

npm test
npm run test:storybook

# Initial-load budget: a production build, measured (tools/bundle_budget.mjs).
# Here rather than on the host in test.sh because the build needs the
# workspace packages, which the CI job's host install does not have.
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "${BUILD_DIR}"' EXIT
npx vite build --manifest --outDir "${BUILD_DIR}" --emptyOutDir --logLevel warn
node tools/bundle_budget.mjs "${BUILD_DIR}"
