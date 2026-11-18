# The images of the harness e2e suite: the scripted model server, and one
# image per harness under test. Build context is this directory
# (test.sh, via docker-compose.yaml). Same Debian release as the server
# Dockerfile's workspace-test stage, whose userland the workspaces' scripts
# are tested against.

# What every image that runs the suite's Python needs. python3-pytest is
# Debian's, as in workspace-test: the suite needs nothing beyond it.
FROM debian:bookworm-slim AS python-base

RUN apt-get update \
  && apt-get install -y --no-install-recommends python3 python3-pytest \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /srv

# The scripted model server, and its own unit tests (test.sh runs them first).
FROM python-base AS scripted-model

COPY scripted_model ./scripted_model

USER 65532:65532
EXPOSE 8000
CMD ["python3", "-m", "scripted_model", "--host", "0.0.0.0", "--port", "8000"]

FROM scripted-model AS scripted-model-test

CMD ["python3", "-m", "pytest", "-q", "-p", "no:cacheprovider", "scripted_model"]

# A workspace as a person has one: a Debian userland with git, curl, and the
# util-linux flock the workspace scripts need, running as a non-root user (Claude
# Code refuses --dangerously-skip-permissions as root). node is here for npm,
# which installs the harnesses that ship there.
FROM node:22-bookworm-slim AS harness-base

RUN apt-get update \
  && apt-get install -y --no-install-recommends python3 python3-pytest curl procps git ca-certificates \
  && rm -rf /var/lib/apt/lists/*

# What the supervisor's setup script and the Remote Control launcher want on a
# machine: uv, and the Python it supplies (>=3.12, which Debian bookworm lacks),
# installed here because the containers have no internet at run time. The
# Remote Control host runs as a uv project, which builds it with the package's
# build backend: a project with the same build-system is built once here so
# that backend is in uv's cache for the offline runs.
COPY --from=ghcr.io/astral-sh/uv:0.8.22 /uv /usr/local/bin/uv
ENV UV_PYTHON_INSTALL_DIR=/opt/uv-python \
    UV_CACHE_DIR=/opt/uv-cache
RUN uv python install 3.12 \
  && mkdir -p /tmp/warm/src/warm \
  && touch /tmp/warm/src/warm/__init__.py \
  && printf '[project]\nname = "warm"\nversion = "0"\nrequires-python = ">=3.12"\n[build-system]\nrequires = ["uv_build>=0.8,<0.13"]\nbuild-backend = "uv_build"\n' > /tmp/warm/pyproject.toml \
  && (cd /tmp/warm && uv sync) \
  && rm -rf /tmp/warm \
  && chmod -R a+rwX /opt/uv-python /opt/uv-cache

# Where the suite writes the logs test.sh collects.
RUN mkdir /logs && chown node:node /logs

WORKDIR /srv

# Claude Code, at the version harness_versions.env pins ("latest" for the
# scheduled run). The version is also what the suite expects the harness to
# report, which is how a pin that did not take is noticed.
FROM harness-base AS harness-claude

ARG CLAUDE_CODE_VERSION
RUN test -n "${CLAUDE_CODE_VERSION}" \
  && npm install -g --no-audit --no-fund "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"
ENV HARNESS=claude \
    HARNESS_VERSION=${CLAUDE_CODE_VERSION}

COPY scripted_model ./scripted_model
COPY harnesses ./harnesses
COPY support ./support
COPY scenarios ./scenarios

USER node
CMD ["python3", "-m", "pytest", "-p", "no:cacheprovider", "scenarios"]

# pi, at the version harness_versions.env pins ("latest" for the scheduled
# run), from npm like Claude Code. Its updater and telemetry are switched off
# by the adapter's environment, not here, so the image is the stock install.
FROM harness-base AS harness-pi

ARG PI_VERSION
RUN test -n "${PI_VERSION}" \
  && npm install -g --no-audit --no-fund "@earendil-works/pi-coding-agent@${PI_VERSION}"
ENV HARNESS=pi \
    HARNESS_VERSION=${PI_VERSION}

COPY scripted_model ./scripted_model
COPY harnesses ./harnesses
COPY support ./support
COPY scenarios ./scenarios

USER node
CMD ["python3", "-m", "pytest", "-p", "no:cacheprovider", "scenarios"]

# Codex, at the version harness_versions.env pins ("latest" for the scheduled run), from npm. Its update check,
# analytics and feedback are switched off by the adapter's config.toml, not here. bubblewrap is what its sandbox
# (the steward's read-only profile) runs commands in.
FROM harness-base AS harness-codex

ARG CODEX_VERSION
RUN apt-get update \
  && apt-get install -y --no-install-recommends bubblewrap \
  && rm -rf /var/lib/apt/lists/* \
  && test -n "${CODEX_VERSION}" \
  && npm install -g --no-audit --no-fund "@openai/codex@${CODEX_VERSION}"
ENV HARNESS=codex \
    HARNESS_VERSION=${CODEX_VERSION}

COPY scripted_model ./scripted_model
COPY harnesses ./harnesses
COPY support ./support
COPY scenarios ./scenarios

USER node
CMD ["python3", "-m", "pytest", "-p", "no:cacheprovider", "scenarios"]
