# Lint

Static analysis, complexity, dead code and duplication for the Go,
TypeScript, Python and shell code. Every tool feeds one ratchet
(`ratchet.sh`), so the findings that were there when a tool arrived are a
committed baseline, a change fails only on new findings, and fixed findings
must leave the baseline: it only shrinks.

## Before pushing: `lint.sh --changed`

```sh
dev-tools/lint/lint.sh --changed     # what the branch changed, and the repo-wide tools
dev-tools/lint/lint.sh PATH...       # the projects those files or directories are in, or under
dev-tools/lint/lint.sh               # everything
```

It lints the projects (`projects.txt`) with files changed against the
merge-base with `main` (or `origin/main`, where nearer; `LINT_BASE`
overrides): commits, staged and unstaged edits, untracked and deleted files.
A file goes to its project's runner by what it is: Go to `go.sh`,
TypeScript or JSON to `oxlint.sh`, Python to `ruff.sh`, a shell script to
`shell.sh`; a linter's own config here re-lints every project it applies to.
knip, jscpd, shellcheck and shfmt run every time.

| Option | |
| --- | --- |
| `--no-repo` | skip knip, jscpd, shellcheck and shfmt (shell scripts among the paths still get the last two) |
| `--repo-only` | only those four: CI's `lint` job |
| `--prune`, `--rebaseline`, `--only=TOOL[,TOOL]` | passed to every runner; `--only` skips the runners with none of the tools |
| `-j N` | runners at once (default 4) |
| `-v` | every runner's output, not only a failed one's |
| `--dry-run` | the commands, not run |

It exits non-zero if any runner failed, naming each, and prints a time per
runner. Warm (tools built, images pulled), on an idle 20-core machine: a
change to one Python or TypeScript project takes about 3s (shellcheck over
every script is most of it, knip 2s cold and 0.5s from its cache), one to
the server's Go about 22s (its runner alone), and everything about 25s
(19s with `-j 8`). The first run builds the Go tools and pulls the images,
a few minutes.

**Local = CI.** CI runs every project's `test.sh`, each of which runs that
project's runner exactly as `lint.sh` does (`lint.bats` checks that every
line of `projects.txt` is so, and that `projects.txt` has every Go module
and Python file), and its `lint` job runs `lint.sh --repo-only`. So CI runs
what `lint.sh --changed` runs, and more: the projects a change did not
touch. The other way, the repo-wide tools in every `test.sh`, would have
needed the root's full `npm ci` in every job and run knip a dozen times.

**oxlint on the host.** In CI it runs inside the frontend and js-components
test images, whose `npm ci` is the root's, of every workspace. `lint.sh`
runs it on the host instead, where it finds the same with the same install:
both areas pass their ratchet, written by the images, run on the host, and
the ratchet is an exact multiset. With less (`npm ci --workspaces=false`, or
a lockfile changed since the install) the type-aware rules quietly find
less, so `lint.sh` first checks `node_modules` against the lockfile
(`npm_complete.mjs`) and fails oxlint and knip, saying why, if it differs.

## What runs where

| Tool | Version | License | Checks | Runs from |
| --- | --- | --- | --- | --- |
| staticcheck | 0.8.2 | MIT | its default checks | `go.sh`, from every Go module's `test.sh` |
| errcheck | 1.30.0 | MIT | an error dropped any way but `_ =` | `go.sh` |
| gosec | 2.30.0 | Apache-2.0 | security rules, not tests; not G104 (errcheck's) | `go.sh` |
| gocognit | 1.3.0 | MIT | cognitive complexity over 20, not tests | `go.sh` |
| revive | 1.17.1 | MIT | function length, nesting, context first, argument count (`revive.toml`) | `go.sh` |
| deadcode | x/tools 0.52.0 | BSD-3-Clause | exported code no main reaches | `go.sh`, for a module with a `deadcode/` directory |
| oxlint | 1.74.0 | MIT | `oxlintrc.base.json`, type-aware | `oxlint.sh`, from `slop-haus/frontend/native_test.sh` and `js-components/native_test.sh unit` |
| oxlint-tsgolint | 7.0.2005 | MIT | the type-aware rules, for oxlint | with oxlint |
| knip | 6.44.1 | ISC | unused files, exports, types, dependencies (`/knip.jsonc`) | `repo.sh` |
| jscpd | 5.4.2 | MIT | duplicated Go and TypeScript (`/.jscpd.json`) | `repo.sh` |
| ruff | 0.17.3 | MIT | `ruff.toml`: pyflakes, pycodestyle errors, bugbear, complexity, dead code | `ruff.sh`, from the supervisor's, token's and harness suite's `test.sh`, the server's lint suite (the workspace scripts) and `go-db/test.sh` (its `e2e_test`) |
| shellcheck | 0.11.0 | GPL-3.0: approved as a dev-only tool, run from its image and never shipped | every check, following `source` (`-x`) | `shell.sh` |
| shfmt | 3.14.1 | BSD-3-Clause | check-only (`-d`), `-i 2 -ci -sr` | `shell.sh` |
| lizard | 1.25.0 | MIT | complexity report, not a gate | `complexity_report.sh`, by hand |

`repo.sh` (knip, jscpd) and `shell.sh` (shellcheck, shfmt) are the
repo-wide runners, from CI's `lint` job through `lint.sh --repo-only`.

The Go tools are pinned by `tool` directives in `go.mod` here, a module of
its own outside the root `go.work` (it is built with `GOWORK=off`, so its
dependencies never mix with the apps'). `go.sh` builds them once into
`~/.cache/slop-factory/lint-go-<hash of go.mod and go.sum>`. The npm tools
are exact-pinned root devDependencies; lizard is pinned by `lizard/uv.lock`
and ruff by `ruff/uv.lock`, both run through uv as the Python projects run
their own tools, so every project gets the same ruff. shellcheck and shfmt
run from images pinned by digest in `shell.sh`: one pin covers every
platform, where static binaries would need a checksum per OS and
architecture, and docker is already what most suites need. shellcheck runs
in eight containers at once (`SHELLCHECK_JOBS`), which halves its time.

**ruff, per project.** `ruff.sh` lints a project's `*.py`, tracked or
untracked, less the linguist-generated copies of `slop_haus_token.py` and
any project nested below it, with `ruff.toml` for all (a project's own
`[tool.ruff]` does not apply). The target Python is the project's
`requires-python`, or 3.9, the oldest the workspace scripts run on, for a
project without a `pyproject.toml`. Lint only: it never formats, and
pyright stays the type checker.

**shell, repo-wide.** `shell.sh` lints every `*.sh`, `*.bash`, `*.bats`
and extensionless file with a sh/bash shebang, tracked or untracked, less
linguist-generated ones (the golden scripts under `testdata/golden`) and
symbolic links. The repo was formatted with shfmt once, so its baseline is
empty and a script off the style fails. shfmt keeps the syntax tree as it
parses it, which is not always as bash runs it; two lessons from that pass:

- An associative array's keys are arithmetic to shfmt, which cannot know
  the array is one: `[X-Frame-Options]=` becomes `[X - Frame - Options]=`.
  Quote them: `["X-Frame-Options"]=`.
- It splits `main "$@"; exit $?` onto two lines, and a script that is
  rewritten while it runs (the workspace's `update.sh`) then reads its next
  line from the new file. Wrap the two in `{ ...; }`, which bash reads whole
  before running.

**Go on the host, not in the test containers.** Linting needs Go and no
database, and every CI job that runs a Go module's `test.sh` already has Go
on the host (`.github/actions/setup`). The modules lint through the root
`go.work`, as the apps build. The server's `test.sh` runs it as a fourth
suite beside the containerised ones (`SUITE=lint`).

**oxlint in the test images**, inside `native_test.sh`: the type-aware rules
need every package's types, which only the full `npm ci` the images make
has. On a CI host with only the root tools installed they would quietly
find less.

## The Go thresholds

- **gocognit 20.** SonarQube's 15 was set for languages without Go's
  `if err != nil { return }` after most calls, each of which scores a point
  (and more when nested in a loop). At 20 a function with ordinary error
  handling and one real decision passes; one that nests loops and branches
  three deep does not. 15 would have doubled the baseline (208 functions
  against 114) without catching a different kind of function.
- **revive**: at most 60 statements or 100 lines per function (not in
  tests, where a table of cases is long and still clear), 4 levels of
  control nesting, 6 parameters (the repo's rule is to group a long
  argument list into a struct), and `context.Context` first.
- **oxlint**: `max-depth` 4, `max-params` 4, `max-lines-per-function` 80
  (not in tests or stories), `complexity` (cyclomatic) 15.
- **ruff C901 15.** McCabe's measure is oxlint's `complexity`, so the same
  15 (and lizard's report flags the same). ruff's default 10 would baseline
  24 functions where 15 baselines 3, each far over (28, 29, 37): the
  difference is long `if`/`elif` dispatches that read fine.
- **ruff's rules**: its defaults (pyflakes, which has the dead-code
  checks for unused imports and variables, and pycodestyle's errors),
  bugbear, `ERA` (commented-out code) and the unused-argument checks for
  functions, class and static methods (`ARG001`, `ARG003`, `ARG004`). Not
  `ARG002` or `ARG005`: methods overriding a protocol and lambdas passed as
  callbacks keep arguments they do not use, which here is most of the 160
  they would report (the fakes in the tests, above all).

## A finding

One line, no line numbers, so an edit elsewhere in the file does not churn
the baseline:

```
internal/landing/ciwait.go gocognit (*ciWait).awaitChecks
cmd/api/board.go function-length
src/app.tsx eslint(complexity)
src/slop_haus_supervisor/cli.py C901 main
slop-haus/agent_workspace.sh SC2012
slop-haus/dev_env.sh shfmt
slop-haus/frontend/src/lib/layout.ts exports breakpoints
go-auth/email/outbox.go clone slop-haus/server/internal/user/outbox.go
```

errcheck's symbol is the dropped call's source text (`defer f.Close()`), so
editing that line without fixing it -- renaming the variable, say -- makes
it a new finding and its old entry stale: fix it while you are there.

The baseline is a multiset: a file with three of the same finding lists the
line three times, and a fourth fails. For jscpd that pins the number of
clones between each pair of files, not the duplicated-lines percentage,
which moves whenever code is added anywhere.

| Baseline | Holds |
| --- | --- |
| `<module>/lint_baseline/<tool>.txt` | a Go module's findings, paths relative to it |
| `<app>/deadcode/baseline.txt`, `allowlist.txt` | deadcode's (`slop-haus/server`, `slop-review`) |
| `slop-haus/frontend/lint_baseline/oxlint.txt`, `js-components/lint_baseline/oxlint.txt` | oxlint's per area |
| `<project>/lint_baseline/ruff.txt` | a Python project's, paths relative to it |
| `dev-tools/lint/baseline/knip.txt`, `jscpd.txt`, `shellcheck.txt`, `shfmt.txt` | the repo-wide ones, paths from the root |

A deadcode allow-list line is `<file> <symbol> #<ticket>`: code landed
unwired on purpose, whose ticket wires it and deletes the line.

## Running one tool

```sh
dev-tools/lint/go.sh slop-haus/server                       # every Go tool, one module
dev-tools/lint/go.sh --only=gosec,errcheck go-auth          # some of them
dev-tools/lint/oxlint.sh slop-haus/frontend                 # oxlint, one area (needs npm ci)
dev-tools/lint/repo.sh --only=knip                          # knip or jscpd (needs npm ci)
dev-tools/lint/ruff.sh slop-haus/supervisor                 # ruff, one project (needs uv)
dev-tools/lint/shell.sh --only=shellcheck                   # shellcheck or shfmt (needs docker)
slop-haus/server/deadcode/check.sh                          # deadcode, the server
dev-tools/lint/complexity_report.sh [path...]               # lizard -> security-reports/lizard-*.{csv,txt}
```

For a tool's full output, with line numbers: run it directly, e.g.
`cd go-auth && "$(ls -d ~/.cache/slop-factory/lint-go-*/ | head -1)"staticcheck ./...`,
`node_modules/.bin/knip`, `cd js-components && ../node_modules/.bin/oxlint --type-aware .`, or
`uv run --project dev-tools/lint/ruff ruff check --config dev-tools/lint/ruff.toml slop-haus/token`.
To format a script: `docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/mnt" -w /mnt mvdan/shfmt:v3.14.1 -w -i 2 -ci -sr <file>`.

## Updating a baseline

**After fixing debt**, prune: it removes the fixed entries and never adds
one, so it is always safe to run and commit.

```sh
dev-tools/lint/go.sh --prune --only=<tool> <module>
dev-tools/lint/oxlint.sh --prune <area>
dev-tools/lint/repo.sh --prune --only=<knip|jscpd>
dev-tools/lint/ruff.sh --prune <project>
dev-tools/lint/shell.sh --prune --only=<shellcheck|shfmt>
dev-tools/lint/lint.sh --prune [PATH...]                    # or every runner a path needs
```

**A new finding is not a baseline entry.** Fix it. When it is deliberate,
suppress that one place, with the reason, in the tool's own syntax
(golangci-lint's `//nolint` means nothing here):

| Tool | Suppression |
| --- | --- |
| staticcheck | `//lint:ignore SA1019 <reason>` above the line |
| errcheck | none: write `_ = f()` (in a defer, `defer func() { _ = f() }()`), which is the repo's rule anyway |
| gosec | `// #nosec G304 -- <reason>` on the line |
| gocognit | `// gocognit:ignore` above the function, with the reason |
| revive | `//revive:disable-next-line:<rule> <reason>` |
| deadcode | an allow-list line with its ticket |
| oxlint | `// oxlint-disable-next-line <rule> -- <reason>` |
| knip | an `ignore`/`ignoreDependencies` entry in `knip.jsonc`, with a comment |
| jscpd | `// jscpd:ignore-start` ... `// jscpd:ignore-end`, with the reason |
| ruff | `# noqa: C901 -- <reason>` on the line (the `def` line for C901) |
| shellcheck | `# shellcheck disable=SC2086 # <reason>` above the line, or before the first command for the whole file |
| shfmt | none: format the script |

**Regenerating** -- a tool upgrade that reports differently, a rule change,
or another branch's findings landing first -- is one command per tool, and
its commit says which and why:

```sh
dev-tools/lint/go.sh --rebaseline --only=<tool> <module>
dev-tools/lint/oxlint.sh --rebaseline <area>
dev-tools/lint/repo.sh --rebaseline --only=<knip|jscpd>
dev-tools/lint/ruff.sh --rebaseline <project>
dev-tools/lint/shell.sh --rebaseline --only=<shellcheck|shfmt>
slop-haus/server/deadcode/check.sh baseline
```

Review the diff before committing it: a rebaseline that adds lines is
accepting debt.

## Tests

`bats/run.sh dev-tools/lint` (Go, uv, docker and the root `npm ci`
needed): the ratchet itself (`ratchet.bats`), `go.sh` against scratch
modules with the real tools (`go.bats`), `oxlint.sh` and the report
adapters (`ts.bats`), `ruff.sh` and `shell.sh` against scratch projects
with the pinned tools (`ruff.bats`, `shell.bats`), and `lint.sh`'s routing,
`--changed` and flags against a scratch repo with stub runners, and the
real `projects.txt` against the repo (`lint.bats`).
