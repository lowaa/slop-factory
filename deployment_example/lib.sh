#!/usr/bin/env bash
# Shared by test.sh and scan.sh: the pinned helm image, run against this
# directory, so a local run and CI's use the same helm.
# shellcheck disable=SC2034 # HERE and CHART are for the sourcing scripts.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELM_IMAGE="alpine/helm:3.17.3@sha256:d899e6316789fec04ee95300a18e454b7942539cbb3d89bde3e0655d6ca2e895"
CHART=helm/slop-haus

# helm ARGS...: paths are relative to this directory, which is mounted
# read-only; no network.
helm() {
  docker run --rm --network none -v "${HERE}:/work:ro" -w /work "${HELM_IMAGE}" "$@"
}
