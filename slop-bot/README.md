# slop-bot

slop-bot is a local daemon that runs a coding agent on each ticket, for the
ticket's whole life. One binary is the daemon and the CLI. The daemon runs
as a per-user service (systemd on Linux, launchd on macOS) and talks to its
CLI over a unix socket.

For each ticket, an agent:

- refines the ticket, and sends risky decisions to a human (`discuss`)
- works in its own git worktrees
- reviews, opens PRs, and merges only when the ticket has `AutoMerge`
- decides when the ticket is done

Tickets are stored in a local SQLite database ("local mode"). A person drives
everything through the `slop-bot` CLI, typically from a controller AI
session.

## Harnesses and drivers

A harness is a coding-agent CLI. A driver is how slop-bot drives it.

| Harness (`harnesses.<name>`) | Driver | Notes |
| --- | --- | --- |
| `claude-code` | `stream-json` | |
| `codex` | `app-server` | |
| `pi` | `rpc` | |
| `agy` | `tui` (in tmux) | agy's own TUI in slop-bot's tmux server (`tmux -L slop-bot`); needs tmux. Its sessions outlive a daemon restart, and `attach` joins them. On a consumer Google sign-in, see `doctor`'s terms warning; `api_key_opt_in` with `GEMINI_API_KEY` runs it on a key instead |

The merge guard covers all four ([merge guard](docs/merge_guard.md)).

## Quickstart

You need `git` 2.36 or later and at least one harness CLI, signed in. See
[prerequisites](docs/dev_setup.md#prerequisites) for per-OS install lines.

```sh
# Check the machine, write a commented config and a stub briefing, install
# and start the service. Repeatable: it keeps your edits.
slop-bot install --harness claude-code --repo myrepo="$HOME/src/myrepo"

# File a ticket and assign it to yourself, so an agent picks it up.
slop-bot ticket create --title "Fix the flaky login test" \
  --repo myrepo --assign --body "login_test.go fails about 1 run in 20"

slop-bot status                  # every agent: ticket, state, progress note
slop-bot watch <agent|ticket>    # follow one live
slop-bot tell <agent|ticket> "use the existing retry helper"
slop-bot asks                    # questions agents are waiting on
slop-bot tell <agent> "yes, use A" # a user tell is the answer to an open ask
```

`slop-bot doctor` re-checks the machine at any time, and changes nothing.

`slop-bot attach <agent|ticket>` takes an agent's session over in its
harness's own TUI, in your terminal; quitting the TUI hands the agent back.
`slop-bot detach` hands it back from another terminal. `slop-bot tui` is a
live, read-only table of every agent, and `slop-bot transcript` reads an
agent's conversation. See [concepts](docs/concepts.md#attach-and-hand-back).

Worktrees that hold work outlive their ticket: `slop-bot worktrees` lists
them and `slop-bot worktrees clean <ticket>` removes them.

## Documentation

- [Configuration](docs/configuration.md): every config key, its default, an
  annotated example
- [CLI](docs/cli.md): every command and flag, user versus agent commands
- [Concepts](docs/concepts.md): the agent lifecycle, declarations, slots,
  the autonomous loop, transcripts and retention, plan-limit holds, the
  briefing, and the trust model
- [Merge guard](docs/merge_guard.md): what it refuses, and how each harness
  enforces it
- [Development](docs/development.md): repo layout, the test scripts and
  tiers, mutation testing, the fake harness, and how to add a harness
- [Prerequisites](docs/dev_setup.md): what a machine needs, per OS

## Where files live

`slop-bot paths` prints the resolved locations. They follow the XDG Base
Directory spec on Linux and macOS alike; see
[configuration](docs/configuration.md#where-slop-bot-keeps-its-files).
