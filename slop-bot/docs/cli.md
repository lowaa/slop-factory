# CLI reference

Checked against the cobra command tree built from `internal/cli` and each
command's `--help` output, hidden commands included (`go build
./cmd/slop-bot`). `slop-bot <command> --help` is always the final word.

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
- `tell --interrupt`, `tell --merge` and `inbox --log`
- changing a ticket that isn't theirs, or moving one out of `discussion`
- cancelling a ticket that is neither their own nor one they filed
- reading `asks` and `discussions`: other agents' questions and tickets

An agent following `events` (as `inbox --follow` does) sees its own events
whole, other agents' lifecycle and messaging events without their payloads,
and nothing else of theirs: not their conversations or merge refusals.

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
| `--purge` | also delete config, briefing, tickets, state and cache. It lists what it will delete and asks first. `workspace_dir` is never deleted: it may hold work in progress. The installed service is stopped and removed first; purge refuses only if a daemon started by hand still answers. |
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
| `ticket cancel <id>` | agent | Cancel a ticket (agents: their own, unless it is in discussion, or one they filed). |
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
agent spec can't be changed while its agent is live: a live agent resumes
from the spec it started with. Once the agent has ended, it can.

`ticket list` flags: `--status <s>` (repeat), `--assignee <user>` (`""` for
unassigned), `--mine` (assigned to slop-bot's user), `--json`.
`ticket show` takes `--json`.

Effort levels per harness: `claude-code` and `agy`: `low`, `medium`,
`high`, `xhigh`, `max`; `pi`: `off`, `minimal`, `low`, `medium`, `high`,
`xhigh`, `max`; `codex`: per model, so slop-bot doesn't check them: the
value is passed to codex as given.

Ticket statuses: `preparing`, `discussion`, `open`, `in_progress`,
`misconfigured`, `done`, `cancelled`. A ticket with open blockers is
"blocked" whatever its status.

### Watching and talking to agents

| Command | Meaning |
| --- | --- |
| `status` | Every agent: its ticket, state, and progress note, plus autonomy and its budget, and each harness's plan allowance. `--json` prints JSON. (Agent, see below.) |
| `watch <agent\|ticket>` | Follow an agent live: its turns, tool calls, text and state changes. |
| `attach <agent\|ticket>` | Take an agent over in its harness's TUI, in this terminal. Quitting the TUI hands the agent back. See below. |
| `detach <agent\|ticket>` | Hand an attached agent back from another terminal, which ends its TUI. |
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

`attach` flags:

| Flag | Meaning |
| --- | --- |
| `--now` | interrupt the agent's turn instead of waiting for it to end |
| `--no-resume` | release the agent to you: slop-bot stops running it, and the ticket stays assigned but no new agent is started for it |

`events` flags: `--after <n>` (start after this event id), `--follow`.

### Worktrees

A ticket's worktrees are cleaned up when it ends, except any whose removal
would lose work. These commands find and remove those.

| Command | Meaning |
| --- | --- |
| `worktrees` | Every recorded worktree: its ticket, repo, path, branch, agent, and status. `in use` is a live agent's; `kept` is an ended agent's that holds work, listed with why (`uncommitted`, `untracked`, `unpushed`, `in_progress`, `locked`, `nested`); `released` was handed to you by `attach --no-resume`; `removable` holds nothing a clean would lose; `unchecked` couldn't be checked, with the error. `--json` prints JSON. |
| `worktrees clean <ticket>` | Remove the ticket's worktrees and its local `slop-bot/t<ticket>/` branches, keeping any that hold work, as at the ticket's end, and any released to you by `attach --no-resume`. Besides the recorded ones, it takes any worktree of the ticket's repos on disk that nothing records, as a first start that failed leaves. Refused while the ticket has a live agent or one is starting. git can't remove a worktree with initialised submodules, so one listed `removable` can still fail here: `--force` removes it. |
| `worktrees clean <ticket> --force` | The same, but discarding the worktrees and branches whatever they hold, a lock or a deleted `.git` link included. It lists what it discards, warning where a worktree couldn't be checked, and asks you to type `discard` first. Still refused, to be removed by hand: a worktree with another worktree inside it (remove that one first), one whose `.git` file points nowhere (it isn't plainly slop-bot's), and one whose repo is no longer in the config. |

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
| `--interrupt` | stop the agent's current turn first; the message is delivered as the next turn, once the interrupted one ends (user only) |
| `--merge` | authorise the agent to merge its ticket's work; the text is optional (user only) |

A user's `tell` to an agent with an open `ask` is its answer. A user's
`tell <agent> merge` to an agent in `waiting_merge` authorises the merge,
as `--merge` does ([merge_guard.md](merge_guard.md)). Messages from
another agent are information, not instructions.

`inbox` flags:

| Flag | Meaning |
| --- | --- |
| `--follow` | keep reading as messages arrive |
| `--json` | print JSON |
| `--log` | the message history (user only) |
| `--target <agent>` | with `--log`, only messages to or from this agent |

## Hidden commands

`hook` is run by harnesses, not by people or agents, and is left out of the
briefing's CLI reference.

| Command | Meaning |
| --- | --- |
| `hook pre-tool` | The merge guard for harnesses with a `PreToolUse` hook (Claude Code, pi): reads the call on stdin and answers allow or deny ([merge_guard.md](merge_guard.md)). |
| `hook tui <session> <event>` | Relays a TUI harness's (agy's) hook to its conversation in the daemon, which answers it, including the merge guard's verdict. |

Not commands: `briefing` (the built-in briefing is rendered at each agent's
start into `$XDG_STATE_HOME/slop-bot/agents/<agent>/briefing.md`) and
`restart --drain`.
