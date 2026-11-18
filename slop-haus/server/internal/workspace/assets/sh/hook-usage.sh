#!/usr/bin/env bash
# slop-haus usage hook. Claude Code runs this on Stop and SubagentStop,
# asynchronously (settings.json's "async": true), so it never holds up a
# stop or the Stop hook's decision beside it. It sums the transcript the
# payload names through usage-report.py, beside this script, and posts the
# totals to the server's /hooks/usage.
#
# Reporting is never a reason to fail anything: whatever goes wrong is
# logged on stderr and the exit status is 0. The totals are full ones, so
# a report lost here is put right by the next for the same transcript --
# the main session's Stop fires at the end of every turn.
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

# Drained first, whatever happens next: Claude Code has written it already.
payload=$(mktemp) || exit 0
report=""
trap 'rm -f "$payload" "$report"' EXIT
cat > "$payload"

if [ -z "$server" ]; then
  echo "slop-haus: usage: no server url; not reporting" >&2
  exit 0
fi

script_dir="$(cd -- "$(dirname -- "$0")" &> /dev/null && pwd)"
if ! command -v python3 > /dev/null 2>&1; then
  echo "slop-haus: usage: no python3; not reporting" >&2
  exit 0
fi

# One report at a time in this workspace, read and posted under one lock.
# Async hooks overlap -- a Stop can fire while the last one's report is
# still in flight -- and each report replaces the last for its transcript,
# so two posting out of order would leave the older totals stored. Under
# the lock, whoever reads later posts later, and a transcript only grows.
# flock is util-linux, which token.sh already requires.
lock="$script_dir/usage.lock"
if ! command -v flock > /dev/null 2>&1; then
  echo "slop-haus: usage: no flock; not reporting" >&2
  exit 0
fi
if [ -L "$lock" ]; then
  echo "slop-haus: usage: $lock is a symlink; not reporting" >&2
  exit 0
fi
exec 8> "$lock" || exit 0
# The knob is for tests.
if ! flock -w "${SLOP_HAUS_USAGE_LOCK_WAIT:-60}" 8; then
  echo "slop-haus: usage: another report held the lock too long; not reporting" >&2
  exit 0
fi
report=$(mktemp) || exit 0
# -I: nothing from the environment or the working directory -- the agent's
# repo -- decides which modules a script that reads the transcript loads.
python3 -I "$script_dir/usage-report.py" < "$payload" > "$report" || true
# Nothing to report (its reason is on stderr already).
[ -s "$report" ] || exit 0

token=""
if [ -x "$script_dir/token.sh" ]; then
  # The server this hook talks to, named outright -- see hook-stop.sh.
  token=$(SLOP_HAUS_URL="$server" "$script_dir/token.sh" 2> /dev/null) || token=""
fi
if [ -z "$token" ]; then
  echo "slop-haus: usage: no valid token; not reporting" >&2
  exit 0
fi

# The token goes to curl on stdin, not in its arguments, where any process
# on the machine could read it.
response=$(
  printf 'Authorization: Bearer %s\n' "$token" |
    curl -sS --max-time 10 \
      -X POST "$server/hooks/usage" \
      -H 'Content-Type: application/json' \
      -H @- \
      --data-binary "@$report" \
      -w $'\n%{http_code}' 2>&1
)
curl_exit=$?
# Unquoted assignments: $'\n' is not ANSI-C quoted inside double quotes.
if [ "$curl_exit" -ne 0 ]; then
  reason=${response%%$'\n'*}
  echo "slop-haus: usage: $server unreachable: $reason" >&2
  exit 0
fi
status=${response##*$'\n'}
if [ "$status" != "200" ]; then
  body=${response%$'\n'*}
  echo "slop-haus: usage: HTTP $status: ${body:0:200}" >&2
fi
exit 0
