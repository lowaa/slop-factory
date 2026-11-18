# Contributing

Every suite has its own `test.sh`, and CI (`.github/workflows/ci.yml`) runs
exactly those, and the repo-wide linters, so a green local run (the touched
suites' `test.sh` and `dev-tools/lint/lint.sh --changed`) is a green CI run.
Most need Docker.

| Suite | Command |
| --- | --- |
| Go libraries | `go-auth/test.sh` (and the same for each `go-*/` and `artifact-store/`) |
| Server | `slop-haus/server/test.sh` (its own Postgres via testcontainers) |
| Lint, before pushing: what the change touched, and the repo-wide tools | `dev-tools/lint/lint.sh --changed` |
| Lint: static analysis, complexity, dead code (Go, TypeScript, Python) | in each suite's `test.sh`; one tool alone: `dev-tools/lint/README.md` |
| Lint across the repo: unused code, duplication, shell scripts | `dev-tools/lint/lint.sh --repo-only` (CI's `lint` job) |
| Frontend | `slop-haus/frontend/test.sh` |
| API client | `slop-haus/frontend/generate_api_client.sh --check` |
| Shared frontend packages | `js-components/test.sh unit`, `js-components/test.sh storybook` |
| Supervisor, token | `slop-haus/supervisor/test.sh`, `slop-haus/token/test.sh` |
| slop-review | `slop-review/test.sh`, `slop-review/report/test.sh` |
| Test tooling | `bats/run.sh --recursive dev-tools` |
| End to end | `SHARDS=2 slop-haus/e2e_playwright_ts/test.sh` |
| Harnesses against a scripted model | `slop-haus/harness_e2e/test.sh` |
| Deployment examples (compose, Helm, Argo CD), static checks | `deployment_example/test.sh` |

The soak test, the lists-at-scale e2e run and the harnesses at their latest
releases run nightly (`.github/workflows/nightly.yml`), not on pull requests.

## Images

Once ci passes on `main`, its last job pushes `ghcr.io/<owner>/slop-haus-api`
and `ghcr.io/<owner>/slop-haus-frontend` as `sha-<12 hex>` and `main`; a `v*`
tag pushes them as that tag (`.github/workflows/publish_images.yml`).
`.github/workflows/prune_images.yml` trims old `sha-*` versions weekly and
never touches `v*` or `main`; run it by hand for a dry run.

After changing a route or a request/response body, run
`slop-haus/frontend/generate_api_client.sh` and commit the result.

Security scans are report-only: `<project>/scan.sh` (see
`dev-tools/security/README.md`), and so is the complexity report,
`dev-tools/lint/complexity_report.sh`.
