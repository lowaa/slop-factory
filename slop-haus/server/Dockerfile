# Build context is the repo root (see docker-compose-dev.yaml), not this
# directory: slop-haus/server now depends on github.com/lowaa/slop-factory/go-db and
# github.com/lowaa/slop-factory/go-auth via go.work (see ../../go.work) for its new auth and
# multi-tenancy support, not tagged releases, so those modules' source must
# be present in the build context alongside this module.
#
# base holds only the module manifests. Go modules are deliberately not
# downloaded into a layer: each go.mod change made a new ~1.6GB one. Compiling
# stages use BuildKit cache mounts; test.sh gives its containers a named volume
# for the same directories, since a cache mount isn't there at `docker run`.
# test.sh's ATTACH=1 mode bind-mounts real source over /srv on top of it
# instead of rebuilding an image per source change; its default mode builds
# the test stage below instead.
FROM golang:1.27-bookworm AS base

WORKDIR /srv

COPY artifact-store/go.mod ./artifact-store/
COPY go-db/go.mod go-db/go.sum ./go-db/
COPY go-auth/go.mod go-auth/go.sum ./go-auth/
COPY go-privacy/go.mod go-privacy/go.sum ./go-privacy/
COPY go-llm-usage/go.mod go-llm-usage/go.sum ./go-llm-usage/
COPY go-settings/go.mod go-settings/go.sum ./go-settings/
COPY go-clock/go.mod go-clock/go.sum ./go-clock/
COPY go-web/go.mod go-web/go.sum ./go-web/
COPY slop-review/report/go.mod slop-review/report/go.sum ./slop-review/report/
COPY slop-haus/server/go.mod slop-haus/server/go.sum* ./slop-haus/server/
# A workspace scoped to just the modules this build needs, rather than
# COPYing the repo's real go.work -- that file's `use` directives also
# list other local-dev-only modules that have no place in this build
# context, and go refuses to load a workspace referencing a module directory
# that isn't there ("cannot load module ... no such file or directory").
# Generating our own avoids this image breaking every time the real go.work
# gains an entry unrelated to slop-haus.
RUN go work init ./artifact-store ./go-clock ./go-db ./go-auth ./go-privacy ./go-llm-usage ./go-settings ./go-web ./slop-review/report ./slop-haus/server

# The source, for anything that needs it baked in rather than mounted:
# the build below, and test.sh's default (fully containerized) mode. A
# mount only works when the Docker daemon can see the same path -- not the
# case in a CI step, where the daemon is the agent's and the checkout lives
# inside the step's container.
FROM base AS source

COPY artifact-store ./artifact-store
COPY go-db ./go-db
COPY go-auth ./go-auth
COPY go-privacy ./go-privacy
COPY go-llm-usage ./go-llm-usage
COPY go-settings ./go-settings
COPY go-clock ./go-clock
COPY go-web ./go-web
# The report contract slop-review posts to the server with (internal/reviewreport).
COPY slop-review/report ./slop-review/report
COPY slop-haus/server ./slop-haus/server
COPY slop-haus/testdata ./slop-haus/testdata
# The shared vocabulary manifests the Go tests hold their constants to.
COPY slop-haus/contracts ./slop-haus/contracts
COPY slop-haus/token/slop_haus_token.py ./slop-haus/token/
# Only what `uv run --frozen` needs on a worker machine: the supervisor
# package the server ships (internal/supervisorpkg), and its test reads this
# copy to check the real directory still loads.
COPY slop-haus/supervisor/pyproject.toml slop-haus/supervisor/uv.lock ./slop-haus/supervisor/
COPY slop-haus/supervisor/src ./slop-haus/supervisor/src

FROM source AS test

WORKDIR /srv/slop-haus/server

CMD ["go", "test", "./..."]

# The bats, node and Python suites for the scripts the server hands to
# agents (internal/workspace). They run in agent workspaces on
# Debian/Ubuntu, so they're tested in one -- GNU coreutils (run_remote_control.sh's
# realpath -e), util-linux flock, python3, curl, and ps for the lock tests
# and git for sync-repos.sh's tests -- rather than on whatever machine runs test.sh: a CI step's busybox
# realpath has no -e, and it has no python3 at all. bats is the root
# devDependency, at its pinned version (--workspaces=false: the root
# package's own dependencies only). pytest is Debian's, since a workspace
# has nothing but the standard library and the tests need no more.
FROM node:22-bookworm-slim AS workspace-test

RUN apt-get update \
  && apt-get install -y --no-install-recommends python3 python3-pytest curl procps git \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /srv

COPY package.json package-lock.json ./
RUN npm ci --workspaces=false --ignore-scripts --no-audit --no-fund

COPY bats ./bats
# The bash command fixture the pi extension's and usage-report.py's
# extractors are tested against.
COPY go-llm-usage/fixtures ./go-llm-usage/fixtures
COPY slop-haus/server/internal/workspace ./slop-haus/server/internal/workspace
# The manifests pi-extension.test.ts holds the extension to.
COPY slop-haus/contracts ./slop-haus/contracts
# test.sh itself, run by test_sh.bats against a fake docker; it sources the
# reap library.
COPY dev-tools/reap/reap_lib.sh ./dev-tools/reap/
COPY slop-haus/server/test.sh slop-haus/server/test_sh.bats ./slop-haus/server/

# Each part names itself on failure: a bare "workspace: FAIL" with every bats test ok
# could not otherwise be told from a node or pytest failure above the log tail.
CMD ["bash", "-c", "run_part() { local name=$1; shift; \"$@\" || { rc=$?; echo \"== workspace part '${name}' failed (exit ${rc})\" >&2; return \"${rc}\"; }; }; run_part bats bats/run.sh slop-haus/server/internal/workspace/assets/sh slop-haus/server/test_sh.bats && run_part node node --test --experimental-strip-types slop-haus/server/internal/workspace/assets/ts/*.test.ts && run_part pytest python3 -m pytest -q -p no:cacheprovider slop-haus/server/internal/workspace/assets/py"]

# The supervisor package's own checks (slop-haus/supervisor/test.sh: pyright,
# then pytest), which include the Remote Control host's tests. The type check
# runs while the image builds, where the network is, and fetches pyright's
# node then (which wants libatomic); the tests are the container's run. uv's image carries the
# interpreter the package asks for, same version as harness_e2e's.
FROM ghcr.io/astral-sh/uv:0.8.22-python3.12-bookworm-slim AS supervisor-test

RUN apt-get update \
  && apt-get install -y --no-install-recommends git libatomic1 \
  && rm -rf /var/lib/apt/lists/*

# The tests commit in throwaway repos and expect an identity to do it with.
ENV GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com \
    GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

WORKDIR /srv/slop-haus/supervisor

COPY slop-haus/supervisor ./
# The spec the contract tests hold the supervisor's REST clients to.
COPY slop-haus/frontend/src/api_generated/openapi.json /srv/slop-haus/frontend/src/api_generated/
# The manifests the supervisor's contracts tests hold its constants to.
COPY slop-haus/contracts /srv/slop-haus/contracts
# The credential module's source and server copy, which the byte-identity test compares the vendored copy to.
COPY slop-haus/token/slop_haus_token.py /srv/slop-haus/token/
COPY slop-haus/server/internal/workspace/assets/py/slop_haus_token.py /srv/slop-haus/server/internal/workspace/assets/py/
# The model names the server prices, which the headless usage test checks the supervisor's reports against.
COPY go-llm-usage/pricing/aliases.go /srv/go-llm-usage/pricing/
# The label fixture the usage tests run the supervisor's patterns against.
COPY go-llm-usage/fixtures/record_labels.json /srv/go-llm-usage/fixtures/
# Unpinned, pyright-python builds its node env from nodeenv's "latest", which can be a
# pre-release nodeenv crashes on parsing (ValueError on '0-alpha').
ENV PYRIGHT_PYTHON_NODE_VERSION=22.23.4
RUN uv run --frozen pyright
RUN uv run --frozen python tools/suppression_ratchet.py

CMD ["uv", "run", "--frozen", "pytest", "-q"]

FROM source AS build

# The binaries the dev stack and the cluster run: api serves requests,
# add_user is the one-shot account bootstrapper (see
# docker-compose-dev.yaml's seed service and cmd/add_user's own comment) run
# against the same database once the api has migrated it. smoke_client
# makes the deploy smoke test's read-only credential (cmd/smoke_client).
# upgrade_data seeds and checks rows around the kube upgrade test's upgrade
# (cmd/upgrade_data). seed_scale fills a tenant with thousands of tickets for
# the lists-at-scale browser guard (cmd/seed_scale).
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd slop-haus/server && go build -o /srv/bin/api ./cmd/api
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd slop-haus/server && go build -o /srv/bin/add_user ./cmd/add_user
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd slop-haus/server && go build -o /srv/bin/smoke_client ./cmd/smoke_client
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd slop-haus/server && go build -o /srv/bin/upgrade_data ./cmd/upgrade_data
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd slop-haus/server && go build -o /srv/bin/seed_scale ./cmd/seed_scale

# What the cluster and the dev stack run: the three binaries and nothing
# else. `base`, `source` and `build` above stay as they are -- the test
# stages and test.sh's ATTACH=1 want the toolchain -- but deploying `build`
# shipped the Go toolchain, the module cache, the source and the build cache
# on every push (~830MB a tag, none of it shared between tags).
#
# bookworm-slim rather than distroless because things run in the container
# that need more than the binary: the api Deployment's preStop is `sleep 5`,
# the README's first-admin step and upgrade_data.sh `kubectl exec` through
# `env` and `sh -c`, and the compose healthcheck is curl. It is also the
# same Debian release as golang:1.27-bookworm, so the cgo-linked binaries
# find the libc they were built against. Everything else the api reads is
# embedded in the binary (migrations, legal documents, the workspace
# scripts, the privacy inventory), so no source or asset directory is
# needed.
FROM debian:bookworm-slim AS runtime

# ca-certificates for outbound TLS (GitHub, SMTP, the LLM providers),
# tzdata for time zones, curl for the compose healthcheck.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /srv/bin /srv/bin
# The package served at /api/supervisor/package, read at startup (the api's
# SUPERVISOR_PACKAGE_DIR defaults here).
COPY --from=build /srv/slop-haus/supervisor /srv/supervisor

# Numeric, so the kubelet can verify it is not root. The api writes nothing
# to its filesystem: logs go to stderr and the collector, state to Postgres.
USER 65532:65532

# Last, so a new tag rebuilds nothing above: the tag this image is built as
# (.github/workflows/publish_images.yml), which the api reports at /api/version.
ARG BUILD_TAG=dev
ENV BUILD_TAG=${BUILD_TAG}

CMD ["/srv/bin/api"]
