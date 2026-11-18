# Build context is the repo root (see ../docker-compose-dev.yaml), not this directory: this app is one of the root
# npm workspaces, so it installs against the root lockfile and the image
# mirrors the repo's layout. It also depends on @slop-factory/ui,
# @slop-factory/auth, @slop-factory/logging and @slop-factory/router via those workspaces --
# npm resolves them to symlinks back to ../../js-components/*, so that
# layout has to exist here as on disk too.
FROM node:22-alpine AS deps

WORKDIR /srv

# npm ci against the root lockfile, which resolves every workspace at once;
# only the package.json files this app's install needs are copied, and npm
# ci tolerates the other workspaces' directories being absent.
COPY package.json package-lock.json ./
COPY slop-haus/frontend/package.json ./slop-haus/frontend/package.json
COPY js-components/ui/package.json ./js-components/ui/package.json
COPY js-components/auth/package.json ./js-components/auth/package.json
COPY js-components/logging/package.json ./js-components/logging/package.json
COPY js-components/privacy/package.json ./js-components/privacy/package.json
COPY js-components/router/package.json ./js-components/router/package.json
COPY js-components/storage/package.json ./js-components/storage/package.json
COPY js-components/storybook-kit/package.json ./js-components/storybook-kit/package.json
RUN npm ci

COPY js-components/ui ./js-components/ui
COPY js-components/auth ./js-components/auth
COPY js-components/logging ./js-components/logging
COPY js-components/privacy ./js-components/privacy
COPY js-components/router ./js-components/router
COPY js-components/storage ./js-components/storage
COPY js-components/storybook-kit ./js-components/storybook-kit
COPY slop-haus/frontend ./slop-haus/frontend
COPY slop-haus/testdata ./slop-haus/testdata
# The help area bundles the user guide (src/components/help/guide_pages.ts).
COPY slop-haus/docs/guide ./slop-haus/docs/guide

# Target for test.sh, which runs native_test.sh: the vitest suite (jsdom)
# and the Storybook stories as browser tests. Its own Debian stage, on the
# Playwright base image, rather than FROM deps: Playwright only
# provides Chromium for glibc distributions, and deps' node_modules can't
# be reused across the switch because some packages ship native binaries
# built per libc. Debian also
# has the bash agents_panel.test.tsx's `bash -n` check needs.
FROM mcr.microsoft.com/playwright:v1.64.1-noble AS test

# The image already holds the browser the locked Playwright version expects
# (the base image); npm ci must not fetch another.
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

WORKDIR /srv

COPY package.json package-lock.json ./
COPY slop-haus/frontend/package.json ./slop-haus/frontend/package.json
COPY js-components/ui/package.json ./js-components/ui/package.json
COPY js-components/auth/package.json ./js-components/auth/package.json
COPY js-components/logging/package.json ./js-components/logging/package.json
COPY js-components/privacy/package.json ./js-components/privacy/package.json
COPY js-components/router/package.json ./js-components/router/package.json
COPY js-components/storage/package.json ./js-components/storage/package.json
COPY js-components/storybook-kit/package.json ./js-components/storybook-kit/package.json
RUN npm ci

COPY js-components/ui ./js-components/ui
COPY js-components/auth ./js-components/auth
COPY js-components/logging ./js-components/logging
COPY js-components/privacy ./js-components/privacy
COPY js-components/router ./js-components/router
COPY js-components/storage ./js-components/storage
COPY js-components/storybook-kit ./js-components/storybook-kit
COPY slop-haus/frontend ./slop-haus/frontend
COPY slop-haus/testdata ./slop-haus/testdata
# The help area bundles the user guide (src/components/help/guide_pages.ts).
COPY slop-haus/docs/guide ./slop-haus/docs/guide
# browser_storage.test.ts checks its keys against the privacy inventory.
COPY slop-haus/server/privacy/inventory.yaml ./slop-haus/server/privacy/inventory.yaml
# The server's usage allowlist, which a test holds the event catalog to.
COPY slop-haus/server/internal/usagemetrics/ui_allowlist.json ./slop-haus/server/internal/usagemetrics/ui_allowlist.json
# The oxlint ratchet (dev-tools/lint/README.md), which native_test.sh runs
# first. oxlint skips what .gitignore does, or it would lint node_modules.
COPY .gitignore ./.gitignore
COPY dev-tools/lint/oxlint.sh dev-tools/lint/ratchet.sh dev-tools/lint/findings.mjs dev-tools/lint/oxlintrc.base.json ./dev-tools/lint/

WORKDIR /srv/slop-haus/frontend

CMD ["./native_test.sh"]

FROM deps AS build

# `npm run build` runs two independent Vite builds -- the main SPA
# (dist/) and the admin UI (dist/admin/, vite.admin.config.ts) -- so that a
# non-admin's browser is never served the admin bundle's JavaScript at all,
# not just kept from rendering it. See vite.admin.config.ts's own comment
# and nginx.conf.template's auth_request gate, which is what actually
# enforces that split at request time.
RUN npm run build -w slop-haus/frontend

# nginx with this app's configuration and nothing else: the base of the
# served image below, and what nginx.bats tests (it needs no app build to
# check how nginx handles headers).
FROM nginx:1.27-alpine AS nginx-conf

COPY slop-haus/frontend/nginx.conf.template /etc/nginx/templates/default.conf.template
# Outside templates/ and conf.d/: it is an include, not a server block of
# its own, and nginx.conf.template names it relative to /etc/nginx.
COPY slop-haus/frontend/security_headers.conf /etc/nginx/security_headers.conf

# /version.json: the tag this image is built as, so a deploy can
# tell when this build is the one serving. A stage of its own so
# version.bats can build it in seconds, without the app. Checked to the
# tag's own alphabet first, since it is spliced into JSON unescaped.
FROM nginx:1.27-alpine AS version

ARG BUILD_TAG=dev
RUN case "${BUILD_TAG}" in \
      ''|*[!A-Za-z0-9._-]*) echo "BUILD_TAG '${BUILD_TAG}' is not a tag" >&2; exit 1 ;; \
    esac \
  && printf '{"version":"%s"}\n' "${BUILD_TAG}" > /version.json

# Serve the static build with nginx, proxying /api/* to the api service --
# see nginx.conf.template for why (keeps the browser same-origin, so the Go
# backend needs no CORS support). dist/admin/ nests inside dist/ (the admin
# build's own outDir), so this one copy carries both bundles into place --
# /usr/share/nginx/html/admin/ is exactly what nginx.conf.template's
# location /admin/ expects.
FROM nginx-conf

COPY --from=build /srv/slop-haus/frontend/dist /usr/share/nginx/html
# Last, so a new tag rebuilds nothing but this layer.
COPY --from=version /version.json /usr/share/nginx/html/version.json

# Overridable by docker-compose-dev.yaml; this default lets the image run
# standalone against a same-named `api` service on the default port.
# DNS_RESOLVER defaults to Docker's embedded DNS for the same reason -- see
# nginx.conf.template.
ENV API_UPSTREAM=api:8080
ENV DNS_RESOLVER=127.0.0.11
# The origin of GitHub's website, which the CSP's form-action allows: the
# board posts the App manifest there (see github_app_panel.tsx). Set it to the
# api's GITHUB_WEB_BASE_URL origin if that is not github.com.
ENV GITHUB_WEB_ORIGIN=https://github.com

EXPOSE 80
