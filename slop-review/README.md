# slop-review

LLM code review of a change. Agents run it on their own work before calling
it done (see the Self-review section of the repo's `CLAUDE.md`), and it works
on any git repository, not just this one. A review is one fresh session
that sees only what the change is meant to do, the diff and the
repository, never the author's reasoning.

| Directory | What it is |
|---|---|
| this directory | `review.sh` and the `slop-review` binary, the persona prompts (`personas/`), the orchestrator that splits large changes, and the benchmark (`bench.sh`). |
| [`report/`](report) | The run-report contract: wire types, validation and the HTTP client that posts a run. Only used when reporting to a server. |

```
slop-review/review.sh --intent /tmp/intent.md > /tmp/review.json
```

Output is one JSON object: `.review.verdict`, `.review.findings[]` and
`.markdown`. `review.sh --help` has every option.

## One pass

The defaults are a single large-context pass: the `standard` persona over
the whole change in one session (`--chunk-diff-bytes` and
`--max-diff-bytes` 1000000, `--context-budget-bytes` 2000000), with
`--max-turns=40` and reasoning effort `xhigh` where the provider takes one.
A blast-radius pass runs first, mapping what outside the diff depends on
what it changed; only a change over a megabyte is split, with a pass over
the seams between its parts. The other personas (`security`,
`concurrency`, `test-efficacy`, or `--persona=all`) are there to choose
explicitly; nothing requires them.

| Provider | Effort it is sent by default |
|---|---|
| `claude` | `xhigh`, as `claude -p --effort xhigh` |
| `codex` | `xhigh`, as `-c model_reasoning_effort="xhigh"` |
| `chatgpt` | `xhigh`, as the Responses API's `reasoning.effort` |
| `openrouter`, `openai` | none: they take no effort parameter, so the model's default applies (on OpenRouter, a `-pro` slug selects more) |

A chosen effort (`--reasoning-effort`, `SLOP_REVIEW_REASONING_EFFORT`, or the
central config) beats the default, and is still refused where the provider
can't take it. An empty effort in the central config gets the default.

## Providers

| `--provider` / `SLOP_REVIEW_PROVIDER` | Model (default) | Needs |
|---|---|---|
| `claude` (default) | `claude-sonnet-5-5` | `claude` on PATH, logged in, and Claude Code's sandbox (bubblewrap and socat on Linux). Local machines only. |
| `codex` | `gpt-5.6-luna` | `codex` on PATH, logged in. Local machines only. |
| `openrouter` | `openai/gpt-5.6-luna` | `OPENROUTER_API_KEY` |
| `openai` | `gpt-5.6-luna` | `OPENAI_API_KEY` |
| `chatgpt` | `gpt-5.6-luna` | `pi` 0.99.1+, logged in with Sign in with ChatGPT. Local machines only. |

`claude` and `codex` hand each session to the vendor's CLI, held read-only
and offline by its own sandbox; if the sandbox can't start, the run is
refused, never made unsandboxed. Everything slop-review sends a provider is
redacted, and for `claude` it also denies reads of secret-shaped files in
the checkout and of credential stores under `$HOME`, and of the login
directories named by `$CODEX_HOME` and `$PI_CODING_AGENT_DIR` (a supervisor
keeps them outside `$HOME`). Unset or empty variables are skipped, a
relative one is resolved against the working directory, and one that does
not exist is skipped like any other missing path. A run is refused if one of
them is, or contains, the checkout. `$CLAUDE_CONFIG_DIR` is not denied as a
whole, since claude reads its own settings from it; only its
`.credentials.json` is. Because those CLIs open
files themselves, their reviews carry a warning that the denylist can't
cover everything.

## Settings and the .env

Every `SLOP_REVIEW_*` setting is a flag, an environment variable or a line
in a `.env` file, in that order of precedence: a flag beats the
environment, which beats the `.env` (an empty environment variable counts as
unset), which beats the defaults. `review.sh` reads the `.env` at the root of
its own checkout, or, in a linked worktree (which has no copy of the
git-ignored file), the main worktree's, and passes it as
`--env-file=<path>`; `--env-file=<path>` chooses another, and `--env-file=`
none. It is never the reviewed repository's: a change under review must not
choose its own reviewer, or where its report goes. A program that runs
`review.sh` with settings of its own, as a supervisor does, passes
`--env-file=` so that a checkout's `.env` doesn't fill in what it left unset.

The file is parsed, never sourced, since it may hold other tools' secrets:
only `SLOP_REVIEW_*` lines are read (`KEY=VALUE`, an optional `export `,
`#` comments, values optionally in `''` or `""`, taken literally with no
escapes or `$VAR` expansion), and a provider's API key is never taken from
it. Values are never logged; stderr names the keys a run took from it.

## Fallback

A fallback is a second provider a session is redone on, from the start,
when the primary fails it with one of the chosen failure classes:
`usage_limit` (the default), `credits`, `auth` or `backend`
(`providerconfig.TriggerClasses`). Anything else (a config mistake, running
out of turns, an error not recognised as one of those) fails the run as
before. A primary that can't start at all, its login gone, takes the
fallback for every session if `auth` is chosen. slop-review never switches
provider by itself: only to a fallback someone configured.

It comes from one of two places, never both:

- the central provider config, on a run that reports to a server that has
  one (see Reporting); it wins unchanged.
- otherwise, the local settings: `SLOP_REVIEW_FALLBACK_PROVIDER`,
  `_FALLBACK_MODEL`, `_FALLBACK_REASONING_EFFORT` and `_FALLBACK_ON`
  (comma-separated classes), or `--fallback-provider`, `--fallback-model`,
  `--fallback-reasoning-effort` and `--fallback-on`. They are built into the
  same `providerconfig.Settings` a central config carries, so the same
  validation (a provider other than the primary, classes from the list
  above), defaults and run-time path apply. For example, in the `.env`:

  ```
  SLOP_REVIEW_PROVIDER=codex
  SLOP_REVIEW_MODEL=gpt-5.6-luna
  SLOP_REVIEW_REASONING_EFFORT=xhigh
  SLOP_REVIEW_FALLBACK_PROVIDER=claude
  SLOP_REVIEW_FALLBACK_MODEL=claude-haiku-5-5
  SLOP_REVIEW_FALLBACK_REASONING_EFFORT=xhigh
  ```

`--provider`, `--model` or `--reasoning-effort` make a run a local one-off,
and a one-off runs without the `SLOP_REVIEW_FALLBACK_*` fallback: that was
chosen to back up `SLOP_REVIEW_PROVIDER`, not whatever the command line
names. The fallback flags give a one-off its own, taken from the command
line alone (`--fallback-provider=codex` never picks up a
`SLOP_REVIEW_FALLBACK_MODEL` meant for claude).

When a fallback is taken, stderr says why (the primary's error), the output's
`.meta.fell_back` has `from`, `to` and `class`, each of `.sessions[]` says the
provider and model it ran on, and the review carries a warning. A run that
reports records it as the run's `fallback`.

Under a slop-haus supervisor, which runs this for its agents' changes, logins
are the supervisor's scoped ones, never those of the user it runs as:
`CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `PI_CODING_AGENT_DIR` point under its
root, and API keys and Claude Code's token come from its secrets only. It
checks the provider's login before starting a review and refuses without one
(`slop-haus/docs/guide/supervisor.md`, "Reviews"). `HOME` is left as it is:
the denylist above is taken from it.

## Reporting

Optional. Given `SLOP_REVIEW_REPORT_URL` and a token, each run is posted to
a report server that speaks the `report/` contract, and the provider comes
from that server's central config. The server is slop-haus
(`slop-haus/server`); unset, nothing is sent anywhere.

The token is either:

- `SLOP_REVIEW_REPORT_TOKEN`, a fixed bearer token; or
- `SLOP_REVIEW_REPORT_TOKEN_COMMAND` (`--report-token-command`), for a token
  that expires, as slop-haus agent tokens do. It is an executable, by path or
  name on PATH, whose stdout (trimmed) is the token. It is run before every
  request that needs one (post, spool retry, provider-config fetch, label),
  with a 15 second limit. Arguments may follow, split on whitespace; there is
  no shell, so no quoting or expansion. If the whole string is the path of
  an executable file it is run as is, so a path with spaces works on its
  own; otherwise it is split, and a path with spaces needs a wrapper
  script. A slop-haus workspace's `token.sh` is such a command.

If both are set, the explicit `SLOP_REVIEW_REPORT_TOKEN` wins and the command
is ignored, with a warning saying so: a supervisor may set the command
for every run, and your own token must not break the review. A command that
fails, times out or
prints nothing (or more than one token) is a reporting failure like an
unreachable server: the review still completes, the run is spooled for a
later invocation, and a warning says why. The command's output is never
logged; only its stderr's first line appears in the warning, with the token
scrubbed.

## Tests

`test.sh` and `report/test.sh`: plain Go (gofmt, vet, race-enabled
tests), plus `review.bats` and the shellspec suite (`spec/`) for the wrapper
script. Neither needs Docker or a model.
