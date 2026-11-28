# The merge guard

Defence in depth behind the briefing (plan task 4.10). An agent on a ticket
without `AutoMerge` is told to stop at `slop-bot ready-to-merge`. Where its
harness has a pre-tool hook, slop-bot also refuses the obvious merge commands
until the user says go.

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
   with a tell whose whole text is `merge`. The grant goes in `state.db`
   (`merge_grants`), so it survives a restart. It belongs to the ticket, so a
   resumed agent keeps it. It is cleared when the ticket ends (done or
   cancelled), and the guard ignores a grant on an ended ticket. A grant isn't
   used up by one merge, because merging can take several commands.
4. **Fail safe:** when the daemon can't be asked (it is stopped, the socket is
   gone, or the token is unknown), the hook classifies the command itself. It
   refuses a merge and allows everything else. When the daemon can't read the
   ticket or the grant, it refuses the merge.

An allow writes nothing, so the harness's own permission rules still apply.
The hook never approves anything those rules would have asked about.

## What counts as a merge

The classifier parses the command as bash (`mvdan.cc/sh`). It looks at every
command in it: lists, pipelines, subshells, `$(...)`, `if` bodies, wrappers
(`env`, `sudo`, `nohup`, `timeout`, `xargs`, ...), `bash -c`/`sh -c` scripts
and `eval`. Quotes and backslashes are resolved, so `"gh" pr mer\ge` is
caught. The merges it finds are:

- `gh pr merge` (with `--auto` too) and `glab mr merge`
- `gh api`:
  - the REST merge endpoints (`pulls/<n>/merge`, `repos/<o>/<r>/merges`) with
    any method but GET or HEAD
  - GraphQL merge mutations (`mergePullRequest`, `enablePullRequestAutoMerge`,
    `enqueuePullRequest`, `mergeBranch`), and `gh api graphql --input`, whose
    query it can't see
- `curl` (or `wget`, `http`, `xh`) aimed at those endpoints, or carrying those
  mutations
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
    `cd` and `git -C`
  - a destination it can't read (`git push origin $BRANCH`). The exception is
    `$(git branch --show-current)` and its equivalents, which resolve to the
    current branch
- an MCP tool whose name matches `mcp__*merge*` (for example GitHub's
  `merge_pull_request`)
- a command it can't take apart that mentions `merge` or `push`. That covers a
  parse failure, a script fed to a shell's stdin (`echo ... | bash`, a
  heredoc), a computed command name (`$GH pr merge`), and shells nested more
  than four deep

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
  and `glab`, and an API call without `curl` or `gh`.
- `cd` tracking is in source order, and doesn't model a subshell's scope or a
  failed `cd`.
- Stripping the token (`env -u SLOP_BOT_AGENT_TOKEN ...`) only affects the
  agent's own slop-bot calls. The hook runs from the harness, outside the
  agent's command, with the harness's own environment.
- The agent can edit or delete the hook's inputs where it can write to them:
  pi's extension file sits beside the briefing, in slop-bot's state directory.
  This is the same trust boundary as the briefing.

## Per harness

| Harness / driver | Enforced | How |
| --- | --- | --- |
| claude-code/stream-json | yes | A session-only `PreToolUse` hook (matcher `Bash\|mcp__.*`), passed as JSON to `--settings`. It adds to the user's settings and writes nothing. The TUI hand-over (`attach`) gets the same flags. A `deny` holds under `--dangerously-skip-permissions` (tier 2: `TestClaudeCodeMergeGuard`). |
| pi/rpc | yes | slop-bot's extension `merge-guard.ts`, loaded with `--extension`, which project trust doesn't gate. It is written beside the agent's briefing file, never in the worktree. Its `tool_call` handler runs the hook for `bash` and `mcp*` tools and returns `{block: true, reason}` on a deny. pi runs nested (codemode) calls through the same handler. If the hook can't run at all, the extension refuses a call that mentions merge or push. Tier 2: `TestPiMergeGuard`. |
| codex/app-server | **no: a known gap** | See below. |
| agy/tui (phase 8) | not yet | Its session-only hooks (`PreToolUse` answering a decision) are the place. Wire `slop-bot hook pre-tool` in when the driver lands. |

### Codex: the gap and what would close it

Codex's hooks are Claude Code-compatible, and `-c hooks.PreToolUse=...` loads
them for one session. Under `app-server`, though, session-flag hooks stay
`untrusted` and never fire. Neither `--dangerously-bypass-hook-trust` nor `-c`
can trust them, and persisted trust would be written to the user's
`~/.codex/config.toml` (codex protocol spike). Approvals don't help either:
slop-bot runs Codex with `approvalPolicy: never`, so no command reaches the
client for a verdict. A Codex agent therefore relies on the briefing alone.
Options to close the gap, each needing a decision or a spike:

1. **Route commands through approvals.** Use an approval policy that asks
   about commands (`untrusted`, or a granular policy if Codex offers one). In
   `approvals.go`, answer `item/commandExecution/requestApproval` with the
   classifier's verdict: decline a merge, accept everything else. This is the
   most promising option, but it is unspiked. It has to be measured for how
   many requests every turn would make and how it interacts with the sandbox
   and `Permissive`. It also changes how every Codex agent runs.
2. **A per-agent `CODEX_HOME`** with the hook's trust persisted there.
   `trust.go` rejects this for now: a copied login breaks the shared refresh
   token, and the copied config drifts.
3. **Upstream:** a session-scoped way to trust `-c` hooks under app-server.
4. **The TUI hand-over** (`codex resume`) does run session hooks with
   `--dangerously-bypass-hook-trust`. That flag also trusts any hooks the
   worktree's repo ships, so it isn't passed until someone decides that trade.

## Where it lives

- `internal/mergeguard`: the classifier, the daemon-side `Guard`, and the
  hook (`RunHook`)
- `internal/cli/hook.go`: `slop-bot hook pre-tool`. It is hidden, and not in
  the briefing's CLI reference, because the harness runs it, not the agent
- `internal/daemon/guard_routes.go`: the route, the `merge.refused` event, and
  `tell --merge`
- `internal/agents/merge.go`: `AuthoriseMerge`, and clearing the grant when
  the ticket ends
- `internal/state/merge_grants.go`: the `merge_grants` table
- `internal/harness/claudecode`, `internal/harness/pi`: the hook wiring
