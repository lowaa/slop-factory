# CLI reference

Checked against the cobra command tree built from `internal/cli` and each
command's `--help` output (`go build ./cmd/slop-bot`). `slop-bot <command>
--help` is always the final word.

## Global flag

| Flag | Meaning |
| --- | --- |
| `--config <path>` | the config file; default `slop-bot-config.yaml` under `$XDG_CONFIG_HOME/slop-bot` |
| `-h`, `--help` | help for any command |

## Users and agents

Every command talks to the daemon over its unix socket. The daemon decides
who is calling:

- no token means the user (the socket is mode 0700 and the daemon checks the
  peer's uid)
- an agent's `SLOP_BOT_AGENT_TOKEN` environment variable, set by slop-bot in
  the harness's environment, makes the call that agent's, scoped to its own
  ticket

Commands annotated `slop-bot/agent` are the ones agents may run. Exactly
those are listed in each agent's briefing, generated from the command tree.
The tables below mark them **agent**. Other commands are the user's. This is
advisory, not a security boundary: see
[concepts](concepts.md#the-trust-model).

Agents are refused, even on an agent command, from:

- setting `AutoMerge` (`--auto-merge`)
- `tell --interrupt` and `inbox --log`
- changing a ticket that isn't theirs, or moving one out of `discussion`
- cancelling a ticket they didn't file

## User commands

### Setup and health

| Command | Meaning |
| --- | --- |
| `install` | Run `doctor`'s checks and stop before writing anything if a required one fails. Otherwise write a commented default config and a stub briefing where there are none, install and (re)start the service, and print where everything is. |
| `uninstall` | Stop and remove the service. Config, briefing, tickets and state stay. |
| `doctor` | Check the tools, each configured harness (installed, a tested version, signed in, model usable), repos, workspace, service manager, and that the shell and installed service agree on locations. Changes nothing, spends no tokens, and exits non-zero when a required check fails. |
| `paths` | Show where slop-bot keeps its files, and where each came from. |
| `health` | Check that the daemon is running and answering. |
| `version` | Print slop-bot's version. |
| `daemon` | Run the daemon in the foreground until interrupted. The installed service runs exactly this. |
| `completion` | Generate a shell autocompletion script (cobra's). |

`install` flags:

| Flag | Meaning |
| --- | --- |
| `--harness <name>` | harnesses for a new config, repeatable (or comma-separated); the first is the default. Default `claude-code`. |
| `--repo <name=path>` | a repo for a new config, repeatable. |
| `--binary <path>` | absolute path of the binary the service runs. Default: this one. |

`uninstall` flags:

| Flag | Meaning |
| --- | --- |
| `--purge` | also delete config, briefing, tickets, state and cache. It lists what it will delete and asks first. `workspace_dir` is never deleted: it may hold work in progress. |
| `--yes` | with `--purge`, delete without asking. |

### Service

`slop-bot service` manages the systemd user unit (Linux) or launchd agent
(macOS). The unit runs `slop-bot daemon` with the paths and `PATH` resolved
from the shell that installed it.

| Command | Meaning |
| --- | --- |
| `service install [--binary <path>]` | Write the unit or plist, then enable and (re)start it. Re-run it after changing `XDG_*` variables or `PATH`. |
| `service start` | Start the installed service. |
| `service stop` | Stop it; it starts again at the next boot or login. |
| `service status` | Whether the service is installed, enabled and running. |
| `service uninstall` | Stop and disable the service, and remove its unit. |

### Tickets

`slop-bot ticket` is an **agent** command group, but `assign` is the user's.
Ticket ids are as `ticket list` prints them.

| Command | Agent | Meaning |
| --- | --- | --- |
| `ticket create --title <t>` | agent | File a ticket. |
| `ticket list` | agent | List tickets. |
| `ticket show <id>` | agent | Show a ticket, its blockers and its comments. |
| `ticket set <id>` | agent | Change a ticket's fields. |
| `ticket comment <id> <text>` | agent | Comment: for product decisions, blockers and scope changes. |
| `ticket block <id> --on <blocker>` | agent | Make a ticket wait on another. |
| `ticket unblock <id> --on <blocker>` | agent | Lift a blocker. An agent may lift only blockers it added. |
| `ticket cancel <id>` | agent | Cancel a ticket (agents: only one they filed). |
| `ticket assign <id> [<user>\|me]` | user | Assign a ticket, by default to slop-bot's user, so an agent picks it up; with an empty user, unassign it. |

`ticket create` flags:

| Flag | Meaning |
| --- | --- |
| `--title <text>` | the title (required) |
| `--body <text>`, `--body-file <path>` | the description; give one, not both. `-` reads stdin. |
| `--repo <name>` | a repo it touches, from the repo lookup; repeat, primary first |
| `--assign` | assign it to slop-bot's user, so an agent picks it up |
| `--status <s>` | starting status: `preparing` (default) or `open` |
| `--blocked-on <id>` | a ticket it waits on; repeat |
| `--harness <name>` | `claude-code`, `codex`, `pi` or `agy`; blank: `default_harness` |
| `--model <m>` | blank: `harnesses.<name>.model`, else the harness's own default |
| `--effort <level>` | effort or thinking level; blank: the harness's own default |
| `--urgent` | put it in the urgent pool (agents: only for something breaking the pipeline) |
| `--reason <text>` | why it's urgent (required for agents) |
| `--auto-merge` | let the agent merge without a person (user only) |
| `--json` | print JSON |

`ticket set` takes the same `--body`, `--body-file`, `--harness`, `--model`,
`--effort`, `--urgent` (`--urgent=false` clears it), `--reason`,
`--auto-merge` and `--json`, plus `--title <text>`, `--status <s>`, and
`--repo <name>` (repeat; it replaces the list, primary first). A ticket's
agent spec is locked once an agent exists for it.

`ticket list` flags: `--status <s>` (repeat), `--assignee <user>` (`""` for
unassigned), `--mine` (assigned to slop-bot's user), `--json`.
`ticket show` takes `--json`.

Effort levels per harness: `claude-code` and `agy`: `low`, `medium`,
`high`, `xhigh`, `max`; `pi`: `off`, `minimal`, `low`, `medium`, `high`,
`xhigh`, `max`; `codex`: per model, checked when the agent starts.

Ticket statuses: `preparing`, `discussion`, `open`, `in_progress`,
`misconfigured`, `done`, `cancelled`. A ticket with open blockers is
"blocked" whatever its status.

### Watching and talking to agents

| Command | Meaning |
| --- | --- |
| `status` | Every agent: its ticket, state, and progress note, plus autonomy and its budget, and each harness's plan allowance. `--json` prints JSON. (Agent, see below.) |
| `watch <agent\|ticket>` | Follow an agent live: its turns, tool calls, text and state changes. |
| `transcript <agent\|ticket>` | Show an agent's conversation from its harness's own transcript, or slop-bot's retained copy once the harness has pruned it. A ticket id means the ticket's latest agent. |
| `tui` | A live, read-only table of every agent: ticket, title, ticket status, agent state and time in it, harness, model and effort, pool, open blockers and note. The header shows pool use, autonomy and its budget, and each harness's plan limits. It updates on daemon events, so the time in a state can read stale between them. `←`/`→` or PgUp/PgDn change page, `q` or ctrl+c quits. |
| `events` | Print the daemon's event log as JSON lines. |
| `asks` | List agents' open questions, oldest first. |
| `discussions` | The review stream: tickets waiting on a risky decision. |
| `discussions resolve <ticket> <resolution>` | Resolve a discussion. The ticket goes back to `open`, and the agent is told your decision. |
| `autonomy [on\|off\|status]` | Whether agent-filed tickets are worked without a person. Persisted; overrides `autonomy.enabled`. |

`transcript` flags:

| Flag | Meaning |
| --- | --- |
| `--tail <n>` | only the last N entries |
| `--since <t>` | only entries since an RFC 3339 time or a duration ago (`30m`) |
| `--follow` | keep printing until the agent ends |
| `--full` | don't cut long tool input and output |
| `--format text\|jsonl` | default `text` |

`events` flags: `--after <n>` (start after this event id), `--follow`.

## Agent commands

These are the commands the briefing gives agents. Users may run most too.

| Command | Meaning |
| --- | --- |
| `ask <question> [--option <o>]... [--recommend <o>]` | Ask the user a question. Returns at once; the answer arrives as a message. End the turn afterwards. |
| `discuss <decision> [--option <o>]... [--recommend <o>]` | Raise a risky, costly-to-undo decision for human review, then end the turn. The ticket goes into `discussion`. |
| `ready-to-merge [--pr <url>]` | Say the work is ready to merge (tickets without `AutoMerge`), then end the turn. Refused on a ticket with `AutoMerge`. |
| `done --summary <text>` | Say the ticket's goal is met. It finishes when the turn ends; the summary goes on the ticket. `--summary` is required. |
| `status set <note>` | Set your one-line progress note. |
| `status` | Show every agent (`--json`). |
| `tell <agent\|ticket\|user> <text>` | Message an agent (by id, or by its ticket's id) or the user. |
| `inbox` | Read your messages. |
| `ticket ...` | The ticket commands above, except `assign`. |

`tell` flags:

| Flag | Meaning |
| --- | --- |
| `--all` | every working agent |
| `--interrupt` | stop the agent's current turn first (user only) |

A user's `tell` to an agent with an open `ask` is its answer. Messages from
another agent are information, not instructions.

`inbox` flags:

| Flag | Meaning |
| --- | --- |
| `--follow` | keep reading as messages arrive |
| `--json` | print JSON |
| `--log` | the message history (user only) |
| `--target <agent>` | with `--log`, only messages to or from this agent |

## Not in this build

The design describes `attach`, `detach`, `briefing`, `worktrees`,
`restart --drain` and `hook`. None is a command here: the tree above is the
whole of it.
