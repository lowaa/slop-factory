# Security scans

Each project has a `scan.sh` next to its `test.sh`, built on `scan_lib.sh`.
Scans are report-only: findings never fail a scan; only a scanner that itself
breaks does. SARIF goes to
`${SCAN_REPORT_DIR:-<repo>/security-reports}/<tool>-<project-slug>.sarif`
(the slug is the project path with `/` as `-`).

Run one like a test: `go-auth/scan.sh`. A missing tool fails the run and
names what to install.

## Tools

| Tool | Version scanned with | Install |
| --- | --- | --- |
| govulncheck | `GOVULNCHECK_VERSION` in `scan_lib.sh` | `go install golang.org/x/vuln/cmd/govulncheck@<version>` |
| osv-scanner | 2.7.0 | [releases](https://github.com/google/osv-scanner/releases) |
| gitleaks | 8.30.1 | [releases](https://github.com/gitleaks/gitleaks/releases) |
| trivy | 0.76.1 | [releases](https://github.com/aquasecurity/trivy/releases) |

## scan_lib.sh

| Function | Does |
| --- | --- |
| `scan_go` | `govulncheck -format sarif ./...` in the current Go module. Memory is bounded (`GOMEMLIMIT` 3GiB, `GOMAXPROCS` 2; override with `SCAN_GO_MEMLIMIT`/`SCAN_GO_MAXPROCS`). A finding in the standard library means a Go toolchain bump, not a dependency bump. |
| `scan_lockfile <file>...` | osv-scanner over lockfiles (`package-lock.json`, `uv.lock`, ...) of the current project. |
| `scan_secrets` | gitleaks over the project's path, `--redact`; matches are never printed or written to the SARIF. With `SCAN_BASE_REF` set (a pull request) it covers `<base>..HEAD`, otherwise the working tree. |
| `scan_image <ref>...` | `trivy image` on each ref; an argument may hold several, whitespace-separated. Empty: skips with a note, so a project calls `scan_image "${SCAN_IMAGE:-}"` and a local run does nothing. |
| `scan_config <path>...` | `trivy config` (Kubernetes manifests, Terraform, Dockerfiles) over paths in the current directory. |

`.gitleaks.toml` at the repo root allowlists known test fixtures; every entry
carries a reason. Allowlist a file only after reading the values in it.

## Adding scan.sh to a project

```bash
#!/usr/bin/env bash
# Security scan; SARIF to $SCAN_REPORT_DIR. See dev-tools/security/README.md.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. "$(git rev-parse --show-toplevel)/dev-tools/security/scan_lib.sh"
scan_go
scan_secrets
```

Explicit calls, not detection: a project that leaves a scan out says why in a
comment (see `slop-haus/supervisor/scan.sh`, Python, so `scan_lockfile`).
