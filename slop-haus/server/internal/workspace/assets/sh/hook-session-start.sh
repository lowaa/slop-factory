#!/usr/bin/env bash
# slop-haus SessionStart hook.
#
# This used to be an http hook: SessionStart only ever injects context,
# which a plain response body can carry, so there was nothing a command
# hook did that an http one couldn't. That stopped being true the moment
# this workspace lost its static token -- an http hook's Authorization
# header can only ever be a fixed string, and there is no such string any
# more, only .slop-haus/token.sh's refreshed-on-demand access token. So
# this script exists to do the one thing an http hook no longer can: get a
# fresh token before asking.
#
# Everything else about the contract is unchanged, on purpose. Claude Code
# reads a command hook's stdout the same way it reads an http hook's
# response body, so this prints the server's JSON response verbatim on
# success -- hookSpecificOutput.additionalContext and all -- rather than
# reinterpreting it. On any failure it prints nothing and exits 0: this
# hook only ever adds context, so the fail-open answer is silence, not a
# block.
#
# (In current Claude Code, measured against 2.1.280, SessionStart fires
# before a project's settings.json is read, so a project-scoped hook here
# never actually runs -- see workspace.go's BriefingText comment for where
# the same content reaches the agent instead. This still matters for any
# other harness that does fire SessionStart, which is why it stays wired
# up rather than removed.)
#
# Installed into a workspace by `slop setup-workspace`; the copy there is
# the same bytes as this one.
set -uo pipefail

# A session a conductor's host started (SLOP_HAUS_HOST_SESSION, set by the host
# alone: run_remote_control.sh clears SLOP_HAUS_*, so a worker cannot carry it in by accident)
# is not a worker's. The host reports its usage per conversation and the
# server runs it, so this hook would count the same turns again, or send the
# session back to work as if it were a worker's. Claude Code runs hooks in the
# harness's environment, which is how the variable gets here.
[ -z "${SLOP_HAUS_HOST_SESSION:-}" ] || exit 0

server=""
for arg in "$@"; do
  case "$arg" in
    -server=* | --server=*) server="${arg#*=}" ;;
  esac
done
[ -n "$server" ] || server="${SLOP_HAUS_URL:-}"
server="${server%/}"

# The payload is read before anything can fail out from under it: Claude
# Code has already written it by the time this runs, and it must be
# drained either way or the write blocks.
payload=$(mktemp) || exit 0
described=""
trap 'rm -f "$payload" "$described"' EXIT
cat > "$payload"

[ -n "$server" ] || exit 0

script_dir="$(cd -- "$(dirname -- "$0")" &> /dev/null && pwd)"
token_script="$script_dir/token.sh"

# describe_payload adds this run's self-description -- harness, model,
# labels -- to the payload, through describe-session.py beside this script.
# Best effort: without python3, or on any failure, the payload goes as it
# came, and the server simply records no description.
describe_payload() {
  command -v python3 > /dev/null 2>&1 || return 0
  [ -f "$script_dir/describe-session.py" ] || return 0
  described=$(mktemp) || return 0
  if python3 "$script_dir/describe-session.py" < "$payload" > "$described" 2> /dev/null && [ -s "$described" ]; then
    mv -f "$described" "$payload"
  else
    rm -f "$described"
  fi
}
describe_payload

token=""
if [ -x "$token_script" ]; then
  # See hook-stop.sh's comment on this: $server (from -server=, which wins
  # over any SLOP_HAUS_URL a human has set) is what this script is about
  # to talk to, and has to be what token.sh resolves too, or the two could
  # silently disagree about which server this workspace's token is for.
  token=$(SLOP_HAUS_URL="$server" "$token_script" 2> /dev/null) || token=""
fi
[ -n "$token" ] || exit 0

# The token goes to curl on stdin, not in its arguments, where any process
# on the machine could read it.
response=$(
  printf 'Authorization: Bearer %s\n' "$token" |
    curl -sS --max-time 10 \
      -X POST "$server/hooks/session-start" \
      -H 'Content-Type: application/json' \
      -H @- \
      --data-binary "@$payload" \
      -w $'\n%{http_code}' 2>&1
)
[ $? -eq 0 ] || exit 0

status="${response##*$'\n'}"
body="${response%$'\n'*}"
[ "$status" = "200" ] || exit 0

printf '%s' "$body"
