# Git
- Never rewrite history on `main`, local or remote
- Don't change git config, remotes or `gh auth`

# Tests
- Before pushing: `dev-tools/lint/lint.sh --changed` (every linter CI runs on the change, the repo-wide ones included), then the touched project's `test.sh`, `scan.sh` and `quality.sh` (if it has one: `dev-tools/quality/pending.txt` lists those without); a new Go project gets all three (`dev-tools/security/README.md`, `dev-tools/quality/README.md`) and a `dev-tools/lint/projects.txt` line
- Negative-test non-trivial tests: break the code, watch the test fail
- Filter runs to the test you're iterating on
- No timing-based waits: synchronise on events, locks or channels. Override long production timeouts in tests; discuss any test that must be slow
- Bash tests use shellspec (`dev-tools/quality/spec/` is the example); existing bats suites (`bats/`) stay. `test.sh` entrypoints don't need tests
- Coverage, complexity and test time are ratcheted by `quality.sh` against `quality_baseline/` (`dev-tools/quality/README.md`): fix a regression; rebaseline only a deliberate one, alone (`--rebaseline --only=<measure>`), saying why in the commit; after an improvement, `--prune`
- Lint findings are ratcheted, baselines and suppression syntax in `dev-tools/lint/README.md`: fix a new one, don't baseline it; after paying debt, `--prune` the baseline

# Self-review
In a slop-haus supervisor's slot (a headless run), you don't run the reviewer: push, call `review_request` with the change's PR description, and end your turn. The supervisor runs one slop-review pass and resumes you with its verdict and open findings; read them with `review_findings`, validate each finding (step 6), label it with `review_label`, then fix, push and request again, or carry on. Labels never clear a blocker or major finding: the steward approves only a head whose review raised none, so anything else waits for a person. Steps 1, 5 and 6 still apply, and the PR description records the review's verdict and what was fixed or rejected. `review_request` refuses when no supervisor runs you; then, and everywhere else, follow this procedure.

Before calling a change done:
1. Review it yourself, as a reviewer who didn't write it.
2. Write what the change is meant to do (its PR description, without your reasoning or suspicions) to a scratch file outside the repo.
3. Run `slop-review/review.sh --intent <file> > <out>.json` once. It is a fresh session that sees only the intent, the repo and the diff: the branch against its merge-base with `main`, uncommitted work included. On `main`, pass `--from-commit-hash=$(git rev-parse HEAD)`.
   - Run it from the root of the service the change touches (e.g. `slop-haus/server`), so the one pass sees the trust boundaries between its packages
   - Its defaults are the single large-context pass: the `standard` persona over the whole diff in one session, 40 turns, `xhigh` effort where the provider takes one. Other personas are an explicit `--persona=<p>`, never required
   - The default provider is the Claude Code CLI; `review.sh --help` lists the others. Settings come from the flags, the environment, then the repo-root `.env`
   - A fallback provider configured there (`SLOP_REVIEW_FALLBACK_*`) or centrally is the user's choice: slop-review takes it itself, and `.meta.fell_back` says so. If the review still fails (usage limit, lost login, no fallback or the fallback failing too), the change isn't done: say so, and don't switch provider on your own
   - Runs report to slop-haus when `SLOP_REVIEW_REPORT_URL` and a token (`SLOP_REVIEW_REPORT_TOKEN` or `SLOP_REVIEW_REPORT_TOKEN_COMMAND`) are set; otherwise nothing is sent
4. Read `.review.verdict` (`approve`, `approve_with_comments`, `request_changes`, or `incomplete`, which means part of the change went unreviewed and is not an approval), `.review.findings[]` and `.review.warnings[]`.
5. Compare your review with theirs: what both found, what only one found, where you disagree, and why each side missed what it did.
6. Validate every finding before acting, yours included: reproduce it, read the call sites, or write the failing test. Reviewers produce false positives and fabrications, so say which findings you rejected and why. Label each with `slop-review label` (`review_label` in a slot), and record the review's verdict, and what was fixed or rejected, in the PR description.

Reviews don't replace negative test runs.

# Code
- Program like a senior engineer: prefer the maintainable design over the quick fix
- Follow established patterns; raise it when one doesn't make sense
- Remove repetition with common patterns; wrap imported libraries in app-specific types
- Test at the right level (unit, integration, e2e). Before a big refactor, cover the outer interface first
- Comments explain why, not what. No archaeology, no file headers that restate the obvious

# Go
- Close resources on every path; give owning types a `Close()`
- Pass pointers to structs. Use structs as classes; group long argument lists into a struct
- Ignored errors: `_ =` inline; in a defer, wrap in `func() { _ = x.Close() }()`
- Inject the logger (`*slog.Logger` or the project's equivalent); no package-level loggers
- Use testify
- Test what the database decides (queries, constraints, ordering, locking) against a real one (`dbtest`). Layers above it may use a hand-written fake that only returns what it's given and records calls, never re-implements a rule. Use testify/mock only when call expectations are the point of the test

# Python
- uv with `pyproject.toml`; pytest with `unittest.TestCase`; type hints, checked by pyright

# Frontend
- SCSS: an index-level stylesheet, plus component styles beside the component (`button.tsx`, `button.scss`)
- snake_case file and folder names
- Flag code worth hoisting into `js-components/`

# Docker
- Every Dockerfile gets a `Dockerfile.dockerignore` allowlist

# Dependencies and security
- Choose maintained dependencies; no strong copyleft (GPL, AGPL) without approval
- Follow industry security practice, and raise concerns rather than working around them

# Subagents
- On Opus or Fable, give mechanical sub-tasks to Sonnet subagents. Agents that own a whole unit of work keep their own model

# This repo
- Go modules resolve through the root `go.work` (a new module gets a `use` line), not `require`/`replace`. Each app's Dockerfile writes its own `go work init` naming only the modules it may use, so a new library dependency goes there, in the COPY list, and in the `.dockerignore` allowlist
- Migrations: `LATEST` names the newest (`go-db create-migration` writes it). On a `LATEST` conflict, renumber yours after the one that landed and check it still makes sense. Never renumber one already on `main`
- Committed generated files get a `.gitattributes` in their directory naming the generator, with `* linguist-generated=true`, and the generator keeps it in place. List generated files by name if the directory also holds hand-written ones
