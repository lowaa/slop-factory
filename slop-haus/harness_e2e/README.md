# harness_e2e

Runs the **real harness binaries** (Claude Code, pi and Codex) in containers, started
the way they are started in production (a one-slot **supervisor** for a worker,
`.slop-haus/run_remote_control.sh` for a Remote Control) after the **real setup flow**,
against a real slop-haus stack and a **scripted model server**. The fakes in
the supervisor package's own tests, `pi-extension.test.ts` and the launcher bats encode
our assumptions about a harness; this is what notices when a harness release
breaks them.

## What it proves, and what it does not

Proves, for each scenario: the harness starts from its supervisor (or the Remote Control
launcher; for the host, with the board's harness config applied); it is offered slop-haus's MCP tools; the
briefing reaches it; the supervisor's nudge sends a worker that stopped early back to work;
the board ends up showing what happened (ticket claimed and handed to review, the
session's harness, usage).

Says **nothing about model quality**. The model's replies are a script, so no
real model is called, no credential is involved (the harness gets a dummy API
key), and nothing in CI is configured with the ChatGPT or any other login.
The harness containers have no internet: they reach the slop-haus frontend and
the scripted model server, and `scenarios/test_network_isolation.py` fails if
that stops being true.

## Running it

```
slop-haus/harness_e2e/test.sh                       # every harness, every scenario
slop-haus/harness_e2e/test.sh --harness claude      # one harness
slop-haus/harness_e2e/test.sh --scenario ticket_to_review  # scenarios matching a name (pytest -k)
HARNESS_LATEST=1 slop-haus/harness_e2e/test.sh      # each harness at its latest release
HARNESS_PARALLEL=0 slop-haus/harness_e2e/test.sh    # one harness after another, stopping at the first failure
```

It needs Docker and nothing else, and runs in a CI step the same way
(Docker socket mounted, nothing mounted from the checkout). First it runs the
scripted model server's own unit tests, then brings up the stack
(`docker-compose.yaml` is an overlay on `../docker-compose-dev.yaml`, as in
`../e2e_playwright_ts`: api, Postgres, the seeded dev account, the frontend; no
Dex, since the suite signs in with the seeded password) and runs each
harness's container, which runs the scenarios with pytest.

Each harness gets a stack of its own, and by default they all run at once: the script builds
every image once, then runs itself again per harness, and each of those tags the images as its
own compose project's and brings its stack up from them (`../../dev-tools/shard/shard_lib.sh`,
as `../e2e_playwright_ts` does for its shards). Each harness's output is printed whole, under a
header of its own, once it is done. `HARNESS_PARALLEL=0` (a knob the CI workflows set) runs them
one after another instead, each still on a fresh stack.

When a run fails, the harness's logs (the launcher and harness output of each
scenario) and the api, frontend and scripted-model server logs are copied to
`harness_e2e/artifacts/<harness>/` (`HARNESS_E2E_ARTIFACTS` moves it) and their
tails printed, which is what a CI step's log shows. Every wait is on a
condition with a short timeout (`HARNESS_E2E_TIMEOUT`, seconds, default 30;
`HARNESS_E2E_GATE_TIMEOUT` for a scripted turn held at a gate).

## How it fits together

- `scripted_model/`: the model server, in a container on the test network.
  A **scenario** is an ordered list of **turns** (text and/or tool calls, plus
  scripted usage numbers), independent of any wire API. An API adapter
  (`apis/anthropic.py`: `POST /v1/messages`; `apis/openai_chat.py`:
  `POST /v1/chat/completions`, which pi's custom provider speaks; `apis/openai_responses.py`:
  `POST /v1/responses`, which Codex's custom `model_providers` entry speaks, `wire_api = "responses"`;
  all streaming and not) encodes a turn for its harness. Every request is
  recorded and readable by the test (`/_scripted/requests`), and a request the script did not expect (it ran
  past the end, or asked for a tool the harness was not offered) is an error
  returned to the harness **and** recorded, which the scenarios assert is empty.
  A turn can wait at a **gate** until the test releases it: how a test acts
  on the board between two turns without sleeping. A request with no tools is
  taken to be the harness's own housekeeping (a title, a quota probe) and is
  answered without using up the script. Python's standard library only: it matches the
  workspace scripts' and the host's tests, and a container needs nothing installed.
- `harnesses/`: one **adapter** per harness (`claude.py`, `pi.py`, `codex.py`): its name,
  how it names slop-haus's tools, what makes a headless first run work
  (Claude Code's onboarding and workspace trust in `~/.claude.json`; pi's
  `models.json`, a custom provider over `/v1/chat/completions` in the test
  user's `~/.pi/agent`, which is where a pi with no `PI_CODING_AGENT_DIR` looks), the harness
  config that selects the scripted model (so the board's settings reaching the
  harness's own flags is asserted), the environment that turns its outward
  traffic off (a dummy key; for pi `PI_OFFLINE`, `PI_SKIP_VERSION_CHECK`,
  `PI_TELEMETRY=0`). pi's adapter says `needs_tty`: its extension runs in its TUI, so the
  suite runs it on a pty and stops it itself when its last scripted turn is done.
- `support/`: the test's side. `board.py` plays the signed-in human on the
  API; `workspace.py` does what a person does to onboard a worker: ask for the
  setup script, run it in a fresh git checkout, approve the device sign-in
  (RFC 8628) it prints, with no browser; `harness_run.py` starts a harness process
  a scenario names (the Remote Control host, pi with the host's flags);
  `supervisor_case.py` and `supervised_case.py` set up and run a one-slot supervisor, which is
  how a worker's harness is started; `harness_case.py` is what every scenario
  starts from; `waiting.py` is the one way to wait.
  A later Playwright e2e can reuse the model server and these helpers.
- `scenarios/`: the tests. `test_remote_control.py` is the Remote Control on
  Claude Code, started the way the owner does it: a remote
  control registered through the API, its setup script run in a fresh checkout,
  the device sign-in approved, a harness config of its own saved, the model's
  login (`ANTHROPIC_API_KEY`) in `run.env` and its Claude Code state in
  `.slop-haus/conductor/claude-config`, the host's package installed by
  `.slop-haus/install-app.sh` (offline, from the image's uv cache and interpreter), then
  `.slop-haus/run_remote_control.sh`.
  A round trip over the conversation API asserts what the board shows (the
  reply, the repo state, usage attributed to the remote control with the
  configured model) and what the model was sent (only slop-haus's MCP tools, no
  auto-memory, none of the owner's own `~/.claude`, `Read` of
  `.slop-haus/credentials.json` denied, the second message in the same
  session), then SIGTERMs the host with a turn in flight and expects no Claude
  Code process left. A login that exists only in the owner's `~/.claude` must
  not log the session in. The `test_negative_*` tests are tests of the suite:
  each breaks one thing in the checkout's installed copy of the host package and expects the
  round trip to fail at the assertion about it (`test.sh -rP` prints the
  messages). `support/remote_control_case.py` is what the Remote Control scenarios share.
  `test_remote_control_pi.py` is the same on pi (`run_remote_control.sh --harness pi`): the board's
  provider, model and thinking arriving as pi's flags (a `pi` shim on `PATH` records them, as pi
  retitles its process), the provider configured in the remote control's own pi agent directory
  (`PI_CODING_AGENT_DIR`) with its key in `run.env`, exactly the host's tool allowlist offered to
  the model, none of the owner's `~/.pi/agent` or the repo's `.pi` (extensions, instructions,
  settings) loading, the credential read refused, the second message resuming the same
  `--session-id` session after the host was restarted, usage posted once (the persisted baseline)
  and metered, and no pi process left after SIGTERM. It uses one registration for all its
  tests (each spends the board's rate limits, which the whole run shares), restoring the checkout
  between them; a host still running at the end of a test is stopped with SIGTERM, so it
  releases its place and the next test's host is not refused. `test_pi_extension.py` is pi's: what a real pi
  does with the extension's tool registration and credential guard, started the
  way the Remote Control host starts it. `test_supervisor_api_restart.py` is the headless supervisor (`slop-haus/supervisor`) riding out an api
  restart: set up by the board's setup script, a fresh slot signed in by `slot add`, started in the
  foreground against a fault proxy that has the api down while a run ends. It is Claude Code only,
  and its negative test takes the supervisor's retrying out of the root's copy of the package.
  `test_supervised_ticket_to_review.py` is
  the first story: the supervisor claims a ticket and runs Claude Code on it, the model stops
  early (the supervisor's nudge resumes the session), edits a file and hands the ticket to review
  with ticket_finish. It asserts the board's handoff and usage and what the model was shown
  (the tools, the briefing as the system prompt); its negative test takes the nudge out of the
  root's copy of the supervisor. It also runs on pi (`harness = "pi"` in the root's `supervisor.toml`,
  opt-in for a real slot), which is what a pi slot has to pass before a real one runs it; the other supervised
  scenarios are about Claude Code and skip it. Nothing installs pi's extension at a supervisor root yet, so
  `use_pi` copies the one slot's `.slop-haus` there (see `unified-provisioning.md`). A pi slot's supervisor
  logs the same container/VM warning as pi Remote Control.
  `test_supervised_remote_control.py` (Claude Code only) is the Remote Control the owner adds on the board and the
  supervisor runs: `login` signs the supervisor in, the board's desired state lists the remote control, and the
  supervisor enrols it, makes its workspace and starts the host, which must show in the owner's Remote Control list,
  come online and answer a message. Its negative test starts the host outside its workspace in the root's copy of the
  supervisor.
  `test_interactive_hooks.py` (Claude Code only, **opt-in**: `HARNESS_E2E_INTERACTIVE_HOOKS=1`, which the
  `@latest` run sets, so it is not in the gating run yet) is the one interactive session: Claude Code on a pty with
  `SLOP_HAUS_HOST_SESSION` unset, in a workspace `setup-workspace` made, so the workspace's own hooks run (every other Claude
  Code scenario sets that variable, which turns them off). The Claude Code protocol the hooks depend on is asserted
  against the real binary: SessionStart's briefing reaches the model and binds the session, the Stop hook's exit 2 sends the
  agent back to work while it holds a ticket (with the server's reason), and the usage hook posts. Its negative test breaks
  `hook-stop.sh`'s exit code. It starts Claude Code with no prompt and types it once the MCP server is connected
  (an interactive Claude Code offers a server's tools only to requests made after it connects), with `ENABLE_TOOL_SEARCH=false`
  so slop-haus's tools are not deferred behind ToolSearch.
  The launcher's update on start is `run_remote_control.bats`'s, and an old workspace's migration the Go `migration_test.go`'s.
- **Hook payload fixtures**: the same scenario keeps what Claude Code sent each hook. `test.sh --capture-hook-payloads
  server/internal/hookserver/testdata/claude` runs it and writes them, with the run-specific fields (session id, paths,
  prompt id) replaced (`support/hook_fixtures.py`). The Go tests of `hookserver` and of the hook scripts
  (`workspace/*_hook_test.go`) and `e2e_playwright_ts/tests/hooks.spec.ts` read them. Re-capture after a Claude Code bump
  and commit the diff: it is what changed in the protocol. Payloads a real session does not produce stay hand-written, where
  they are used. Not captured: SubagentStop (the scenario starts no subagent) and a transcript for the usage hook, whose
  tests still build their own.
- `support/fault_proxy.py` and `scenarios/test_mcp_reauth.py` (Claude Code only): a reverse
  proxy thread in the test process, in front of the frontend, that can be armed to fail the next
  requests for a method and path and records what it did (its own tests: `test_fault_proxy.py`).
  A 401 is the API's genuine one (the request is forwarded with a garbage bearer), a 404 "session
  not found" likewise (an unknown `Mcp-Session-Id`); only a 502 is made up, in nginx's shape.
  The scenario points a one-slot supervisor's server (so the MCP config's url and `headersHelper -server=`)
  and the slot's `server-url` at the proxy, puts a `claude` wrapper that adds `--debug` first on the
  supervisor's PATH, and replays what stranded workers on a deploy: the scripted model makes a tool call, waits at a gate while the test arms
  the proxy, calls again, and waits at a gate while the test disarms it before the call that
  decides. The control case (a 401 alone) must recover. A (502s on the reconnect), B (404 session
  not found on the reconnect) and C (expired credentials and `/oauth/token` 502, so
  `token.sh headers` fails) leave Claude Code in needs-auth ("falling through to needs-auth" in
  its `--debug` log); the tests asserting recovery are expected failures for A and B, but not C:
  `token.sh` retries the refresh, which the server's reuse grace window makes safe; and a separate test per
  case asserts the faults fired, so a run in which they did not can't pass for the known bug.
  Each case is a fresh supervisor and slot (one sign-in each, four in all).
- `Dockerfile`: the scripted model server's image (and its test stage) and one
  image per harness, on the same Debian release as the server Dockerfile's
  `workspace-test` stage. `Dockerfile.dockerignore` whitelists what each copies.

## Codex

`harnesses/codex.py` is the adapter (no tty, shell tool `exec_command`, slop-haus's tools offered as the children of
the `mcp__slop_haus` namespace under their own names, the effort read from `reasoning.effort`) and `harness-codex` the
image (`CODEX_VERSION` in `harness_versions.env`). The scripted model is a custom provider in the test user's
`CODEX_HOME/config.toml`, with a model name Codex has no metadata for: a model it knows gets a JavaScript `exec` tool
and its tools in an `additional_tools` input item, which the script does not play.

- `scenarios/test_codex.py` is the real `codex app-server` over stdio with the test's own client
  (`support/codex_app_server.py`): usage is thread-cumulative and a resume replays where the thread stood before the
  turn starts (so the baseline is not `total - last` of the first notification), the turn's effort reaches the
  request, a thread with no rollout is refused with the words the supervisor matches, a turn is interrupted while the
  model is still answering, and an MCP header helper is run again when a call is answered 401 (`support/header_mcp.py`).
- `scenarios/test_codex_steward.py` is the Codex steward's confined shell on the real bubblewrap sandbox, through the
  supervisor's own policy, probe and harness (`support/codex_steward_driver.py`, run under the root's copy of the
  package). The container is started with Docker's seccomp and AppArmor profiles off, which bubblewrap needs for user
  namespaces. The negative runs take one protection out of the policy (a canary in a forbidden place readable, a secret
  variable visible, a writable directory, an enabled network): the probe must refuse each, and a run of that steward
  must show it. With the confinement right a run reads the reviewed checkout's diff and files and nothing else, and is
  offered exactly the allowlisted slop-haus tools; `git` and `cat` are found by name in the scrubbed environment.
- The supervised scenarios run on Codex only where they set `runs_on_codex` (so far the steward's); the supervisor's
  registry has a `codex` entry, and `use_codex` stands in for the worker's MCP credential with an
  `http_headers_helper` in the `CODEX_HOME`'s config.
- `real_model/check_codex.py` is what needs a real model, run by hand on a host with a ChatGPT login (not by `test.sh`,
  not per change; it spends a few short turns of quota): the login is ChatGPT, a real turn's items and the requests it
  provokes of a headless client, real usage with the field names the supervisor reads, the resume replay, an interrupt
  while a command runs, and a read-only thread refusing a write. Its JSON report goes on the ticket.

## Adding a scenario

Add `scenarios/test_<name>.py`. Build the `Scenario` from the adapter's tool
names (`adapter.mcp_tool("ticket_next")`, `adapter.bash_tool`) so it runs on
every harness, `ModelClient.load` it, start it (a worker through `SupervisedCase`, anything else with `HarnessRun`), wait on
the board or the recordings, and assert on both. Every workspace a scenario sets up costs a device-code lookup, and the board
allows 20 in 10 minutes per user for a harness's whole run (each harness has a stack of its
own), all of its scenarios together: spend registrations sparingly (play one conversation and make
several assertions on it, as `test_negative_what_reaches_the_session` does).
Run the new test against
deliberately broken code at least once (a negative run) to see its failure
message says what is wrong.

## Adding a harness

1. `harnesses/<name>.py`: an adapter implementing `harnesses/base.py`'s
   `HarnessAdapter`, registered in `harnesses/__init__.py`.
2. A stage in `Dockerfile` (`harness-<name>`) installing the harness at
   `${<NAME>_VERSION}` and setting `HARNESS` and `HARNESS_VERSION`, and a
   `harness-<name>` service in `docker-compose.yaml` beside `harness-claude`.
3. The version in `harness_versions.env`, and the harness's name in
   `ALL_HARNESSES` in `test.sh`.
4. If it speaks another model API, an `ApiAdapter` in `scripted_model/apis/`
   (with its unit tests), listed in `server.py`.

## Bumping a pin

Edit `harness_versions.env`, run `test.sh`, and commit the new version with
whatever the release made the suite change. The suite fails if the harness
reports a different version than the pin, so a pin that did not take is
noticed.

## The `@latest` run

`HARNESS_LATEST=1 ./test.sh` builds each harness at its latest release instead
of the pin, to see what the next bump will break before making it. It is not
part of the pull-request checks.
