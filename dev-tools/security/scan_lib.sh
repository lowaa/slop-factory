#!/usr/bin/env bash
# Shared library for each project's scan.sh; see dev-tools/security/README.md.
# Source it from the project's directory:
#
#   . "$(git rev-parse --show-toplevel)/dev-tools/security/scan_lib.sh"
#
# Every function writes SARIF to ${SCAN_REPORT_DIR}/<tool>-<project-slug>.sarif
# and returns non-zero only when the scanner itself failed. Findings never
# fail a scan (report-only).
#
# Environment:
#   SCAN_REPORT_DIR  where SARIF goes (default <repo>/security-reports)
#   SCAN_BASE_REF    on a pull request, the ref the change merges into
#                    (e.g. origin/main); scan_secrets then covers only
#                    <base>..HEAD. Unset: the working tree.
#   TRIVY_CACHE_DIR  Trivy's own cache (its vulnerability database), read by
#                    trivy itself; CI points it at a volume shared by runs.

# v1.8.0 onwards needs Go 1.26 to build; the repo and its CI images were on 1.25 when this was pinned.
GOVULNCHECK_VERSION="v1.7.0"

SCAN_ROOT="$(git rev-parse --show-toplevel)"
SCAN_REPORT_DIR="${SCAN_REPORT_DIR:-${SCAN_ROOT}/security-reports}"

scan_log() { echo "[scan] $*" >&2; }

# scan_project_path -- the current directory, relative to the repo root.
scan_project_path() {
  local here
  here="$(pwd -P)"
  here="${here#"$(cd "${SCAN_ROOT}" && pwd -P)"}"
  echo "${here#/}"
}

# scan_slug -- the project path with / as -, so go-auth and slop-haus/server
# get distinct report names.
scan_slug() {
  local p
  p="$(scan_project_path)"
  echo "${p:-root}" | tr '/' '-'
}

# scan_report <tool> -- the report file for the current project.
scan_report() {
  mkdir -p "${SCAN_REPORT_DIR}"
  echo "${SCAN_REPORT_DIR}/$1-$(scan_slug).sarif"
}

# scan_need <tool> <install hint>
scan_need() {
  command -v "$1" > /dev/null || {
    scan_log "$1 is not installed: $2"
    return 127
  }
}

# scan_go -- govulncheck over the Go module in the current directory.
# govulncheck exits 0 with findings in sarif format; non-zero is a failure.
# A finding in the standard library means a Go toolchain bump, not a
# dependency bump.
scan_go() {
  scan_need govulncheck "go install golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" || return
  local out
  out="$(scan_report govulncheck)"
  # govulncheck holds the whole module's SSA, so a large module's peak memory
  # is what trips a CI agent's OOM killer. A soft memory limit makes the Go GC
  # work harder before the heap grows, and fewer procs lowers the parallel
  # allocation peak; both are overridable from the environment.
  local rc=0
  GOMEMLIMIT="${GOMEMLIMIT:-${SCAN_GO_MEMLIMIT:-3GiB}}" GOMAXPROCS="${GOMAXPROCS:-${SCAN_GO_MAXPROCS:-2}}" \
    govulncheck -format sarif ./... > "${out}.tmp" || rc=$?
  if ((rc != 0)); then
    rm -f "${out}.tmp"
    # 137 is SIGKILL, with no output of its own: the kernel OOM killer.
    if ((rc == 137)); then
      scan_log "govulncheck was killed (exit 137, likely out of memory) in $(scan_project_path): an infrastructure failure, not a finding"
    else
      scan_log "govulncheck failed (exit ${rc}) in $(scan_project_path)"
    fi
    return 1
  fi
  mv "${out}.tmp" "${out}"
  scan_log "wrote ${out}"
}

# scan_lockfile <file>... -- osv-scanner over lockfiles of the current
# project. Exit 1 means vulnerabilities were found (report-only); any other
# non-zero is the scanner failing.
scan_lockfile() {
  scan_need osv-scanner "see dev-tools/security/README.md" || return
  [[ $# -gt 0 ]] || {
    scan_log "scan_lockfile: no lockfile given"
    return 2
  }
  local out args=() f rc=0
  out="$(scan_report osv-scanner)"
  for f in "$@"; do args+=(-L "$f"); done
  osv-scanner scan source "${args[@]}" --format sarif --output-file "${out}.tmp" > /dev/null || rc=$?
  if ((rc > 1)) || [[ ! -s ${out}.tmp ]]; then
    rm -f "${out}.tmp"
    scan_log "osv-scanner failed (exit ${rc}) in $(scan_project_path)"
    return 1
  fi
  mv "${out}.tmp" "${out}"
  scan_log "wrote ${out}"
}

# scan_secrets -- gitleaks over the current project's path. Matches are
# redacted in the SARIF and never printed.
scan_secrets() {
  scan_need gitleaks "see dev-tools/security/README.md" || return
  local out rel conf=() rc=0
  out="$(scan_report gitleaks)"
  rel="$(scan_project_path)"
  [[ -f ${SCAN_ROOT}/.gitleaks.toml ]] && conf=(--config "${SCAN_ROOT}/.gitleaks.toml")
  if [[ -n ${SCAN_BASE_REF:-} ]]; then
    gitleaks git "${conf[@]}" --redact --no-banner --log-level error --exit-code 0 \
      --log-opts "${SCAN_BASE_REF}..HEAD -- ${rel:-.}" \
      --report-format sarif --report-path "${out}.tmp" "${SCAN_ROOT}" > /dev/null || rc=$?
  else
    gitleaks dir "${conf[@]}" --redact --no-banner --log-level error --exit-code 0 \
      --report-format sarif --report-path "${out}.tmp" "${SCAN_ROOT}/${rel}" > /dev/null || rc=$?
  fi
  if ((rc != 0)) || [[ ! -s ${out}.tmp ]]; then
    rm -f "${out}.tmp"
    scan_log "gitleaks failed (exit ${rc}) in ${rel:-.}"
    return 1
  fi
  mv "${out}.tmp" "${out}"
  scan_log "wrote ${out}"
}

# scan_image <ref>... -- trivy over container images (an argument may hold
# several refs, whitespace-separated, so a pipeline's SCAN_IMAGE can name
# every image of a release). Refs that are all empty skip with a note: a
# project calls `scan_image "${SCAN_IMAGE:-}"` and a local run has none.
# Reports are trivy-<project-slug>-<image>-<tag>.sarif. Everything is kept in
# the SARIF; only the printed headline counts leave out unfixed findings,
# the ones a rebuild can't resolve. One scan: trivy's JSON is converted to
# SARIF, so the headline and the report come from the same run.
scan_image() {
  local refs=() a ref
  for a in "$@"; do
    # Word splitting is the point: one argument may hold several refs.
    # shellcheck disable=SC2206
    refs+=(${a})
  done
  if ((${#refs[@]} == 0)); then
    scan_log "no image to scan (SCAN_IMAGE unset): skipping trivy image in $(scan_project_path)"
    return 0
  fi
  scan_need trivy "see dev-tools/security/README.md" || return
  local failed=0
  for ref in "${refs[@]}"; do
    scan_one_image "${ref}" || failed=1
  done
  return "${failed}"
}

# scan_one_image <ref> -- the ref without its registry host ("/", ":" and "@"
# as -) makes the report name: two images of a project differ, and so do two
# environments' tags of one image.
scan_one_image() {
  local ref="$1" name out json
  name="${ref#*/}"
  name="${name//[\/:@]/-}"
  mkdir -p "${SCAN_REPORT_DIR}"
  out="${SCAN_REPORT_DIR}/trivy-$(scan_slug)-${name}.sarif"
  json="${out}.json.tmp"
  trivy image --quiet --scanners vuln --format json --output "${json}" "${ref}" || {
    rm -f "${json}"
    scan_log "trivy image failed for ${ref}"
    return 1
  }
  trivy convert --quiet --format sarif --output "${out}.tmp" "${json}" || {
    rm -f "${json}" "${out}.tmp"
    scan_log "trivy convert failed for ${ref}"
    return 1
  }
  scan_log "${ref}: $(jq '[.Results[]?.Vulnerabilities[]?] | length' "${json}") finding(s), $(jq '[.Results[]?.Vulnerabilities[]? | select(.FixedVersion != null and .FixedVersion != "")] | length' "${json}") with a fix available"
  rm -f "${json}"
  mv "${out}.tmp" "${out}"
  scan_log "wrote ${out}"
}

# scan_config <path>... -- trivy's misconfiguration scan (Kubernetes
# manifests, Terraform, Dockerfiles) over paths relative to the current
# directory. One report, trivy-config-<project-slug>.sarif, with one run per
# path. Trivy reports locations relative to the path it was given, so they
# are prefixed with it here: a location that doesn't say which directory it
# is in can't be found, and identical file names in two paths would collide.
scan_config() {
  scan_need trivy "see dev-tools/security/README.md" || return
  [[ $# -gt 0 ]] || {
    scan_log "scan_config: no path given"
    return 2
  }
  local out p tmp parts=()
  out="$(scan_report trivy-config)"
  tmp="$(mktemp -d)"
  for p in "$@"; do
    p="${p%/}"
    # A path that vanished would otherwise scan nothing and look clean.
    [[ -e ${p} ]] || {
      rm -rf "${tmp}"
      scan_log "scan_config: ${p} does not exist"
      return 1
    }
    trivy config --quiet --format sarif --exit-code 0 --output "${tmp}/${#parts[@]}.sarif" "${p}" || {
      rm -rf "${tmp}"
      scan_log "trivy config failed for ${p} in $(scan_project_path)"
      return 1
    }
    jq --arg p "${p}/" '.runs[] |= (
        (.results[]?.locations[]?.physicalLocation.artifactLocation.uri) |= $p + .
        | (.artifacts[]?.location.uri) |= $p + .)' \
      "${tmp}/${#parts[@]}.sarif" > "${tmp}/${#parts[@]}.prefixed" || {
      rm -rf "${tmp}"
      scan_log "could not read trivy's report for ${p}"
      return 1
    }
    parts+=("${tmp}/${#parts[@]}.prefixed")
  done
  jq -s '.[0] + {runs: [.[].runs[]]}' "${parts[@]}" > "${out}.tmp" || {
    rm -rf "${tmp}" "${out}.tmp"
    scan_log "could not merge trivy's reports"
    return 1
  }
  rm -rf "${tmp}"
  mv "${out}.tmp" "${out}"
  scan_log "wrote ${out}"
}
