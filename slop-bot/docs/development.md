# Development

Read the repo's `CLAUDE.md` first: it sets the conventions (testify,
injected `*slog.Logger`, `Close()` on owners, no timing-based waits, negative
test runs). This file is slop-bot's part. For the tools to install, see
[prerequisites](dev_setup.md).

slop-bot is a Go module (`github.com/lowaa/slop-factory/slop-bot`) in the
repo's `go.work`. Tests use the `go-clock` module's fake clock, never
`time.Now`, and run with `t.Parallel()` and `-race`.

## Repo layout

| Path | What |
| --- | --- |
| `cmd/slop-bot` | the one binary: daemon and CLI |
| `cmd/fakeharness` | the fake harness as a standalone binary |
| `cmd/scripted-model` | the scripted model server as a binary, for tier 2's containers |
| `cmd/gencodexproto` | generates the Go types for Codex's app-server protocol (output marked generated) |
| `internal/cli` | every cobra command, and the generated agent CLI reference |
| `internal/config`, `internal/paths` | the config file and XDG locations |
| `internal/daemon` | the daemon's components and HTTP routes, started and stopped in order |
| `internal/control` | the unix-socket transport, peer-uid check and agent tokens |
| `internal/api` | the request and response types shared by CLI and daemon |
| `internal/agents` | the manager: scheduler, runners, verbs, messages, plan limits |
| `internal/agentspec` | validates a ticket's harness, model and effort |
| `internal/briefing` | the built-in briefing template and its renderer |
| `internal/tickets` | the `TicketingSystem` interface, status rules, the `local` SQLite implementation, and the `conformance` suite every implementation runs |
| `internal/state` | `state.db`: agents, messages, events, cursors, transcripts index, pruning (goose migrations in `migrations/`) |
| `internal/sqlitex` | the SQLite helper (WAL, busy timeout, migrations), with `sqlitextest` giving each test its own file |
| `internal/harness` | the `AIHarness` and `Conversation` interfaces, and the kits and implementations below |
| `internal/worktree`, `internal/gitx` | per-ticket worktrees and the git command runner |
| `internal/transcript` | the common transcript model, a library over the harnesses' readers, and the retained-copy archive |
| `internal/doctor`, `internal/setup`, `internal/service` | `doctor`, `install`'s config and briefing stubs, and the systemd and launchd managers |
| `internal/scriptedmodel` | a model server that plays back a script (see its README) |
| `internal/lifecycle` | components with ordered `Start` and `Stop` |
| `tier2/` | the tier 2 Dockerfile and `harness_versions.env` |
| `quality_baseline/`, `deadcode/` | ratchet baselines |

Under `internal/harness`:

| Path | What |
| --- | --- |
| `agentproc` | an `os/exec` child in its own process group, with the harness environment (denylist) and exit capture |
| `rpcdriver` | the kit behind every programmatic driver |
| `tuidriver` | the kit behind every TUI driver, over `sessionhost` (tmux) |
| `claudecode`, `codex`, `pi` | the real harness adapters |
| `fake` | the fake harness, as `fake/rpc` and `fake/tui`, and its controller |
| `conformance` | the `Conversation` suite every harness runs; `conformance/scripted` runs it against the real CLIs |

The harness-to-driver registry is `builders` in `internal/daemon/harnesses.go`.

## The scripts

All run from `slop-bot/`. CI runs `test.sh` and `quality.sh`, and
`tier2.sh` in its own workflow; `scan.sh` is run locally. Before pushing, per the repo's
`CLAUDE.md`, run `dev-tools/lint/lint.sh --changed` from the repo root, then
`test.sh`, `scan.sh` and `quality.sh`.

| Script | What it runs |
| --- | --- |
| `test.sh [go test args]` | the linters and the deadcode check (`dev-tools/lint/go.sh`), `gofmt -l`, `go vet`, then `go test -race ./...`. Arguments replace `./...`, so `./test.sh -run TestScheduler ./internal/agents` filters. |
| `scan.sh` | the repo's security scan: `scan_go` and `scan_secrets` (`dev-tools/security/README.md`), SARIF to `$SCAN_REPORT_DIR` |
| `quality.sh` | coverage, complexity and test time, ratcheted against `quality_baseline/` (`dev-tools/quality/README.md`). `--prune` after an improvement, `--rebaseline --only=<measure>` only for a deliberate regression, `--report` for the full reports. |
| `tier2.sh` | the real harness CLIs driven through slop-bot's adapters against the scripted model |

### Tier 2

Tier 2 runs real Claude Code, Codex and pi binaries, with the scripted model
answering for the model: no credential, no tokens, no network. The tests are
opt-in (`SLOP_BOT_TIER2=1`), so `test.sh` skips them.

| Invocation | What |
| --- | --- |
| `./tier2.sh [go test args]` | in Docker, each harness at its pin in `tier2/harness_versions.env`, source baked into the image, `--network none` |
| `ATTACH=1 ./tier2.sh` | a shell in that image, offline, with `slop-bot` and `go-clock` mounted, for iterating |
| `NATIVE=1 ./tier2.sh [args]` | on this machine with the harnesses installed (a missing one skips); each gets a temp config dir, so yours are untouched |
| `HARNESS_LATEST=1 ./tier2.sh` | each harness at its latest release instead of the pin |

To bump a harness, edit its line in `tier2/harness_versions.env`, run
`tier2.sh`, and commit the pin with whatever the new release changed.
`doctor`'s tested version ranges are in `internal/doctor/versions.go`; widen
one only after the live and scripted tests pass on the new version.

## Test patterns

**Hand-written fakes, real stores.** Behaviour the database decides runs
against a real SQLite (`sqlitextest`). Layers above use hand-written fakes
that return what they're given and record calls.

**The fake harness.** `internal/harness/fake` speaks a minimal JSON protocol
and is scripted through its own control socket (`fake.Controller`): reply,
ask, hang, crash, ignore an interrupt, go quiet. It records what it
received and decides nothing. It registers as `fake/rpc` and `fake/tui` (the
latter in a per-test tmux server), so the manager and conformance tests run
without a real harness. `faketest` has the per-test rigs
(`faketest.RPCSubject`, `faketest.TUISubject`).

**The re-exec pattern.** A test that needs a child process makes the test
binary itself the child, so there is no build step and nothing to go stale.
The test's `TestMain` checks an environment variable and, when set, runs the
child's `main` instead of the tests:

| Variable | The test binary becomes |
| --- | --- |
| `SLOP_BOT_FAKE_REEXEC=1` | the fake harness (`faketest.Main`, used in `agents`, `fake`, and others) |
| `SLOP_BOT_CLI_REEXEC` | the `slop-bot` CLI, for `internal/cli`'s end-to-end tests |
| `SLOP_BOT_TEST_FAKE_CLAUDE=1`, `SLOP_BOT_TEST_FAKE_PI=1` | a fake `claude` or `pi`, so an adapter's launch, handshake and hand-over are tested without the real CLI |

The fake harness reads its wiring from `SLOP_BOT_FAKE_CONTROL`,
`SLOP_BOT_FAKE_ID`, `SLOP_BOT_FAKE_HOOK_CMD` and `SLOP_BOT_FAKE_TRANSCRIPTS`.

**Synchronise on events.** Tests wait on the event log, a channel or the
fake clock, never on sleeps. The `Timeout` constants are safety nets.

### Live test gates

Tests that spend plan usage or need a real login are skipped unless gated on:

| Variable | Runs |
| --- | --- |
| `SLOP_BOT_LIVE_CLAUDE=1` | the Claude Code adapter against the real `claude`, on your own login |
| `SLOP_BOT_LIVE_CODEX=1` | the same for Codex |
| `SLOP_BOT_LIVE_PI=1` | the same for pi |
| `SLOP_BOT_TIER2=1` | the scripted-model tests against the real CLIs (set by `tier2.sh`) |
| `SLOP_BOT_TIER2_CONTAINER` | set inside the tier 2 image, enabling the container-only checks (network isolation, pinned versions) |

Live runs are manual and not deterministic. Tests that need `tmux` skip when
it isn't installed.

## Adding a harness

A harness is one `AIHarness` implementation under one driver. The core sees
only `harness.AIHarness` and `harness.Conversation`
(`internal/harness/harness.go`).

1. **Pick the driver kit.**
   - Programmatic (stdio, JSON lines): `rpcdriver`. Write a `Codec` for the
     protocol: `Open` (the handshake, returning the conversation id), `Send`
     (a new turn, or a steer when `midTurn`), `Interrupt`, and `Decode`
     (one stdout line to `harness.Event`s: `Ready`, `TurnStarted`, tool and
     text events, `TurnEnded` with an outcome, `LimitReport`). Give it the
     `LaunchSpec` and, for hand-over, an `Attach` function returning the TUI
     command that resumes the same conversation. `rpcdriver` runs the process
     through `agentproc` and handles serialised writes, turn settling and exit.
   - TUI: `tuidriver`. Write a `Profile`: the launch `Command`,
     `InstallHooks` (session-only, never the user's config), the submit and
     cancel keys, screen `Markers` as the fallback, `Quit`, and `ParseHook`
     and `Reply` for the hook payloads. The kit pastes pointers while idle,
     never while a person is attached.
2. **Write the adapter** (`internal/harness/<name>/`): `Name`, `Driver`,
   `Capabilities` (mid-turn steer, chosen ids), `Start` (build the launch
   spec from the `Workspace`: cwd, model and effort, briefing injected
   through the harness's system-prompt channel, `permissive`, the agent
   token in `Env`), and `Transcripts()`, a `transcript.Reader` for the
   harness's session files. `agentproc` strips the denylisted environment.
3. **Register it:**
   - the driver in `drivers` in `internal/config/config.go`
   - the builder in `builders` in `internal/daemon/harnesses.go`
   - the effort levels in `efforts` in `internal/agentspec/agentspec.go`
     (or check them at spawn, as Codex does)
   - the `doctor` probe in `internal/doctor/harness.go`, and the tested
     version range in `internal/doctor/versions.go`
   - install hints per OS in the probe
4. **Run the conformance suite.** `conformance.Run(t, func(t) *Subject)`
   needs a `Subject` (the harness, a fresh `Workspace` per test, a
   `Puppet`, and a timeout). It covers start, an idle and a mid-turn
   message, interrupt, stop idle and forced, resume, and a crash. Run it
   against the fake for the kit (`fake_test.go`), and for the real CLI
   through `conformance/scripted` (the puppet drives the scripted model).
5. **Teach the scripted model the wire API** the harness uses
   (`internal/scriptedmodel`: Anthropic, OpenAI responses and chat, Gemini
   adapters exist), and add the harness to `tier2/Dockerfile` and
   `harness_versions.env`.
6. **Test the adapter's own parts**: codec tests from recorded protocol
   lines in `testdata/`, the transcript reader, and a fake CLI via the
   re-exec pattern. Add a `SLOP_BOT_LIVE_<NAME>` gated test.
7. **Briefing quirks**: the briefing data has a `HarnessNotes` field, rendered
   under "Notes for this harness". No adapter fills it yet; use it for
   behaviour agents should know of.

Keep generated code marked: a `.gitattributes` in its directory with
`* linguist-generated=true` (see `internal/doctor/testdata`).
