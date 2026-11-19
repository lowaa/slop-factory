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

A fresh checkout needs the root `npm ci` first: bats and the frontend's
stylelint ratchet come from it.

## macOS (Apple silicon)

The suites run on an arm64 Mac as well as on CI's Linux. The images they
build are arm64 there; the one exception pulled is `mailhog/mailhog`, which
has no arm64 build and runs emulated (Docker Desktop's Rosetta or QEMU must
stay on). What differs is the host:

- **Docker Desktop** (or another daemon reachable at `/var/run/docker.sock`).
  The first run of each suite builds its images cold, so expect it to take
  several times longer than the next.
- **bash 5**, first on `PATH`: `brew install bash`. macOS's `/bin/bash` is
  3.2: the lint runners, `bats/run.sh` and `slop-haus/server/test.sh` stop
  with a message saying so (`dev-tools/require_bash.sh`); other scripts may
  just fail.
- **flock**: `brew install flock`. macOS has no `flock(1)`; the
  supervisor's tests and the workspace scripts a supervisor host runs
  (`update.sh` among them) call it.
- Go, Node 22 and uv on the host for the suites that run outside Docker
  (`slop-haus/supervisor`, `slop-haus/token`, `slop-review`, the Go
  libraries' lint), as on Linux.

The host-side scripts stick to what BSD and GNU tools share: a GNU-only flag
(`find -printf`, `mv -T`, `stat -c`, `date -r FILE`, `realpath
--relative-to`) or a read of `/proc` breaks here. CI's `macos` job runs the
host-side suites listed in `ci.yml` on an Apple-silicon runner to catch that;
the Docker suites run on Linux only.

The supervisor runs on macOS, with one gap Linux does not have: macOS hides
the environment of Apple's own binaries (`/bin/bash`, `/bin/sleep`, ...)
from `ps -E`, so a run's processes are found through the token in their
environment and through their parents, when the run is stopped. A
background job whose shell had already exited by then (an Apple binary such
as `sleep` left running by an earlier command) is not found;
see `RunProcesses` in `slop-haus/supervisor/src/slop_haus_supervisor/harness/run_processes.py`.

## Images

Once ci passes on `main` (every job but the advisory `macos`), its last job pushes `ghcr.io/<owner>/slop-haus-api`
and `ghcr.io/<owner>/slop-haus-frontend` as `sha-<12 hex>` and `main`; a `v*`
tag pushes them as that tag (`.github/workflows/publish_images.yml`).
`.github/workflows/prune_images.yml` trims old `sha-*` versions weekly and
never touches `v*` or `main`; run it by hand for a dry run.

After changing a route or a request/response body, run
`slop-haus/frontend/generate_api_client.sh` and commit the result.

Security scans are report-only: `<project>/scan.sh` (see
`dev-tools/security/README.md`), and so is the complexity report,
`dev-tools/lint/complexity_report.sh`.
