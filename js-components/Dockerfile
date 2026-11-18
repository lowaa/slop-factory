# Build context is the repo root (see test.sh): the packages here are
# root npm workspaces, installed against the root lockfile, and link to
# each other through it.
#
# One npm ci, in deps, shared by both test stages: unit runs on it as is,
# and storybook copies it onto the browser image. Both are Debian (glibc),
# so the native binaries some packages ship carry across the switch.
FROM node:22-bookworm-slim AS deps

# storybook's browser comes from ci-playwright; nothing here needs one.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

WORKDIR /srv

COPY package.json package-lock.json .gitignore ./
COPY js-components/auth/package.json ./js-components/auth/package.json
COPY js-components/logging/package.json ./js-components/logging/package.json
COPY js-components/privacy/package.json ./js-components/privacy/package.json
COPY js-components/router/package.json ./js-components/router/package.json
COPY js-components/storage/package.json ./js-components/storage/package.json
COPY js-components/storybook-kit/package.json ./js-components/storybook-kit/package.json
COPY js-components/ui/package.json ./js-components/ui/package.json
RUN npm ci

COPY js-components ./js-components
# Test data a js-components test shares with a Go module; the
# dockerignore whitelists just this file.
COPY go-auth/login/testdata/local_redirect_cases.json ./go-auth/login/testdata/local_redirect_cases.json

# The style guard (stylelint ratchet) runs from native_test.sh unit.
COPY stylelint.config.mjs ./stylelint.config.mjs
COPY dev-tools/style-check/stylelint_ratchet/ratchet.mjs ./dev-tools/style-check/stylelint_ratchet/ratchet.mjs
# And the oxlint ratchet (dev-tools/lint/README.md); every package's
# .oxlintrc.json extends the shared config.
COPY dev-tools/lint/oxlint.sh dev-tools/lint/ratchet.sh dev-tools/lint/findings.mjs dev-tools/lint/oxlintrc.base.json ./dev-tools/lint/

# lint, typecheck and the vitest suites (jsdom): Node only.
FROM deps AS unit

CMD ["js-components/native_test.sh", "unit"]

# The Storybook stories as browser tests, in the Chromium the Playwright image
# ships for the locked Playwright version.
FROM mcr.microsoft.com/playwright:v1.64.1-noble AS storybook

ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

WORKDIR /srv

COPY --from=deps /srv /srv

CMD ["js-components/native_test.sh", "storybook"]
