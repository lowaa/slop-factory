# tickets.spec.ts drives slop-haus/frontend with a real browser page: the
# Playwright base image supplies it, with the OS libraries it needs, for the
# Playwright version this suite's lockfile pins.
#
FROM mcr.microsoft.com/playwright:v1.61.1-noble

# npm ci must not fetch a second browser beside the image's.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

WORKDIR /srv

COPY package.json package-lock.json ./
RUN npm ci

COPY . .

# The data inventory, whose storage section tests/storage_registry.spec.ts
# audits the browser against. A named build context (the compose file's
# additional_contexts), since it lives outside this directory.
COPY --from=privacy inventory.yaml /srv/inventory.yaml

# The default-look check the Storybook preview runs, which tests/theme.spec.ts
# runs against the real pages (js-components/storybook-kit/src, as a named
# build context).
COPY --from=storybook_kit default_style_check.ts /srv/default_style_check.ts

# Support code shared with the other e2e suites (js-components/e2e-support/src,
# a named build context). Inside /srv so its imports resolve from this
# suite's node_modules.
COPY --from=e2e_support . /srv/e2e_support

# What a real Claude Code sent each hook (server/internal/hookserver/testdata/claude, a named
# build context), which tests/hooks.spec.ts posts.
COPY --from=hook_payloads . /srv/hook_payloads
ENV HOOK_PAYLOADS_DIR=/srv/hook_payloads

CMD ["npx", "playwright", "test"]
