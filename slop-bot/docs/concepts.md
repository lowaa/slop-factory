# Concepts

Where this describes behaviour, it is checked against `internal/agents`,
`internal/state`, `internal/tickets`, `internal/daemon` and
`internal/briefing`.

## Tickets and agents

A ticket has a status; an agent, one per ticket for the ticket's whole life,
has a state. They move together but are different things.

Ticket statuses:

| Status | Meaning |
| --- | --- |
| `preparing` | being refined: an agent reads the code, clarifies scope and chooses an approach. The start for every ticket unless filed straight into `open`. |
| `discussion` | waiting on a person to resolve a risky decision. Only the user may move a ticket out of it. |
| `open` | refined, ready to be worked once nothing blocks it |
| `in_progress` | being worked |
| `misconfigured` | parked until you fix it: its agent spec failed validation (an unconfigured harness, a bad effort), or its agent failed to start or was given up on (see below) |
| `done` | finished, only from `in_progress` |
| `cancelled` | dropped |

Being blocked is not a status: it is a ticket with open blockers. Blockers
gate the work, not the refinement, so a blocked `preparing` ticket still
gets an agent, and a blocked `open` one waits.

A ticket gets an agent when it is `preparing` or `open`, assigned to the
configured `user`, not blocked (if `open`), and has no live agent. Oldest
first.

## The agent lifecycle and states

A new agent gets its own worktrees, a rendered briefing and a token, and
starts in `starting`. It refines the ticket, works it, reviews and opens
PRs as your briefing says, merges only if the ticket has `AutoMerge`, and
declares the ticket done.

Agent states:

| State | Meaning |
| --- | --- |
| `starting` | the harness is launching |
| `refining` | in a turn, ticket `preparing` or in `discussion` |
| `working` | in a turn, ticket `open` or `in_progress` |
| `idle` | between turns: nudged, or interrupted by a person |
| `waiting_user` | has an open `ask` |
| `discussion` | raised a `discuss` |
| `waiting_merge` | said `ready-to-merge` |
| `stalled` | needs a person: too many nudges, a failed turn, or silence mid-turn |
| `attached` | a person has taken over in the harness's TUI (see [attach](#attach-and-hand-back)) |
| `released` | handed to a person with `attach --no-resume`, or given up on (see below) |
| `done` | the ticket's goal is met |
| `exited` / `crashed` | the harness process ended, with status 0 / non-zero |

What an agent is waiting on is read from where it is recorded, not only
from this turn's declaration: open asks mean `waiting_user`, a ticket in
`discussion` means `discussion`.

If the harness process dies, the agent becomes `exited` or `crashed` and
the user is told. slop-bot then resumes it by its conversation id, up to 3
times in a row. The count resets only when a turn completes: a failed turn
doesn't reset it. Past 3, or when a resume fails, the agent is `released`
and its worktrees are cleaned up (any holding work are kept, and the user
is told; `slop-bot worktrees` lists them, and `worktrees clean` removes them). A `preparing` or `open` ticket is then parked as `misconfigured`,
so a new agent isn't started into the same failure. A harness that fails to
start is treated the same way. `slop-bot ticket set <id> --status preparing`
puts a parked ticket back in line. A resume happens in the directory the
conversation started in, with any worktree gained since added.

On a daemon restart, live agents are resumed by their conversation id,
except those parked to free a process (see [slots](#slots)), which wait for
work. A TUI agent's (agy's) session outlives the daemon instead, and is
adopted as it runs, with its token: one caught mid-work is told that its
slop-bot calls may have failed meanwhile. `slop-bot uninstall` ends those
sessions. A ticket that is done or cancelled ends its agent, running or not,
at restart too.

## Declarations, nudges and stalls

An agent must end each turn with a reason. These are the declarations, all
CLI commands:

| Declaration | Effect |
| --- | --- |
| `ask` | files a question for the user and returns at once; the agent carries on with what doesn't depend on the answer. When the turn ends, the agent is `waiting_user`. A user `tell` to an agent with open asks is the answer, mid-turn included. Broadcasts and slop-bot's own notices never answer. An ask still open `agents.ask_reminder_after` (default 24h) after it was asked brings you one reminder in your inbox. |
| `discuss` | for a risky, costly-to-undo decision: moves the ticket to `discussion`, writes the options and recommendation as a ticket comment, tells the user. The agent is `discussion` at turn end. `discussions resolve <ticket> <text>` moves the ticket back to `open` and tells the agent. Leaving `discussion` is the user's alone: the agent can't move the ticket out (to any status, cancel included), and `done` is refused while it is there. |
| `ready-to-merge` | on a ticket without `AutoMerge`: comments (with the PR URL, if given), tells the user, and the agent is `waiting_merge`. Merge when the user tells the agent to. Refused if the ticket has `AutoMerge`. |
| `done --summary` | the ticket's goal is met, not merely merged: there may be checks or a deploy to verify first. The agent finishes when the turn ends; the summary goes on the ticket as a comment, the ticket becomes `done`, the transcript is kept, and worktrees are cleaned up. |

A declaration is acted on at the next turn end, so an agent's own CLI call
can land before the daemon sees the turn finish.

A turn that ends with none of these is **nudged**:
a message from slop-bot asks it to say why it stopped. After
`agents.nudges` nudges (default 3), the next such stop makes it `stalled`
and tells the user. A turn that ended because a person interrupted it is
`idle`, not nudged, and a turn that failed is `stalled` at once. A turn
that ends with none of the declarations on a ticket with open blockers
leaves the agent `idle`, not nudged: it was told to wait, and slop-bot wakes
it when they are done.

Separately, an agent that is in a turn but emits no event for
`agents.stall_after` (default 15 minutes) is `stalled`, and the user is told
how to `watch` it or `tell --interrupt` it. It returns to working when
events resume.

## Blockers

A ticket's blockers gate its work, not its refinement. While a ticket is
blocked, a live agent hears about it as a notice from slop-bot, not from the
user (so it carries no authority and answers no asks), each time the ticket
goes between blocked and unblocked, in any live status:

- blocked while `preparing`: carry on refining, don't start the work
- blocked later: stop at a safe point and end the turn
- unblocked: finish refining, or carry on with the work

What the agent was last told is kept in `state.db`, so the notice isn't
repeated. An agent that ends a turn with no declaration on a blocked ticket
waits: it is `idle`, not nudged. `done` is refused for a blocked ticket whose
work hasn't started. An agent may lift only blockers it added.

## Messages

`tell` queues a message; `inbox` lists what an agent has. Delivery:

- mid-turn, steered into the running turn where the driver can do it
- otherwise at the next turn boundary, which needs a slot; an agent being
  woken for a message goes ahead of new tickets in its pool's queue
- held while a person is attached, and delivered at the hand-back

Each message says who sent it: the text is quoted with `> ` on every line
under a `[slop-bot] Message from ...` header line, the user's included, so
only slop-bot writes a header. slop-bot's own notices aren't quoted.
`tell --interrupt` delivers its message as the next turn, once the
interrupted one ends, not as a steer into it.
A message from **user** carries the user's authority; one from another agent is information to weigh, not an
instruction. A loop guard stops ping-pong and flooding: at most 30
messages a minute from one sender, and 20 between one pair in ten minutes,
after which the user is told.

## Slots

Two limits bound how many agents run. Both are counted per daemon.

- `concurrency.max_active` (default 4) counts agents **in a turn**. An
  agent waiting on an answer, a discussion or a merge holds no slot.
- `concurrency.urgent.max_active` (default 1) is a second, separate pool for
  urgent tickets, so a red main branch never queues behind normal work.
- `concurrency.max_live` (default the larger of 3 times `max_active` and
  `max_active` plus `urgent.max_active`) caps live harness
  processes, working or waiting. At the cap, a start that needs a process
  stops the agent that has waited longest, between turns, on a person
  (`waiting_user`, `discussion`, `waiting_merge`, or `idle` after an
  interrupt) or on its ticket's blockers (`idle`). It is **parked**: it keeps
  its state, and its messages queue as usual. The first message for it, or a
  change to its ticket it must hear of (its blockers, a new repo), queues it
  for a slot, and it is resumed by its conversation id when it gets one,
  parking another if need be. A restart leaves a parked agent parked.
  `slop-bot status` shows it as `<state> (parked)` (`parked_at` in
  `--json`). A TUI session is never parked while a person is in it, or once
  joined, or adopted after a restart: stopping it would end their session. An
  agent in a turn, starting, attached, finishing, `stalled`, or with a
  message waiting for a slot is parked only for a parked agent ahead of it
  in the queue (it keeps its place). When no agent can be parked, the start
  waits.

An agent that has an answer or message to act on is served before new
tickets, parked or not, first come first served per pool. New work never
jumps an agent already queued for a slot. The queue's order is kept in
`state.db`, so a restart serves it in the same order.

An urgent ticket is one the user marked `--urgent`, or an agent did, when
`agents_may_set_urgent` is true, with a required `--reason` that goes on the
ticket as a comment; the user isn't told separately. The briefing tells
agents to use it only for something breaking the pipeline.

## The autonomous loop and its budget

By default, a ticket an agent files lands **unassigned**, in the review
stream for you to accept. With autonomy on (`autonomy.enabled`, or `slop-bot
autonomy on`), agent-filed tickets are assigned to you at once, so agents
pick them up: `preparing`, then the work.

- it applies to agent-filed tickets only; yours are always yours to assign
- an agent's ticket inherits `AutoMerge` from the ticket it was filed from;
  agents can't set it
- an agent filing with no repos inherits its creating ticket's repos
- the budget caps **outstanding** agent-filed tickets (not finished ones) at
  `autonomy.tickets_per_agent` times the number of slots, normal plus urgent
  (default 10 x 5 = 50). Past it, filings are refused, and the user is told
  once when it fills.
- the `autonomy` runtime toggle is stored in `state.db` and wins over the
  config default

## Attach and hand-back

A harness gets one of two attaches, by its driver.

- **Join** (TUI harnesses: agy). `attach` runs `tmux attach` on the agent's
  own session, which runs on: nothing stops, and nothing is typed under you.
  Messages are held while you're in it (retried every 2s), your turns leave
  the agent `attached`, declarations wait for the hand-back, and detaching
  (`Ctrl-b d`, or `slop-bot detach`) hands it back with the held messages.
  `--now` interrupts the agent's turn first. Quitting agy ends the session,
  and the hand-back resumes the conversation. `--no-resume` is refused.
- **Hand-over** (programmatic harnesses: Claude Code, Codex, pi), below.

The rest of this section is the hand-over, except where it says otherwise.

`slop-bot attach <agent|ticket>` lets a person take over an agent's session.
It stops the programmatic session at the end of the agent's turn (with
`--now`, it interrupts the turn instead of waiting), then runs the harness's
own TUI, resuming the same conversation, in your terminal. The TUI gets a
fresh agent token, so `ask` and `done` work from inside it.

- one attach per agent. A second is told who holds it.
- a parked agent (see [slots](#slots)) has no session to take over:
  `attach` queues it to be resumed, without a message (a `tell` to an
  agent with open asks would answer them), and asks you to attach again in a
  moment.
- the attach holds a **lease**, renewed by the CLI's heartbeat (a third of
  the lease apart), for both kinds. If the heartbeat stops for
  `agents.attach_lease` (default 30s), the agent is handed back without the
  CLI. After a daemon restart the CLI has that long to reconnect. Other
  renewal errors, such as a busy database, are logged and retried, never a
  reason to end the attach.
- while attached, messages are held, not typed into the session, and are
  delivered at the hand-back with a note that a person worked with the agent
- the agent is handed back, and resumed by its conversation id, when you
  quit the TUI, when the terminal hangs up, when the lease expires, or when
  `slop-bot detach <agent|ticket>` is run from another terminal (which ends
  the TUI)
- if the agent ran `done` from inside the TUI, the hand-back finishes the
  ticket instead of resuming the agent. If the ticket was cancelled during
  the attach, the agent just ends.
- `--no-resume` releases the agent to you: it is `released`, nothing
  resumes it, and the ticket stays assigned, but no new agent is
  started for it. The merge guard still applies in the TUI, except Codex's,
  which runs with approvals bypassed ([merge_guard.md](merge_guard.md)).

A hand-back's resume is a start: it takes the ticket's start place, waits
for a start already in flight for the ticket, and is bounded by the start
timeout (2 minutes). A ticket cancelled, or a daemon stopped, mid hand-back
ends the agent instead.

## Transcripts and retention

`slop-bot transcript <agent|ticket>` reads an agent's conversation from the
harness's own session files (Claude Code's JSONL under
`$CLAUDE_CONFIG_DIR/projects`, Codex's rollout, pi's session JSONL) and
normalises it. When an agent ends, slop-bot copies the normalised
transcript to `$XDG_STATE_HOME/slop-bot/transcripts/`, because harnesses
prune their own. A later read prefers the harness's copy (it may have more,
for example after an attach) and falls back to the retained one.

Retention sweeps run on a timer:

- retained transcripts are deleted `retention.transcripts` after their agent
  ends (default 30 days)
- events older than `retention.events` are deleted (default 30 days)
- agents' state history older than `retention.state_changes` (default 30
  days)
- messages delivered or read more than `retention.messages` ago (default
  30 days); queued messages are kept
- ended agents, with their history, `retention.agents` after they ended
  (default 90 days)

An open `ask` is never pruned, and neither is an agent that still has
worktrees recorded, nor one released to a person with `attach --no-resume`:
its record is what keeps a new agent off the ticket the person took over.

## Plan-limit holds

Harnesses report how much of the user's plan allowance is used, per window
(for example a five-hour and a weekly window). slop-bot keeps the latest
report of each, saved across restarts, and `slop-bot status` shows them.

When any window of a harness reaches `harnesses.<name>.limit_threshold`
(default 95%), **no new agent starts** on that harness until the last of
those windows resets. When a harness reports no reset time, the hold lasts
30 minutes. Agents already running are untouched, so they can finish. The
user is told once per hold, and an event is logged per held ticket.

A held ticket that left its harness, model and effort blank, and was never
started, goes to another configured harness that isn't held. Any other
ticket waits: it named its harness, or its conversation lives in one.

## agy

agy is Google's Antigravity CLI, run as its own TUI in tmux (`tui` driver):
slop-bot types into it and reads its hooks, where the other harnesses are
driven over a protocol.

- **tmux.** Sessions live in a tmux server of their own on socket
  `slop-bot` (`tmux -L slop-bot`), named `agy-<agent>`. Under a user's
  systemd manager the server is started in a transient scope
  (`slop-bot-tmux-slop-bot`), so stopping or restarting the service doesn't
  end the sessions with its cgroup; the unit's `KillMode=mixed` still ends
  the daemon's other children. launchd ends only the job's process group,
  which tmux leaves. Needs tmux 3.4 or later (`doctor` checks).
- **Sessions outlive the daemon.** A daemon stop lets go of them and a
  restart adopts them, see [the lifecycle](#the-agent-lifecycle-and-states).
  Messages reach agy as a pointer ("run `slop-bot inbox`"), and stay in the
  agent's inbox until read. `attach` joins the session.
- **Sidecar.** agy has no flag for a hooks file or a system prompt, so
  slop-bot writes `<worktree>.slop-bot-agy/` beside the primary worktree,
  with `.agents/hooks.json` (the merge guard's `PreToolUse` hook, running
  `slop-bot hook tui`) and `AGENTS.md` (the briefing), and adds it with
  `--add-dir`. Nothing goes into the worktree, where an agent could commit
  it, or into your agy config. The folder is removed when the agent's
  workspace is forgotten. The session also gets `--settings` marking its
  folders trusted.
- **Sign-in.** agy runs on your own login (`~/.gemini`). On a consumer
  Google sign-in, Google's terms may forbid driving agy from another
  program: `doctor` warns once, and the choice is yours. With
  `harnesses.agy.api_key_opt_in: true` and a non-empty `GEMINI_API_KEY`, its
  sessions run on the key (billed per token) through a session-only
  `modelProvider` setting, and your own agy keeps its sign-in. A service
  doesn't read your shell profile: `doctor` says how to import the key into
  it.
- **Effort.** `low` to `max`, as Claude Code.

## The briefing

Every agent is told, in its system prompt, slop-bot's contract with it,
followed by your own briefing. The built-in part is rendered per agent from
`internal/briefing/builtin.md.tmpl`, embedded in the binary so it always
matches the CLI it describes. It covers:

- the ticket, and the lifecycle above, with the merge step depending on
  `AutoMerge`
- when a decision is risky enough to `discuss`
- always ending a turn with a reason, and the nudge count
- asking, messages and their authority, what goes on the ticket versus
  `tell`, and the progress note
- the worktrees (primary first, the agent's working directory), reading each
  repo's own `CLAUDE.md` or `AGENTS.md`, and not creating worktrees
  elsewhere
- the repo lookup
- filing follow-up tickets, blockers, and the urgent rules, for the current
  autonomy setting
- harness-specific quirks
- a **CLI reference generated from the command tree**: exactly the commands
  annotated `slop-bot/agent`

`briefing_path` names your own briefing, which follows a `---` separator
under the heading "The user's briefing", and wins on workflow choices
(review tools, PR conventions, how to merge). It is read at each agent
start, so an edit applies to the next agent with no restart. An empty path
means none; a path that can't be read is an error, never silently skipped.
`install` writes a stub at `~/.config/slop-bot/briefing.md`.

The rendered briefing is written to
`$XDG_STATE_HOME/slop-bot/agents/<agent>/briefing.md`, never into the
worktree.

## The trust model

Isolation between agents, and between agents and the user, is **advisory,
and the risk is accepted**. It stops mistakes, not malice.

- The control socket is a unix socket in a mode-0700 directory, and the
  daemon checks the peer's uid. Anything running as you can reach it.
- Each agent gets a random token, in `SLOP_BOT_AGENT_TOKEN`, stored only as
  a hash. A call with a token is that agent's, scoped to its own ticket:
  it can't set `AutoMerge`, message as the user, or move a ticket out of
  `discussion`. A call with no token is the user's. An unknown token (from an
  ended agent, say) is refused, not treated as the user.
- But an agent runs as you, with your filesystem, your credentials and
  `permissive` mode on by default (no permission prompts). It can read
  another agent's token from the process environment, or edit
  `slop-bot-config.yaml`, if it tries. The restrictions keep a well-behaved
  agent inside its ticket; they don't contain a hostile one.
- The harness environment is the daemon's, minus a denylist: API keys
  (unless `api_key_opt_in`), Claude Code's parent-session variables (not
  `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CODE_USE_*` or `CLAUDE_CODE_SKIP_*_AUTH`),
  and other agents' tokens.
- The merge flag is policy in the briefing, backed by a guard that refuses
  obvious merges (`gh pr merge`, a push to the default branch) on a ticket
  without `AutoMerge` until you run `slop-bot tell <agent> --merge`. Claude
  Code, pi and agy have the guard through a pre-tool hook. Codex has it
  through its command approvals, but only in permissive mode (the default),
  and not in the TUI after `attach`. The guard is advisory: an agent with a
  shell can get round it. See [merge_guard.md](merge_guard.md).
- `agy` runs on a Google sign-in whose consumer terms may forbid driving it
  from another program. `doctor` says so once; whether to proceed is your
  call. With `api_key_opt_in` and a `GEMINI_API_KEY`, agents run agy on the
  key instead (billed per token), set for their sessions only.

Run slop-bot only against repos and on machines where an agent running as
you is an acceptable risk.
