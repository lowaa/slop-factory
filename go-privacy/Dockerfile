# Build context is the repo root: go-privacy depends on go-db and go-auth
# through go.work, not tagged releases, so their source has to be in the
# context beside it.
FROM golang:1.27-bookworm AS base

WORKDIR /srv

COPY go-db/go.mod go-db/go.sum ./go-db/
COPY go-auth/go.mod go-auth/go.sum ./go-auth/
COPY go-privacy/go.mod go-privacy/go.sum ./go-privacy/
# A workspace of just the modules this build needs, rather than the
# repo-level go.work, whose other entries aren't in this context.
RUN go work init ./go-db ./go-auth ./go-privacy
RUN cd go-privacy && go mod download

# The source is baked in, not mounted: a CI step's Docker daemon can't see
# the step's checkout. The tests start Postgres containers of their own, so
# test.sh runs this with the daemon's socket.
FROM base AS test

COPY go-db ./go-db
COPY go-auth ./go-auth
COPY go-privacy ./go-privacy

WORKDIR /srv/go-privacy
