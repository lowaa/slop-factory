# Tier 2's image: Go, the harness CLIs at the versions harness_versions.env
# pins, and slop-bot's source. ../tier2.sh runs it with no network at all:
# the scripted model runs inside the test process, on loopback, so it is the
# only thing a harness can reach.
#
# Build context is the repo root: slop-bot depends on go-clock through
# go.work, not a tagged release, so its source has to be in the context.
FROM golang:1.27.4-bookworm AS go

# node for the harnesses that ship on npm (Claude Code, Codex, pi); pi needs
# >= 22.19.
FROM node:22.23.3-bookworm-slim AS base

# gcc and libc6-dev for -race (cgo); git and procps because the harnesses'
# tools run them; ca-certificates for npm, agy's download and the module
# download at build; curl for agy's. tmux for the TUI harnesses (agy), from
# backports: bookworm's own is 3.3a, below slop-bot's 3.4
# (TestContainerTmuxIsSupported).
RUN echo "deb http://deb.debian.org/debian bookworm-backports main" > /etc/apt/sources.list.d/backports.list \
  && apt-get update \
  && apt-get install -y --no-install-recommends gcc libc6-dev git procps curl ca-certificates \
  && apt-get install -y --no-install-recommends -t bookworm-backports tmux \
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
# agy is no npm package: a checksummed tarball from Google's bucket, which
# needs the network only here, and no sign-in (install_agy.sh).
ARG AGY_VERSION
ARG AGY_RELEASE
ARG AGY_SHA512_AMD64
ARG AGY_SHA512_ARM64
COPY slop-bot/tier2/install_agy.sh /usr/local/bin/install_agy.sh
RUN test -n "${AGY_VERSION}" \
  && if [ "${AGY_VERSION}" = latest ]; then install_agy.sh latest; \
     else install_agy.sh "${AGY_VERSION}" "${AGY_RELEASE}" "${AGY_SHA512_AMD64}" "${AGY_SHA512_ARM64}"; fi
ENV CLAUDE_CODE_VERSION=${CLAUDE_CODE_VERSION} \
    CODEX_VERSION=${CODEX_VERSION} \
    PI_VERSION=${PI_VERSION} \
    AGY_VERSION=${AGY_VERSION}

# /srv is node's: WORKDIR doesn't re-own a directory the base image already
# has as root's, and `go work init` there exits 0 without writing go.work
# when it can't, leaving the build on the published go-clock.
RUN mkdir -p /srv && chown node:node /srv
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
RUN go work init ./go-clock ./slop-bot && test -f go.work && cd slop-bot && go mod download

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
