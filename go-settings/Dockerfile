# Build context is the repo root: go-settings depends on go-db through
# go.work, not a tagged release, so its source has to be in the context
# beside it.
FROM golang:1.27-bookworm AS base

WORKDIR /srv

COPY go-db/go.mod go-db/go.sum ./go-db/
COPY go-settings/go.mod go-settings/go.sum ./go-settings/
# A workspace of just the modules this build needs, rather than the
# repo-level go.work, whose other entries aren't in this context.
RUN go work init ./go-db ./go-settings
RUN cd go-settings && go mod download

# The source is baked in, not mounted: a CI step's Docker daemon can't see
# the step's checkout. The tests start Postgres containers of their own, so
# test.sh runs this with the daemon's socket.
FROM base AS test

COPY go-db ./go-db
COPY go-settings ./go-settings

WORKDIR /srv/go-settings
