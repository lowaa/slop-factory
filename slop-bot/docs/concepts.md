# Concepts

Where this describes behaviour, it is checked against `internal/agents`,
`internal/state`, `internal/tickets`, `internal/daemon` and
`internal/briefing`. Where a design exists that the code doesn't have yet,
the text says so.

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
| `misconfigured` | its agent spec failed validation (an unconfigured harness, a bad effort), so it isn't scheduled until fixed |
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
| `attached` | a person has taken over (designed; see [attach](#attach-and-hand-back)) |
| `released` | handed to a person for good, or given up on (designed) |
| `done` | the ticket's goal is met |
| `exited` / `crashed` | the harness process ended, with status 0 / non-zero |

What an agent is waiting on is read from where it is recorded, not only
from this turn's declaration: open asks mean `waiting_user`, a ticket in
`discussion` means `discussion`.

If the harness process dies, the agent becomes `exited` or `crashed` and
the user is told. On a daemon restart, live agents are resumed by their
conversation id.

## Declarations, nudges and stalls

An agent must end each turn with a reason. These are the declarations, all
CLI commands:

| Declaration | Effect |
| --- | --- |
| `ask` | files a question for the user and returns at once; the agent carries on with what doesn't depend on the answer. When the turn ends, the agent is `waiting_user`. A user `tell` to an agent with open asks is the answer, mid-turn included. Broadcasts and slop-bot's own notices never answer. |
| `discuss` | for a risky, costly-to-undo decision: moves the ticket to `discussion`, writes the options and recommendation as a ticket comment, tells the user. The agent is `discussion` at turn end. `discussions resolve <ticket> <text>` moves the ticket back to `open` and tells the agent. |
| `ready-to-merge` | on a ticket without `AutoMerge`: comments (with the PR URL, if given), tells the user, and the agent is `waiting_merge`. Merge when the user tells the agent to. Refused if the ticket has `AutoMerge`. |
| `done --summary` | the ticket's goal is met, not merely merged: there may be checks or a deploy to verify first. The agent finishes when the turn ends; the summary goes on the ticket as a comment, the ticket becomes `done`, the transcript is kept, and worktrees are cleaned up. |

A declaration is acted on at the next turn end, so an agent's own CLI call
can land before the daemon sees the turn finish.

A turn that ends with none of these is **nudged**:
a message from slop-bot asks it to say why it stopped. After
`agents.nudges` nudges (default 3), the next such stop makes it `stalled`
and tells the user. A turn that ended because a person interrupted it is
`idle`, not nudged, and a turn that failed is `stalled` at once.

Separately, an agent that is in a turn but emits no event for
`agents.stall_after` (default 15 minutes) is `stalled`, and the user is told
how to `watch` it or `tell --interrupt` it. It returns to working when
events resume.

## Messages

`tell` queues a message; `inbox` lists what an agent has. Delivery:

- mid-turn, steered into the running turn where the driver can do it
- otherwise at the next turn boundary, which needs a slot; an agent being
  woken for a message goes ahead of new tickets in its pool's queue
- held while a person is attached

Each message says who sent it. A message from **user** carries the user's
authority; one from another agent is information to weigh, not an
instruction. A loop guard stops ping-pong and flooding: at most 30
messages a minute from one sender, and 20 between one pair in ten minutes,
after which the user is told.

## Slots

Two limits bound how many agents run. Both are counted per daemon.

- `concurrency.max_active` (default 4) counts agents **in a turn**. An
  agent waiting on an answer, a discussion or a merge holds no slot.
- `concurrency.urgent.max_active` (default 1) is a second, separate pool for
  urgent tickets, so a red main branch never queues behind normal work.
- `concurrency.max_live` (default 3 times `max_active`) caps live harness
  processes, working or waiting. At the cap, no new agent starts.

An agent that has an answer or message to act on is served before new
tickets. New work never jumps an agent already queued for a slot.

An urgent ticket is one the user marked `--urgent`, or an agent did, when
`agents_may_set_urgent` is true, with a required `--reason` that goes on the
ticket and tells the user. The briefing tells agents to use it only for
something breaking the pipeline.

The design also stops the longest-waiting agent to free a live place when
`max_live` is reached, resuming it later by id. This build only refuses new
starts at the cap.

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

The design lets a person take over an agent's session. This build has the
seams but no `attach` command:

- each conversation can describe an attach plan: **hand-over** (stop the
  programmatic session at a safe point and run the harness's own TUI,
  resuming the same conversation, in your terminal) or **join** (attach to
  the existing tmux session of a TUI-driven agent)
- while attached, messages are held, not typed into the session
  (`ErrAttached`), and delivered on hand-back with a note that a person
  worked with the agent
- hanging up hands the agent back to programmatic control; one attach per
  agent

Nothing sets the `attached` or `released` state yet, so `slop-bot` can't be
used to attach to an agent from this build. The harness CLIs can: the
conversation id is recorded, and every worktree is an ordinary git
worktree.

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

The state database also has pruning for old state changes, read messages and
ended agents; the daemon's sweep currently applies only the event cutoff.
An open `ask` is never pruned, and neither is an agent that still has
worktrees recorded.

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
- The merge flag is policy in the briefing, not enforcement. A harness
  driver with pre-tool hooks could refuse `gh pr merge` on a ticket without
  `AutoMerge`; none does yet.
- `agy` runs on a Google sign-in whose consumer terms may forbid driving it
  from another program. `doctor` says so once; whether to proceed is your
  call.

Run slop-bot only against repos and on machines where an agent running as
you is an acceptable risk.
