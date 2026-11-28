# Configuration

slop-bot reads one YAML file, `slop-bot-config.yaml`. `slop-bot install`
writes a commented default where there is none, and never overwrites it.
`--config <path>` on any command points at another file. The file `install` writes sets only
`workspace_dir`, `briefing_path`, `default_harness`, `harnesses`, `repos` and
`doctor`, and lists `user`, `concurrency` and `autonomy` as comments to
uncomment. Run `slop-bot doctor` after an edit, and restart the service (`slop-bot service install`,
or `slop-bot install`) for the daemon to pick it up.

The loader is strict:

- an unknown key is an error, so a misspelt setting can't silently not apply
- every problem is reported at once, each naming its field
- `~` expands to the home directory, and a relative path resolves against
  the config file's directory, not the daemon's working directory
- every key is optional, and a missing file at the default location means
  all defaults (an explicit `--config` that doesn't exist is an error)

Source checked against: `internal/config/config.go` (the `Config` struct's
`yaml` tags, listed by walking the struct with `go/ast`, and the
`applyDefaults` and `validate` functions), at the head of branch
`slop-bot/docs`. Re-check the table when that file changes.

## Keys

Durations are Go duration strings: `15m`, `24h`, `720h`.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `workspace_dir` | path | `~/slop-bot/workspace` | Where ticket worktrees are created, as `t<ticket>-<repo>`. `uninstall` never deletes it. |
| `user` | string | the OS user name (when blank) | Who slop-bot runs for. Tickets assigned to this id are its agents' to work. |
| `briefing_path` | path | none | Your own briefing, appended to slop-bot's built-in one for every agent. Empty means none. See [concepts](concepts.md#the-briefing). `install` writes a stub at `~/.config/slop-bot/briefing.md` (in the config directory) and names it here. |
| `default_harness` | string | `claude-code` | The harness a ticket with no `--harness` runs on. Must be listed under `harnesses`. |
| `agents_may_set_urgent` | bool | `true` | Whether an agent may file a ticket as urgent (with a required reason). The user always may. |
| `ticketing.system` | string | `local` | The ticket store. Only `local` exists. |
| `repos[].name` | string | none | The repo's name in the lookup, and part of its worktree directory name. A letter or digit first, then letters, digits, `.`, `_` or `-`; no `..`; at most 100 characters; unique. |
| `repos[].path` | path | none | The repo's checkout on this machine. Required. |
| `repos[].git_remote` | string | none | Accepted and kept, but nothing reads it yet (reserved for multi-machine use). |
| `concurrency.max_active` | int | `4` | Agents in a turn at once, in the normal pool. At least 1. |
| `concurrency.max_live` | int | the larger of `3 * max_active` and `max_active + urgent.max_active` | Cap on live harness processes, working or waiting. At the cap, a start stops (parks) the longest-waiting idle agent, which is resumed when it next has work; see [slots](concepts.md#slots). Must be at least `max_active + urgent.max_active`. |
| `concurrency.urgent.max_active` | int | `1` | Agents in a turn at once, in the urgent pool. At least 1. |
| `autonomy.enabled` | bool | `false` | Whether tickets that agents file are worked without a person. `slop-bot autonomy on\|off` overrides it at runtime and persists. |
| `autonomy.tickets_per_agent` | int | `10` | Budget of outstanding agent-filed tickets, per agent slot (`max_active + urgent.max_active`). At least 1. |
| `harnesses.<name>.driver` | string | the harness's only driver | One of the drivers below. |
| `harnesses.<name>.api_key_opt_in` | bool | `false` | Let the harness's API key variables through from the environment. Off, they are stripped: Claude Code in `-p` mode would otherwise bill an `ANTHROPIC_API_KEY` at API rates instead of using your subscription login. |
| `harnesses.<name>.permissive` | bool | `true` | Run the harness without permission prompts. Nobody watches a headless agent's dialogs. pi has no permission prompts, so `false` is refused for it. |
| `harnesses.<name>.model` | string | blank | The model for agents whose ticket names none. Blank leaves it to the harness; pi's adapter then picks a known-good one and `doctor` warns. |
| `harnesses.<name>.limit_threshold` | number | `95` | Percent of the plan's allowance at which no new agent starts on this harness until the window resets. Above 0, up to 100. See [plan-limit holds](concepts.md#plan-limit-holds). |
| `agents.nudges` | int | `3` | How many times an agent that stops without a reason is nudged before it is stalled. At least 1. |
| `agents.stall_after` | duration | `15m` | How long a working agent may go without an event before it is stalled. |
| `agents.attach_lease` | duration | `30s` | How long an attach outlives its CLI's last heartbeat. Past it, the agent is handed back. Across a daemon restart, the attached CLI has this long to reconnect. At least 1s. See [attach](concepts.md#attach-and-hand-back). |
| `agents.ask_reminder_after` | duration | `24h` | How long an `ask` may go unanswered before you are reminded of it, once per ask, in your inbox. Not negative. |
| `retention.transcripts` | duration | `720h` (30 days) | How long a retained transcript is kept after its agent ends. |
| `retention.events` | duration | `720h` (30 days) | How long the event log keeps events. |
| `retention.state_changes` | duration | `720h` (30 days) | How long agents' state history is kept. |
| `retention.messages` | duration | `720h` (30 days) | How long a message is kept once delivered or read. Queued messages, and open asks, are kept however old. |
| `retention.agents` | duration | the larger of `2160h` (90 days) and `retention.transcripts` | How long an ended agent's record is kept, with its worktree list and history. Keep it at least `retention.transcripts`, so a retained transcript can still be found by its ticket's id. An agent released to a person (`attach --no-resume`) is kept for good: its record keeps new agents off the ticket. So is one whose worktrees couldn't be cleaned up: slop-bot's own cleanup forgets each worktree it removes, but one you remove by hand stays recorded, and keeps its agent for good. |
| `doctor.require` | list of strings | empty | Binaries your workflow needs on `PATH` (`gh`, linters). slop-bot doesn't use them, but its agents will; `doctor` fails when one is missing. Entries must not be blank. |

If `harnesses` is empty, it holds just the default harness with all
defaults. A harness name can appear with no body (`codex:`) to take its
defaults.

### Harnesses and drivers

| Harness name | Drivers | Default |
| --- | --- | --- |
| `claude-code` | `stream-json` | `stream-json` |
| `codex` | `app-server` | `app-server` |
| `pi` | `rpc` | `rpc` |
| `agy` | `tui` | `tui` (not runnable in this build) |

Any other harness name is an error.

## Annotated example

```yaml
# Where ticket worktrees are created. Never deleted by `uninstall`.
workspace_dir: ~/slop-bot/workspace

# Your own briefing, appended to slop-bot's. Read at each agent spawn, so
# an edit applies to the next agent without a restart.
briefing_path: ~/.config/slop-bot/briefing.md

default_harness: claude-code
agents_may_set_urgent: true

# The repo lookup: agents get a worktree of each repo their ticket names.
repos:
  - name: slop-factory
    path: ~/src/slop-factory

concurrency:
  max_active: 4       # agents in a turn at once
  max_live: 12        # live harness processes, including waiting ones
  urgent:
    max_active: 1     # a separate pool, so urgent work never queues behind normal

autonomy:
  enabled: false      # agent-filed tickets wait for you to assign them
  tickets_per_agent: 10   # budget = 10 * (4 + 1) = 50 outstanding tickets

harnesses:
  claude-code:
    driver: stream-json
    model: opus
    limit_threshold: 90   # hold new agents at 90% of the plan window
  codex: {}               # all defaults
  pi:
    model: "<provider>/<model>"   # a pair your pi login serves (`pi --list-models`)

agents:
  nudges: 3
  stall_after: 15m

retention:
  transcripts: 720h
  events: 720h
  state_changes: 720h
  messages: 720h
  agents: 2160h

doctor:
  require: [gh]
```

## Where slop-bot keeps its files

`slop-bot paths` prints these, resolved for this shell. Each XDG variable is
honoured when set to an absolute path; otherwise the default applies.

| What | Default | Variable |
| --- | --- | --- |
| Config: `slop-bot-config.yaml`, `briefing.md` | `~/.config/slop-bot/` | `XDG_CONFIG_HOME` |
| Data: `tickets.db` (the local tickets) | `~/.local/share/slop-bot/` | `XDG_DATA_HOME` |
| State: `state.db`, `agents/<id>/` (rendered briefings), `transcripts/`, `logs/` (macOS; under systemd the logs go to the journal, `journalctl --user -u slop-bot.service`) | `~/.local/state/slop-bot/` | `XDG_STATE_HOME` |
| Cache | `~/.cache/slop-bot/` | `XDG_CACHE_HOME` |
| Runtime: `slop-bot.sock`, `daemon.lock` | `$XDG_RUNTIME_DIR/slop-bot/` | `XDG_RUNTIME_DIR` |

When `XDG_RUNTIME_DIR` is unset:

- Linux falls back to `/run/user/<uid>/slop-bot`, only if that directory is
  mode 0700 and yours
- macOS falls back to `$TMPDIR/slop-bot`

The socket path must be at most 103 bytes (macOS allows 104 including the
terminating NUL), so a long `XDG_RUNTIME_DIR` is refused with a message
saying so.

The installed service carries the paths and `PATH` resolved in the shell
that ran `install`. If you later change `XDG_*` variables or `PATH`, re-run
`slop-bot install`; `doctor` warns when the shell and the service disagree.
