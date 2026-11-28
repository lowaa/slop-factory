# Tier 2's image: Go, the harness CLIs at the versions harness_versions.env
# pins, and slop-bot's source. ../tier2.sh runs it with no network at all:
# the scripted model runs inside the test process, on loopback, so it is the
# only thing a harness can reach.
#
# Build context is the repo root: slop-bot depends on go-clock through
# go.work, not a tagged release, so its source has to be in the context.
FROM golang:1.27.4-bookworm AS go

# node for the harnesses that ship on npm (all three); pi needs >= 22.19.
FROM node:22.23.3-bookworm-slim AS base

# gcc and libc6-dev for -race (cgo); git and procps because the harnesses'
# tools run them; ca-certificates for npm and the module download at build.
RUN apt-get update \
  && apt-get install -y --no-install-recommends gcc libc6-dev git procps ca-certificates \
  && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go

# The pins, or "latest" (HARNESS_LATEST=1). They stay in the environment, so
# the run can check the install took them.
ARG CLAUDE_CODE_VERSION
ARG CODEX_VERSION
ARG PI_VERSION
RUN test -n "${CLAUDE_CODE_VERSION}" && test -n "${CODEX_VERSION}" && test -n "${PI_VERSION}" \
  && npm install -g --no-audit --no-fund \
    "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
    "@openai/codex@${CODEX_VERSION}" \
    "@earendil-works/pi-coding-agent@${PI_VERSION}" \
  && npm cache clean --force
ENV CLAUDE_CODE_VERSION=${CLAUDE_CODE_VERSION} \
    CODEX_VERSION=${CODEX_VERSION} \
    PI_VERSION=${PI_VERSION}

# Not root: Claude Code refuses --dangerously-skip-permissions as root.
USER node
ENV PATH=/usr/local/go/bin:${PATH} \
    GOPATH=/home/node/go \
    GOTOOLCHAIN=local
WORKDIR /srv

COPY --chown=node:node go-clock/go.mod go-clock/go.sum ./go-clock/
COPY --chown=node:node slop-bot/go.mod slop-bot/go.sum ./slop-bot/
# A workspace of just the modules slop-bot uses, rather than the repo-level
# go.work, whose other entries aren't in this context.
RUN go work init ./go-clock ./slop-bot && cd slop-bot && go mod download

# The source is baked in, not mounted: a CI step's Docker daemon can't see
# the step's checkout.
FROM base AS test

COPY --chown=node:node go-clock ./go-clock
COPY --chown=node:node slop-bot ./slop-bot
WORKDIR /srv/slop-bot

# Compiled while there is a network, so the run, which has none, only links.
RUN go test -race -count=1 -run '^$' ./internal/scriptedmodel/... ./internal/harness/...

# GOPROXY=off: a module the build somehow lacks fails at once in the
# offline run, instead of waiting on a proxy it cannot reach.
ENV SLOP_BOT_TIER2=1 \
    SLOP_BOT_TIER2_CONTAINER=1 \
    GOPROXY=off \
    GOFLAGS=-mod=readonly
CMD ["go", "test", "-race", "-count=1", "-timeout", "20m", "./internal/scriptedmodel/...", "./internal/harness/..."]
