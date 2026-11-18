#!/usr/bin/env bash
# slop-haus Stop hook. Claude Code runs this when an agent tries to stop;
# exiting 2 sends it back to work, with whatever this prints on stderr as
# the reason. Exiting 0 lets it stop.
#
# Bash rather than a compiled binary because a workspace is usually just a
# checkout on someone's machine, with no Go toolchain and no slop-haus
# source anywhere near it. An http hook cannot do this job -- Claude Code
# surfaces an http hook's "block" and lets the agent stop anyway -- so the
# hook has to be a command, and a command has to already exist on disk.
#
# The decision stays server-side. This only turns the answer into an exit
# code.
#
# The token comes from token.sh, installed beside this script: there is no
# static token any more to read out of the environment, only this
# workspace's own OAuth credentials, refreshed on demand and never printed
# anywhere token.sh doesn't have to.
#
# Installed into a workspace by `slop setup-workspace`; the copy there is
# the same bytes as this one.

# Deliberately not `set -e`. Every failure below is handled by letting the
# agent stop, and an early exit would skip that reasoning.
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

script_dir="$(cd -- "$(dirname -- "$0")" &> /dev/null && pwd)"
token_script="$script_dir/token.sh"

# Fail open, here and at every failure below: a hook that cannot reach the
# server must let the agent stop. An outage should idle the fleet, not
# freeze it -- the same reasoning Claude Code applies to a hook that errors.
if [ -z "$server" ]; then
  echo "slop-haus: no server url in the environment; letting the agent stop" >&2
  exit 0
fi

token_err=$(mktemp) || exit 0
token=""
if [ -x "$token_script" ]; then
  # $server is what this hook is actually about to talk to -- settings.json
  # bakes in an absolute -server= argument, which wins over any
  # SLOP_HAUS_URL a human has set in their shell. token.sh has no such
  # argument and would otherwise resolve its own address independently
  # (SLOP_HAUS_URL first, same as this script, but this script's -server=
  # takes priority over that same variable) -- passed explicitly here so
  # the two can never point at different servers.
  token=$(SLOP_HAUS_URL="$server" "$token_script" 2> "$token_err") || token=""
fi
if [ -z "$token" ]; then
  reason=$(tr '\n' ' ' < "$token_err")
  rm -f "$token_err"
  echo "slop-haus: no valid token (${reason:-token.sh is missing}); letting the agent stop" >&2
  exit 0
fi
rm -f "$token_err"

# Via a file rather than an argument: the payload is Claude Code's, not
# ours, and its size is not something to bet an argv limit on.
payload=$(mktemp) || exit 0
described=""
trap 'rm -f "$payload" "$described"' EXIT
cat > "$payload"

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

# A restarting server -- every deploy -- is unreachable for some seconds, or
# answers 502-504 from the proxy in front of it. Sessions that were idle
# are told to stop when that happens, so they all arrive here at once:
# failing open straight away would put the whole idle fleet
# to sleep on every deploy. Those answers are retried for a while first;
# any other is acted on at once. The knobs are for tests. The clock is whole
# seconds, so a short window can be spent before the first answer arrives;
# a test that needs "retried, then gave up" bounds the attempts instead.
retry_for="${SLOP_HAUS_STOP_RETRY_FOR:-45}"
retry_every="${SLOP_HAUS_STOP_RETRY_EVERY:-3}"
retry_attempts="${SLOP_HAUS_STOP_RETRY_ATTEMPTS:-0}"
give_up_at=$(($(date +%s) + retry_for))
attempts=0

while :; do
  # The status code is appended on its own line, so one call gives both.
  # The token goes to curl on stdin, not in its arguments, where any
  # process on the machine could read it.
  response=$(
    printf 'Authorization: Bearer %s\n' "$token" |
      curl -sS --max-time 10 \
        -X POST "$server/hooks/stop" \
        -H 'Content-Type: application/json' \
        -H @- \
        --data-binary "@$payload" \
        -w $'\n%{http_code}' 2>&1
  )
  curl_exit=$?
  attempts=$((attempts + 1))
  # Unquoted, like the assignments below: $'\n' is not ANSI-C quoted
  # inside double quotes.
  attempt_status=${response##*$'\n'}
  if [ "$curl_exit" -eq 0 ]; then
    case "$attempt_status" in
      502 | 503 | 504) ;;
      *) break ;;
    esac
  fi
  [ "$(date +%s)" -lt "$give_up_at" ] || break
  [ "$retry_attempts" -le 0 ] || [ "$attempts" -lt "$retry_attempts" ] || break
  sleep "$retry_every"
done

if [ "$curl_exit" -ne 0 ]; then
  # curl writes its -w output even when it fails, so the status line is
  # tacked onto the error text; the first line is the part that says what
  # went wrong. Taken in its own unquoted assignment because $'\n' is not
  # ANSI-C quoted inside double quotes.
  reason=${response%%$'\n'*}
  echo "slop-haus: $server unreachable: $reason" >&2
  exit 0
fi

status="${response##*$'\n'}"
body="${response%$'\n'*}"

if [ "$status" != "200" ]; then
  echo "slop-haus: stop hook: HTTP $status: $body" >&2
  exit 0
fi

# field reads one value out of hookSpecificOutput. python3 because this
# repo's scripts already rely on it and jq is not safe to assume; the grep
# is a last resort for a machine with neither, and is why `reason` is
# looked up separately rather than alongside the decision -- a reason can
# contain anything, including something that looks like the next field.
field() {
  if command -v python3 > /dev/null 2>&1; then
    printf '%s' "$body" | python3 -c '
import json, sys
try:
    doc = json.load(sys.stdin)
except Exception:
    sys.exit(0)
if not isinstance(doc, dict):
    sys.exit(0)
out = doc.get("hookSpecificOutput")
out = out if isinstance(out, dict) else {}
value = out.get(sys.argv[1]) or ""
if not value and sys.argv[1] == "reason":
    value = doc.get("systemMessage") or ""
sys.stdout.write(str(value))
' "$1" 2> /dev/null
  else
    printf '%s' "$body" |
      grep -o "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
      head -1 |
      sed 's/.*"\([^"]*\)"$/\1/'
  fi
}

if [ "$(field decision)" != "block" ]; then
  exit 0
fi

reason=$(field reason)
[ -n "$reason" ] || reason="slop-haus: there is still work assigned to you."
echo "$reason" >&2
exit 2
