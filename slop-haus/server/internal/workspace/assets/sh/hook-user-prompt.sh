#!/usr/bin/env bash
# slop-haus UserPromptSubmit hook. Claude Code runs this when a human sends
# the agent a prompt, asynchronously (settings.json's "async": true), so it
# never holds the prompt up; pi's extension runs it the same way on input.
# It tells the server a human has spoken, which gives the run a fresh budget
# of Stop blocks: see the server's UserPromptSubmit handler for why a nudge
# needs one.
#
# It says nothing, ever. For this event Claude Code adds a command hook's
# stdout to the prompt's context, and exit 2 would erase the prompt, so
# every path below prints nothing and exits 0 -- a server that is down,
# slow, or refuses the request costs the nudge its fresh budget, never the
# human their prompt. There are no retries for the same reason: the next
# prompt tries again.
#
# Installed into a workspace by `slop setup-workspace`; the copy there is
# the same bytes as this one.
set -uo pipefail

# A session a conductor's host started (SLOP_HAUS_HOST_SESSION, set by the host
# alone) is not a worker's: the server runs it, and has nothing to reset.
[ -z "${SLOP_HAUS_HOST_SESSION:-}" ] || exit 0

server=""
for arg in "$@"; do
  case "$arg" in
    -server=* | --server=*) server="${arg#*=}" ;;
  esac
done
[ -n "$server" ] || server="${SLOP_HAUS_URL:-}"
server="${server%/}"

# Drained first, whatever happens next: Claude Code has written it already.
payload=$(mktemp) || exit 0
trap 'rm -f "$payload"' EXIT
cat > "$payload"

[ -n "$server" ] || exit 0

script_dir="$(cd -- "$(dirname -- "$0")" &> /dev/null && pwd)"
token=""
if [ -x "$script_dir/token.sh" ]; then
  # The server this hook talks to, named outright -- see hook-stop.sh.
  token=$(SLOP_HAUS_URL="$server" "$script_dir/token.sh" 2> /dev/null) || token=""
fi
[ -n "$token" ] || exit 0

# The token goes to curl on stdin, not in its arguments, where any process
# on the machine could read it. The response is discarded.
printf 'Authorization: Bearer %s\n' "$token" |
  curl -sS --max-time "${SLOP_HAUS_PROMPT_HOOK_TIMEOUT:-5}" -o /dev/null \
    -X POST "$server/hooks/user-prompt-submit" \
    -H 'Content-Type: application/json' \
    -H @- \
    --data-binary "@$payload" > /dev/null 2>&1
exit 0
