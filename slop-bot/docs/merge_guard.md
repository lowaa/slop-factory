# The merge guard

Defence in depth behind the briefing (plan task 4.10). An agent on a ticket
without `AutoMerge` is told to stop at `slop-bot ready-to-merge`. Where its
harness has a pre-tool hook, slop-bot also refuses the obvious merge commands
until the user says go. Codex has no hook that runs under app-server, so there
the daemon answers Codex's command approvals instead (see
[Codex](#codex-approvals-not-hooks)).

## How it works

1. The harness runs `slop-bot hook pre-tool` before each shell (or MCP) tool
   call. The call arrives on stdin as a Claude Code `PreToolUse` payload.
2. The hook asks the daemon (`POST /v1/guard/pre-tool`) with the agent's
   token. The daemon classifies the command (`internal/mergeguard`). A merge
   is allowed only when the ticket has `AutoMerge` or the user has authorised
   one. Otherwise the hook answers `permissionDecision: deny`, with a reason
   that tells the agent to run `ready-to-merge` and end its turn. Each refusal
   is a `merge.refused` event.
3. The user authorises with `slop-bot tell <agent|ticket> --merge [text]`, or
   with a tell whose whole text is `merge` to an agent in `waiting_merge`.
   To an agent with an open question, `merge` is an answer, not an
   authorisation. The grant goes in `state.db`
   (`merge_grants`), so it survives a restart. It belongs to the ticket, so a
   resumed agent keeps it. It is cleared when the ticket ends (done or
   cancelled, by anyone, with or without a running agent), and the guard
   ignores a grant on an ended ticket. An ended ticket can't be granted one.
   A grant isn't
   used up by one merge, because merging can take several commands.
4. **Fail safe:** when the daemon can't be asked (it is stopped, the socket is
   gone, or the token is unknown), the hook classifies the command itself. It
   refuses a merge and allows everything else. When the daemon can't read the
   ticket or the grant, it refuses the merge.

An allow writes nothing, so the harness's own permission rules still apply.
The hook never approves anything those rules would have asked about. (Codex
is the exception: there slop-bot is the one asked, and it approves whatever
isn't a merge.)

For the hook-based harnesses, `attach` hands the session over with the same
flags, so a person attached to an agent is held to the guard too, because the session still carries the
agent's token. To merge from there, run `slop-bot tell <agent> --merge`
first. After `attach --no-resume` the session is the person's, but it keeps
the hook and a token that no longer names a live agent. The hook then can't
ask the daemon and refuses merges, and `tell --merge` has no agent to
authorise. Merge from another shell. Codex's TUI hand-over is the exception:
it runs with approvals bypassed and no hook, so an attached person isn't
guarded there (see [Codex](#codex-approvals-not-hooks)).

## What counts as a merge

The classifier parses the command as bash (`mvdan.cc/sh`). It looks at every
command in it: lists, pipelines, subshells, `$(...)`, `if` bodies, wrappers
(`env`, `sudo`, `nohup`, `timeout`, `xargs`, ...), `bash -c`/`sh -c` scripts
and `eval`. Quotes and backslashes are resolved, so `"gh" pr mer\ge` is
caught. The merges it finds are:

- `gh pr merge` (with `--auto` too) and `glab mr merge`, with `-R` before or
  after the group
- `gh api` and `glab api`:
  - the REST merge endpoints (GitHub's `pulls/<n>/merge` and
    `repos/<o>/<r>/merges`, GitLab's `merge_requests/<n>/merge`) with any
    method but GET or HEAD
  - GraphQL merge mutations (`mergePullRequest`, `enablePullRequestAutoMerge`,
    `enqueuePullRequest`, `mergeBranch`). `gh api graphql --input`, and
    `-F query=@file`, are treated as merges because the guard can't see the
    query
- `curl` (or `wget`, `http`, `xh`) aimed at those endpoints, a quoted URL with
  variables in it included, or carrying those mutations
- an agent authorising itself: `slop-bot tell ... --merge`, or a bare
  `merge`, from the agent's shell. With the token stripped (`env -u
  SLOP_BOT_AGENT_TOKEN`), that call would reach the daemon as the user
- `git push` that writes to a default branch. The default branches are
  `main`, `master` and whatever `refs/remotes/<remote>/HEAD` names. The push
  is a merge in any of these cases:
  - a refspec's destination is a default branch (`main`, `HEAD:main`,
    `x:refs/heads/main`, `+main`, `:main`, `--delete main`)
  - a glob destination
  - `--all`, `--branches` or `--mirror`
  - a bare `git push` (or `git push <remote>` or `git push <remote> HEAD`)
    whose push destination (`@{push}`) or current branch is a default branch.
    The branch is looked up in the directory the command runs in, following
    `cd` and `git -C`. After a `git checkout` or `git switch` earlier on the
    line, or a computed `cd`/`git -C`/`--git-dir`, the branch the hook sees
    isn't the one pushed. Then a bare push is refused as unknown, and `HEAD`
    is taken to be a default branch
  - `:` or `+:` (every matching branch), and `heads/main` for
    `refs/heads/main`
  - a destination it can't read (`git push origin $BRANCH`). The exception is
    `$(git branch --show-current)` and its equivalents, which resolve to the
    current branch
- an MCP tool whose own name is a merging verb: `merge_pull_request`,
  `merge_merge_request`, `accept_merge_request`, `enable_*auto_merge`,
  `enqueue_pull_request`, `merge_branch`. Tools that read or open a merge
  request (`get_merge_request`, `create_merge_request`) aren't merges
- a command it can't take apart, when the whole line mentions `merge` or
  `push`. That covers a parse failure, a script fed to a shell's stdin
  (`echo ... | bash`, a heredoc), a computed command name, subcommand or
  `bash -c` script (`$GH pr merge`, `ACTION=push; git $ACTION ...`), and
  shells nested more than four deep

These aren't merges: `--dry-run`, tags (`--tags`, `tag v1`, `refs/tags/...`),
the words in arguments (`echo "gh pr merge"`, `gh pr create --title "merge
..."`), comments, and a local `git merge`. A local merge only lands on the
remote through a push, which is checked.

## Known bypasses (accepted)

Detection is advisory, like the agent token itself (plan 1.9). An agent with
a shell can always reach a merge that the classifier can't see:

- A script file: `./merge.sh`, `make release`, `python -c`, `node -e`, a test
  that runs git. The guard reads the command line, not the programs it runs.
- Aliases: git aliases (`git config alias.p push`), `gh alias set`, and gh
  extensions.
- Indirection the parser can't resolve: a variable as the command or the
  subcommand (`$CMD`, `git $SUB`) where the line doesn't mention merge or
  push, `xargs` or `find -exec` fed the words from elsewhere, and a GraphQL
  query read from a variable.
- Other remotes and forges: a push to a URL remote whose default isn't `main`
  or `master`, a default branch with another name, forge CLIs other than `gh`
  and `glab`, other forges' API endpoints, and an API call through a client
  other than `curl`, `wget`, `http`, `xh`, `gh` or `glab`.
- `cd` and checkout tracking is in source order, and doesn't model a
  subshell's scope or a command that fails.
- A token stripped in a way the classifier doesn't see (a script, an alias)
  lets an agent's `slop-bot tell --merge` reach the daemon as the user. The
  hook itself runs from the harness, outside the agent's command, with the
  harness's own environment.
- Claude Code settings that turn hooks off, `disableAllHooks` or a managed
  `allowManagedHooksOnly`, turn the guard off too. The user's own settings
  still apply under `--settings`.
- The agent can edit or delete the hook's inputs where it can write to them:
  pi's extension file sits beside the briefing, in slop-bot's state directory.
  This is the same trust boundary as the briefing.

## Per harness

| Harness / driver | Enforced | How |
| --- | --- | --- |
| claude-code/stream-json | yes | A session-only `PreToolUse` hook (matcher `Bash\|mcp__.*`), passed as JSON to `--settings`. It adds to the user's settings and writes nothing. The TUI hand-over (`attach`) gets the same flags. A `deny` holds under `--dangerously-skip-permissions` (tier 2: `TestClaudeCodeMergeGuard`). |
| pi/rpc | yes | slop-bot's extension `merge-guard.ts`, loaded with `--extension`, which project trust doesn't gate. It is written beside the agent's briefing file, never in the worktree. Its `tool_call` handler runs the hook for `bash` and `mcp*` tools and returns `{block: true, reason}` on a deny. pi runs nested (codemode) calls through the same handler. If the hook can't run at all, the extension refuses a call that mentions merge or push. Tier 2: `TestPiMergeGuard`. |
| codex/app-server | yes, when permissive (the default) | Its hooks don't run under app-server, so the daemon answers Codex's command approvals in-process: `approvalPolicy: untrusted`, a merge declined and the reason steered into the turn. A non-permissive agent and the TUI hand-over aren't guarded. See below. Tier 2: `TestCodexMergeGuard`. |
| agy/tui | yes | Its session-only `PreToolUse` hook (in the sidecar's `.agents/hooks.json`) runs `slop-bot hook tui`, which relays the call to the daemon; the TUI kit puts `run_command` calls (and MCP tools) to the same `mergeguard.Guard` as `hook pre-tool`, and a refused merge is answered `{"decision":"deny","reason":...}`. Otherwise the answer is unchanged: `allow`, or `ask` in a non-permissive workspace, which still passes a deny on. With the daemon away, `hook tui` answers itself with the offline classifier (merges refused, the rest allowed); only if slop-bot can't run at all does the hook's shell fallback allow, unclassified. Tier 1: `TestTheMergeGuardRefusesATUIAgentsMergeUntilAuthorised`, `TestATUIHookAnswersForTheDaemonWhenItsAway`; live: `TestLiveAgyWorksATicketThroughTheDaemon`. |

### Codex: approvals, not hooks

Codex's hooks are Claude Code-compatible, and `-c hooks.PreToolUse=...` loads
them for one session. Under `app-server`, though, session-flag hooks stay
`untrusted` and never fire. Neither `--dangerously-bypass-hook-trust` nor `-c`
can trust them, and persisted trust would be written to the user's
`~/.codex/config.toml` (codex protocol spike). So slop-bot guards Codex
through its approvals instead.

**How.** A permissive agent's thread starts (and resumes) with
`approvalPolicy: "untrusted"` and `approvalsReviewer: "user"`, so Codex asks
the client before every command. The codec puts each
`item/commandExecution/requestApproval` to the guard in-process, as the agent
(its workspace's agent and ticket, not a token), through the same check, and
the same `merge.refused` event, as the hook's route. An allow is answered
`accept` at once. A merge is answered `decline`, and since a decline carries
no reason (the model reads only `rejected by user`), the guard's reason is
steered into the running turn with `turn/steer`. Outside that turn the command
is only declined: the reason goes only into the turn that asked, so it
never reaches another thread (a sub-agent's) or starts a turn after an
interrupt. Patches, permission requests and MCP elicitations are approved,
because the agent is permissive, as they always were when one slipped
through; under `untrusted` they may now arrive routinely, which is unspiked
(the scripted model's Codex is offered neither `apply_patch` nor MCP tools). Only plain
`accept` is ever sent, never `acceptForSession` or an execpolicy amendment,
so nothing is cached or written to the user's rules.

**The spike (codex-cli 0.172.0, the scripted model, a temp `CODEX_HOME`).**
These are the messages, scrubbed, as recorded in
`internal/harness/codex/testdata/turn_guarded.jsonl`:

```
>> {"method":"thread/start","params":{"approvalPolicy":"untrusted","approvalsReviewer":"user","sandbox":"danger-full-access",...}}
<< {"method":"item/started","params":{"item":{"type":"commandExecution","command":"/bin/bash -lc ls","status":"inProgress",...}}}
<< {"method":"item/commandExecution/requestApproval","id":0,"params":{"kind":"command","command":"/bin/bash -lc ls","cwd":"/ws",
     "commandActions":[{"type":"listFiles","command":"ls"}],"proposedExecpolicyAmendment":["ls"],
     "availableDecisions":["accept",{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["ls"]}},"cancel"],...}}
>> {"id":0,"result":{"decision":"accept"}}
<< {"method":"serverRequest/resolved","params":{"requestId":0,...}}
<< {"method":"item/completed","params":{"item":{"command":"/bin/bash -lc ls","status":"completed","exitCode":0,...}}}
<< {"method":"item/commandExecution/requestApproval","id":1,"params":{"kind":"command","command":"/bin/bash -lc 'gh pr merge 12 --squash'",...}}
>> {"id":1,"result":{"decision":"decline"}}
>> {"id":7,"method":"turn/steer","params":{"expectedTurnId":"<turn>","input":[{"type":"text","text":"slop-bot's merge guard refused this: ..."}]}}
<< {"method":"item/completed","params":{"item":{"command":"/bin/bash -lc 'gh pr merge 12 --squash'","status":"declined",...}}}
<< {"id":7,"result":{"turnId":"<turn>"}}
```

The next model request carried the tool result `exec_command failed:
CreateProcess { message: "Rejected(\"rejected by user\")" }` and, in the same
request, the steered reason. What was measured:

- **Only `untrusted` asks.** Under `danger-full-access` it asked about every
  command, `ls` and `true` included. `on-request` and a granular policy with
  every flag on (`sandbox_approval`, `rules`, `mcp_elicitations`,
  `request_permissions`, `skill_approval`) asked about none, `gh pr merge`
  included: they only ask when the model requests an escalation.
- **It stays permissive.** One round trip per command, answered in-process:
  the `ls` above went from request to completion in about 10ms, running
  included. Tier 2 runs Codex's whole conformance suite guarded (steer,
  interrupt, long commands, resume, forced stop), and it passes unchanged.
- **A resume takes the policy.** A thread started under `never` (an older
  slop-bot, or one handed back from the TUI) asks once resumed with
  `untrusted` (tier 2: `TestCodexMergeGuardAfterResume`).
- **The transcript.** A declined command writes no `CommandExecution` item
  to the rollout, only the model's `function_call_output` (`rejected by
  user`), so `slop-bot transcript` doesn't show it as a command that ran.
- **Not for a sandboxed agent.** With `untrusted` and a `read-only` sandbox,
  an approved `touch made.txt` succeeded, while the same command under `never`
  failed with `Read-only file system`. An approved command runs outside the
  sandbox, so answering a non-permissive agent's approvals would escalate
  every command. A non-permissive Codex agent keeps `never` and is not
  guarded: it relies on the briefing and its sandbox, whose default
  (`workspace-write`) has no network.

**What isn't guarded on Codex** (beyond the bypasses above):

- **Input typed into a running terminal.** An approved `bash` (with a tty),
  then `write_stdin` with `gh pr merge 77\n`, ran the merge with no approval
  request (spike). The guard classifies a `writeStdin` approval when Codex
  sends one, but Codex sent none for this.
- **The user's own execpolicy rules.** A `prefix_rule(pattern=["gh"],
  decision="allow")` in `$CODEX_HOME/rules/` made Codex run `gh pr merge 12`
  without asking (spike). slop-bot runs on the user's Codex home (`trust.go`),
  so their allow rules apply.
- **MCP tools.** Codex's MCP tool calls aren't command approvals, so the
  classifier's merging-verb rule for MCP tools doesn't reach them. Not
  spiked: the scripted model's Codex gets no MCP tools.
- **The TUI hand-over.** `attach` resumes the thread in `codex resume
  --dangerously-bypass-approvals-and-sandbox`, which asks nobody. Session
  hooks would run there only with `--dangerously-bypass-hook-trust`, which
  also trusts any hooks the worktree's repo ships, so it isn't passed until
  someone decides that trade.

## Where it lives

- `internal/mergeguard`: the classifier, the daemon-side `Guard`, and the
  hook (`RunHook`)
- `internal/cli/hook.go`: `slop-bot hook pre-tool`. It is hidden, and not in
  the briefing's CLI reference, because the harness runs it, not the agent
- `internal/daemon/guard_routes.go`: the route, the `merge.refused` event
  (`checkTool`, which Codex is given too), and `tell --merge`
- `internal/agents/merge.go`: `AuthoriseMerge`, and clearing the grant when
  the ticket ends
- `internal/state/merge_grants.go`: the `merge_grants` table
- `internal/harness/claudecode`, `internal/harness/pi`: the hook wiring
- `internal/harness/codex/approvals.go`: Codex's approvals answered with the
  guard's verdict
