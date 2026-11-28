#!/usr/bin/env bash
# Convenience entry point for slop-review: resolves the persona library
# relative to this script (so it works from any cwd) and runs the tool.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Captured before cd'ing into SCRIPT_DIR below, so --repo-root defaults to
# wherever the caller actually ran this from, not slop-review's own
# directory; an explicit --repo-root later in "$@" still overrides it.
INVOKED_FROM="$(pwd)"

usage() {
  cat << 'EOF'
Usage: review.sh [-m|--model <slug>] [slop-review flags...]
       review.sh label <finding-id> --verdict=confirmed|false_positive|fabricated|duplicate|wont_fix
                 [--reason=...] [--fixed-in=<commit>]   (needs SLOP_REVIEW_REPORT_URL and a token)

  --provider <name>   Where the review model runs. Also settable as
                      SLOP_REVIEW_PROVIDER (in the environment or the .env:
                      see "Settings" below).
                        chatgpt     OpenAI's Responses API through
                                    a ChatGPT subscription, logged in with
                                    pi. Local machines only, never CI.
                        openrouter  OpenRouter, paid per token with
                                    OPENROUTER_API_KEY.
                        openai      OpenAI's API, paid per token with
                                    OPENAI_API_KEY.
                        codex       The Codex CLI (`codex exec`) under your
                                    Codex login, in its read-only, offline
                                    sandbox. Local machines only, never CI.
                        claude      (default) Claude Code (`claude -p`) under your
                                    Claude Code login, in its read-only,
                                    offline sandbox. Local machines only.
                      codex and claude read the repository with their own
                      tools, so the secret-shaped file denylist cannot
                      cover what they open: what slop-review sends them is
                      still redacted, and the review warns about the rest.
                      claude's sandbox also denies reads of credential
                      stores under $HOME and of the directories named by
                      CODEX_HOME and PI_CODING_AGENT_DIR (and
                      CLAUDE_CONFIG_DIR's .credentials.json); it refuses to
                      run if one of those directories contains the checkout.
                      A run that reports to the report server uses the
                      tenant's central provider config (its Providers
                      page) over SLOP_REVIEW_PROVIDER/_MODEL/
                      _REASONING_EFFORT, and that config may name a
                      fallback. --provider, --model or --reasoning-effort
                      on the command line make the run a local one-off
                      that ignores it. Without a central config, the
                      fallback is the local one (--fallback-provider
                      below). Without a fallback, a failing provider fails
                      the run, which says so.
  -m, --model <slug>  Model to review with. Defaults to the provider's own:
                        chatgpt, openai: gpt-5.6-luna
                        openrouter:      openai/gpt-5.6-luna
                        codex:           gpt-5.6-luna
                        claude:          claude-sonnet-5-5
                      Known-good OpenRouter slugs:
                        openai/gpt-5.6-luna
                        openai/gpt-5.6-luna-pro (high reasoning effort)
                      On OpenRouter the -pro suffix is what selects high
                      reasoning effort: it exposes effort as separate slugs,
                      so --reasoning-effort is rejected there.
                      (deepseek/deepseek-v4-pro-0813 was listed here, but
                      repeatedly exhausted --max-turns without submitting a
                      review, and once reported a finding about a file that
                      does not exist in this repo)
  --reasoning-effort <level>
                      chatgpt and codex: none, minimal, low, medium, high,
                      xhigh or max (gpt-5.6-luna has no "minimal"); claude:
                      low, medium, high, xhigh or max. Unset, it is xhigh
                      for chatgpt, codex and claude (claude takes it as
                      `claude -p --effort`, codex as model_reasoning_effort).
                      openrouter and openai take no effort parameter, so
                      they are sent none and keep the model's default
                      (choose a -pro slug on OpenRouter for more). An
                      empty effort in the central config gets the same
                      default.
  --fallback-provider <name>, --fallback-model <slug>,
  --fallback-reasoning-effort <level>
                      A provider to redo a session on when the primary
                      fails it with one of --fallback-on: the local
                      counterpart of the central config's fallback, and
                      only used when no central config applies. Also
                      SLOP_REVIEW_FALLBACK_PROVIDER, _FALLBACK_MODEL and
                      _FALLBACK_REASONING_EFFORT. Model and effort default
                      as for the primary; the provider must differ from it.
                      A one-off run (--provider, --model or
                      --reasoning-effort given) runs without the
                      SLOP_REVIEW_FALLBACK_* settings, which were chosen to
                      back up SLOP_REVIEW_PROVIDER; give --fallback-provider
                      on the command line too for a fallback, which is then
                      taken from the command line alone.
  --fallback-on <classes>
                      Comma-separated failure classes that take the
                      fallback: usage_limit, credits, auth, backend.
                      Default usage_limit (also SLOP_REVIEW_FALLBACK_ON).
                      A primary that can't start (lost login: auth) goes
                      straight to the fallback if auth is listed. Other
                      failures (a config mistake, running out of turns, an
                      error not recognised as one of these) never take
                      it. When it is taken, stderr, .meta.fell_back (from,
                      to, class), each session's provider and a review
                      warning say so.
  --intent <file>     What the change is meant to do, typically the PR
                      description (also SLOP_REVIEW_INTENT; read up to 32 KiB).
                      Every session is told it is the author's claim to
                      verify, not fact. A relative path is relative to where
                      you ran review.sh. The branch's commit messages are
                      given to every session whether or not this is set.
  --run-checks        codex and claude only: let the reviewer build and run
                      tests to check a claim. The repository stays read-only
                      and the network stays off; Go's build cache and a
                      scratch temp dir become writable. Off by default
                      (also SLOP_REVIEW_RUN_CHECKS=true).
  -h, --help          Show this help.

By default a review is one pass: `review.sh --intent <file>` runs the
standard persona over the whole change in a single session, 40 turns, at
the effort above (after a blast-radius pass that maps what outside the diff
depends on it). Other personas are an explicit choice, e.g.:
  review.sh --persona=security --from-commit-hash=abc123
  review.sh --persona=standard,concurrency
  review.sh --persona=all
Every other flag is passed straight through to slop-review, e.g.:
  review.sh --max-turns=25 --allow-secrets=true

A change over --chunk-diff-bytes is split into package-sized parts reviewed
in parallel, then a pass over the seams between them. Tune with:
  --chunk-diff-bytes      diff per part (default 1000000)
  --max-diff-bytes        diff inlined per session; the rest is fetched on
                          demand (default 1000000)
  --context-budget-bytes  conversation size before old tool results are
                          elided (default 2000000; must exceed
                          --max-diff-bytes)
  --max-turns             tool-call turns per session (default 40)
  --parallelism           concurrent sessions (default 4)
  --seam-pass=false       skip the cross-part pass
A persona whose front matter says "chunking: whole" is never split.

Prerequisites per provider:
  chatgpt     pi (0.99.1 or newer) on PATH, logged in: run `pi`, then
              `/login` and choose Sign in with ChatGPT (provider openai).
              slop-review checks `pi auth check --provider openai` before
              starting, and fails with these instructions if it isn't ready.
              The token comes from pi for each run and is never written
              anywhere or passed on a command line.
  openrouter  OPENROUTER_API_KEY in the environment.
  openai      OPENAI_API_KEY in the environment.
  codex       codex on PATH, logged in (`codex login`). Runs in a fresh
              CODEX_HOME per run, holding a copy of only the login.
  claude      claude on PATH, logged in (`claude auth login`, or
              CLAUDE_CODE_OAUTH_TOKEN as in a slop-haus slot), and Claude
              Code's sandbox tools (bwrap and socat on Linux). The run is
              refused rather than made unsandboxed.
A missing or lost login fails the run saying so (a configured fallback on
auth takes it); slop-review never switches provider by itself, only to a
fallback someone configured.
To change provider: SLOP_REVIEW_PROVIDER=<name> (e.g. in the .env), or
--provider=<name> for one run. A machine that reports to a report server
takes the provider from the server's central config instead.

Settings: every SLOP_REVIEW_* variable may also be set in a .env file. This
script reads the one at the root of its own checkout (in a linked worktree,
which has no copy of the git-ignored file, the main worktree's), never the
reviewed repository's, and passes it as --env-file=<path>; give
--env-file=<path> to read another, or --env-file= for none. Only
SLOP_REVIEW_* lines are read: KEY=VALUE, an optional "export ", # comments,
and values optionally in '' or "" (taken literally: no escapes or $VAR
expansion). The file is parsed, never sourced, and its values are never
logged; stderr names the keys a run took from it. Precedence: command-line
flags, then the environment (a variable set and non-empty), then the .env,
then the defaults.

Needs git 2.40 or newer on PATH (for `git check-attr --source`, used to
tell whether a changed file was already generated). slop-review fails
with a clear error, naming the version found, if it isn't.

Reporting each run to the report server (optional, and not part of this
repo -- unset, nothing is sent anywhere):
  --report-to <url>              also settable as SLOP_REVIEW_REPORT_URL.
                                  Must be https, unless the host is loopback
                                  (127.0.0.0/8, ::1, localhost).
  SLOP_REVIEW_REPORT_TOKEN       the ingest token. Env-var-only, like the
                                  provider API keys -- there is no flag for
                                  it, so it can't land in shell history.
  --report-token-command <cmd>   also settable as SLOP_REVIEW_REPORT_TOKEN_COMMAND:
                                  for a token that expires. An executable
                                  (a path, or a name on PATH) whose stdout,
                                  trimmed, is the token, run for every
                                  request that needs one. Arguments may follow,
                                  split on whitespace; it is run directly, not
                                  through a shell, so there is no quoting or
                                  expansion. A string that is itself the path
                                  of an executable file is run unsplit (so a
                                  path with spaces works alone); use a
                                  wrapper script for anything fancier. A 15s limit applies. A
                                  failing or empty command only warns, like
                                  any reporting failure, and its output is
                                  never logged. If
                                  SLOP_REVIEW_REPORT_TOKEN is also set it
                                  wins, and the command is ignored with a
                                  warning (a supervisor may set the command
                                  for every run; your own token takes
                                  precedence).
  --report-spool-dir <dir>       where a run that failed to post waits to be
                                  retried (default ~/.cache/slop-review/spool).
  --provider-config-cache-dir <dir>
                                 where the last central provider config
                                  fetched is kept, for when the server is
                                  unreachable (default
                                  ~/.cache/slop-review/provider-config).
  --report-remote <name>         which git remote identifies this repo
                                  (default origin), also settable as
                                  SLOP_REVIEW_REPORT_REMOTE.
  --report-repo <url>            overrides the remote URL a report is filed
                                  under outright, for a repo whose remote
                                  isn't the shared identity (a fork, say);
                                  also settable as SLOP_REVIEW_REPORT_REPO.
A repo with no remote (and no --report-repo override) can't be filed under
any identity, so reporting is skipped with a warning rather than attempted.
A run that fails to post is spooled and retried on the next invocation with
reporting enabled; the review's own result is unaffected either way, and a
warning notes when a run wasn't reported. This script passes --tool-version
(and whether that checkout is dirty) itself, from its own git checkout, and
--source-dir, the directory the run fingerprints its source from.
EOF
}

# env_file prints the .env slop-review reads its SLOP_REVIEW_* settings
# from, if there is one: at the root of the checkout this script is in, or,
# in a linked worktree (which has no copy of the git-ignored file), at the
# main worktree's root. Never the reviewed repository's: a change under
# review must not choose its own reviewer or where its report goes.
env_file() {
  local top common
  top="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2> /dev/null)" || return 0
  if [[ -f "$top/.env" ]]; then
    printf '%s\n' "$top/.env"
    return 0
  fi
  common="$(git -C "$SCRIPT_DIR" rev-parse --path-format=absolute --git-common-dir 2> /dev/null)" || return 0
  if [[ -f "$(dirname "$common")/.env" ]]; then
    printf '%s\n' "$(dirname "$common")/.env"
  fi
}
# First among the arguments, so an explicit --env-file later wins.
ENV_FILE_ARGS=()
ENV_FILE="$(env_file)"
if [[ -n "$ENV_FILE" ]]; then
  ENV_FILE_ARGS+=("--env-file=$ENV_FILE")
fi

# `label` is a subcommand, not a review: none of the review-only flags added
# below apply to it.
if [[ "${1:-}" == "label" ]]; then
  shift
  cd "$SCRIPT_DIR"
  exec go run ./cmd/slop-review label ${ENV_FILE_ARGS+"${ENV_FILE_ARGS[@]}"} "$@"
fi

# absolute_path makes a path relative to the caller's directory absolute.
absolute_path() {
  case "$1" in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s/%s\n' "$INVOKED_FROM" "$1" ;;
  esac
}

args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m | --model)
      # Accept the two-token form as well as --model=<slug>, which needs no
      # handling here since it falls through to the passthrough case below.
      if [[ $# -lt 2 ]]; then
        echo "review.sh: $1 requires a model slug" >&2
        exit 2
      fi
      args+=("--model=$2")
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    --intent)
      # Resolved here because the script cd's into SCRIPT_DIR below, which
      # would otherwise make a relative path mean something else.
      if [[ $# -lt 2 ]]; then
        echo "review.sh: $1 requires a file" >&2
        exit 2
      fi
      args+=("--intent=$(absolute_path "$2")")
      shift 2
      ;;
    --intent=*)
      args+=("--intent=$(absolute_path "${1#--intent=}")")
      shift
      ;;
    *)
      args+=("$1")
      shift
      ;;
  esac
done
if [[ -n "${SLOP_REVIEW_INTENT:-}" ]]; then
  SLOP_REVIEW_INTENT="$(absolute_path "$SLOP_REVIEW_INTENT")"
  export SLOP_REVIEW_INTENT
fi

# The tool's own version identifies which build of slop-review produced a
# report (see report.ToolVersion): this script's checkout's git SHA, and
# whether that checkout has uncommitted changes. Best-effort -- if git
# isn't available or $SCRIPT_DIR isn't a checkout at all, slop-review still
# runs, just without a --tool-version (internal/runreport falls back to
# the Go build's own embedded VCS info, if any).
TOOL_VERSION_ARGS=()
TOOL_VERSION="$(git -C "$SCRIPT_DIR" rev-parse --short HEAD 2> /dev/null || true)"
if [[ -n "$TOOL_VERSION" ]]; then
  TOOL_VERSION_ARGS+=("--tool-version=$TOOL_VERSION")
  if [[ -n "$(git -C "$SCRIPT_DIR" status --porcelain -- . 2> /dev/null)" ]]; then
    TOOL_VERSION_ARGS+=("--tool-version-dirty=true")
  else
    TOOL_VERSION_ARGS+=("--tool-version-dirty=false")
  fi
fi

cd "$SCRIPT_DIR"
# ${args+...} rather than a bare "${args[@]}": expanding an empty array is an
# unbound-variable error under `set -u` on bash < 4.4. TOOL_VERSION_ARGS
# comes before "$@" so an explicit --tool-version in the caller's own args
# still overrides it (slop-review's flags are last-one-wins).
exec go run ./cmd/slop-review \
  --persona-dir="$SCRIPT_DIR/personas" \
  --source-dir="$SCRIPT_DIR" \
  --repo-root="$INVOKED_FROM" \
  ${ENV_FILE_ARGS+"${ENV_FILE_ARGS[@]}"} \
  ${TOOL_VERSION_ARGS+"${TOOL_VERSION_ARGS[@]}"} \
  ${args+"${args[@]}"}
