# Tier 1's image: Go, git and tmux, which the unit and e2e suites shell out
# to, and slop-bot's source. ../tier1.sh runs it with no network. The suite
# runs as an unprivileged user, as the daemon does; the one test that needs
# a second uid (control's peer-uid check) runs as root.
#
# Build context is the repo root: slop-bot depends on go-clock through
# go.work, not a tagged release, so its source has to be in the context.
FROM golang:1.27.4-bookworm AS test

# gcc comes with the golang image (for -race); procps because tests signal
# and inspect processes.
RUN apt-get update \
  && apt-get install -y --no-install-recommends git tmux procps ca-certificates \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --create-home --uid 1000 bot \
  && mkdir -p /srv /gopath /gocache \
  && chown bot:bot /srv /gopath /gocache
# One cache for the unprivileged and the root run; made writable for both
# below.
ENV GOPATH=/gopath \
    GOCACHE=/gocache \
    GOTOOLCHAIN=local \
    GIT_CONFIG_GLOBAL=/etc/slop-bot-gitconfig
# Tests commit, and no identity comes from a home directory.
RUN git config --file /etc/slop-bot-gitconfig user.name tier1 \
  && git config --file /etc/slop-bot-gitconfig user.email tier1@example.invalid \
  && git config --file /etc/slop-bot-gitconfig init.defaultBranch main \
  && git config --file /etc/slop-bot-gitconfig safe.directory '*'
USER bot
WORKDIR /srv

COPY --chown=bot:bot go-clock/go.mod go-clock/go.sum ./go-clock/
COPY --chown=bot:bot slop-bot/go.mod slop-bot/go.sum ./slop-bot/
# A workspace of just the modules slop-bot uses, rather than the repo-level
# go.work, whose other entries aren't in this context.
RUN go work init ./go-clock ./slop-bot && test -f go.work && cd slop-bot && go mod download

COPY --chown=bot:bot go-clock ./go-clock
COPY --chown=bot:bot slop-bot ./slop-bot
WORKDIR /srv/slop-bot

# Compiled while there is a network, so the run, which has none, only links.
RUN go test -race -count=1 -run '^$' ./... \
  && chmod -R a+rwX /gopath /gocache

# GOPROXY=off: a module the build somehow lacks fails at once in the
# offline run, instead of waiting on a proxy it cannot reach.
ENV GOPROXY=off \
    GOFLAGS=-mod=readonly
CMD ["go", "test", "-race", "-count=1", "./..."]
