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

| Harness (`harnesses.<name>`) | Driver | Status in this build |
| --- | --- | --- |
| `claude-code` | `stream-json` | runs |
| `codex` | `app-server` | runs |
| `pi` | `rpc` | runs |
| `agy` | `tui` (in tmux) | accepted by the config, not runnable yet: `doctor` warns, and its tickets become `misconfigured` |

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

`slop-bot attach` (taking over an agent's session by hand) is designed, and
described in [concepts](docs/concepts.md#attach-and-hand-back), but this
build has no `attach` command yet.

## Documentation

- [Configuration](docs/configuration.md): every config key, its default, an
  annotated example
- [CLI](docs/cli.md): every command and flag, user versus agent commands
- [Concepts](docs/concepts.md): the agent lifecycle, declarations, slots,
  the autonomous loop, transcripts, plan-limit holds, the briefing, and the
  trust model
- [Development](docs/development.md): repo layout, the test scripts, the
  fake harness, and how to add a harness
- [Prerequisites](docs/dev_setup.md): what a machine needs, per OS

## Where files live

`slop-bot paths` prints the resolved locations. They follow the XDG Base
Directory spec on Linux and macOS alike; see
[configuration](docs/configuration.md#where-slop-bot-keeps-its-files).
