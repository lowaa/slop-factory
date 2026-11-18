#!/usr/bin/env bash
# slop-haus workspace token helper.
#
#   .slop-haus/token.sh login [agent-name]   walk the OAuth device flow (the
#                                            name defaults to .slop-haus.env's)
#   .slop-haus/token.sh                      print a valid access token
#   .slop-haus/token.sh headers [-server=URL] print it as Claude Code MCP
#                                             headersHelper JSON
#                                             (--headers-json is the same, for Codex)
#
# This workspace has no static credential. What it has is an OAuth 2.1
# grant (RFC 8628's device flow) behind .slop-haus/credentials.json: an
# access token, short-lived, and a refresh token that trades for a new pair
# of both. Every hook and script that talks to slop-haus calls this file
# first to get a token, rather than reading one out of the environment.
#
# The refresh token is the one part of this that cannot be careless: the
# server treats a refresh token presented twice as stolen and revokes the
# whole grant (RFC 8628 section 5, reuse detection). Two of this
# workspace's own processes racing to refresh -- two hooks firing together,
# or a hook and update.sh -- would trigger exactly that and lose the
# workspace's only credential. So every refresh happens under an exclusive
# lock, and re-reads the credentials file after taking the lock: whoever
# loses the race finds the winner's new tokens already on disk and never
# calls the server at all.
#
# Bash + curl + python3, the same convention as hook-stop.sh: jq is not
# safe to assume on an arbitrary machine, and python3 nearly always is
# (there is a grep/sed fallback for the rare machine with neither). flock
# is util-linux, Linux-only -- a harder dependency, but the alternative is
# the race above, and losing the credential is worse than requiring it.
set -uo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)
CREDENTIALS="$root/.slop-haus/credentials.json"
LOCKFILE="$root/.slop-haus/credentials.lock"
# Seeded by the server's migration, not registered per workspace -- see
# oauth.WorkspaceClientID.
CLIENT_ID="slop-haus-workspace"
# How long to wait for another of this workspace's own processes to finish
# with the credentials file before giving up. Long enough to cover a slow
# refresh request; short enough that a wedged process doesn't hang every
# hook behind it forever.
LOCK_TIMEOUT=30

server() {
  local s="${SLOP_HAUS_URL:-}"
  if [ -z "$s" ] && [ -f "$root/.slop-haus/server-url" ]; then
    s=$(head -n 1 "$root/.slop-haus/server-url")
  fi
  if [ -z "$s" ]; then
    echo "slop-haus: no server address -- set SLOP_HAUS_URL, or re-run setup" >&2
    return 1
  fi
  printf '%s' "${s%/}"
}

# numeric_or <value> <default> -- <value> if it is a plain non-negative
# integer, otherwise <default>. Every number in this script that comes
# from outside it -- an OAuth response's expires_in/interval, or
# credentials.json's own expires_at -- passes through this before it is
# ever used in a bash arithmetic context. Bash evaluates $(( )) and (( ))
# operands recursively, so an unvalidated string there (a compromised
# response, or a hand-edited credentials file) is a command injection
# vector, not merely a parsing nicety: "$((x))" with x="$(rm -rf ~)" runs
# that command.
numeric_or() {
  if [[ "$1" =~ ^[0-9]+$ ]]; then
    printf '%s' "$1"
  else
    printf '%s' "$2"
  fi
}

# Retrying a refresh that failed in transit (see refresh_locked). The budget
# sits inside the 10 seconds Claude Code allows a headersHelper; the others
# are overridable only so the tests need not wait in real time. Each is
# checked, since they reach arithmetic and sleep.
REFRESH_BUDGET=$(numeric_or "${SLOP_HAUS_REFRESH_BUDGET:-}" 7)
REFRESH_ATTEMPT_MAX=$(numeric_or "${SLOP_HAUS_REFRESH_ATTEMPT_MAX:-}" 4)
REFRESH_ATTEMPTS=$(numeric_or "${SLOP_HAUS_REFRESH_ATTEMPTS:-}" 4)
REFRESH_BACKOFF=0.5
if [[ "${SLOP_HAUS_REFRESH_BACKOFF:-}" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
  REFRESH_BACKOFF="$SLOP_HAUS_REFRESH_BACKOFF"
fi

# field <name> -- reads a flat JSON object from stdin and prints one
# top-level field, or nothing if it is absent or the body doesn't parse.
# Every field this script looks up is a bare token or a number, never one
# containing a quote or a brace, so the fallback regex is unambiguous even
# without a real parser -- the same reasoning as hook-stop.sh's field().
field() {
  local body
  body=$(cat)
  if command -v python3 > /dev/null 2>&1; then
    printf '%s' "$body" | python3 -c '
import json, sys
try:
    doc = json.load(sys.stdin)
except Exception:
    sys.exit(0)
if not isinstance(doc, dict):
    sys.exit(0)
v = doc.get(sys.argv[1])
if v is not None:
    sys.stdout.write(str(v))
' "$1" 2> /dev/null
    return
  fi
  printf '%s' "$body" |
    grep -o "\"$1\"[[:space:]]*:[[:space:]]*\"\{0,1\}[^\",}]*\"\{0,1\}" |
    head -1 |
    sed -E "s/^\"$1\"[[:space:]]*:[[:space:]]*\"?//; s/\"\$//"
}

# urlencode <value> -- percent-encodes a value for a query string. python3
# handles the general case (unicode included); the fallback covers plain
# ASCII, which is all an agent name reasonably is.
urlencode() {
  if command -v python3 > /dev/null 2>&1; then
    python3 -c 'import urllib.parse, sys; sys.stdout.write(urllib.parse.quote(sys.argv[1], safe=""))' "$1"
    return
  fi
  local s="$1" out="" i c
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    case "$c" in
      [a-zA-Z0-9.~_-]) out+="$c" ;;
      *)
        printf -v hex '%%%02X' "'$c"
        out+="$hex"
        ;;
    esac
  done
  printf '%s' "$out"
}

# refuse_symlink <path> -- refuses (with a message on stderr) to read or
# write through path if it is a symlink. credentials.json and its lock
# file are the one place in this workspace with something worth stealing,
# so unlike the templated files in Files (which are only ever written, and
# a rename over a symlink there simply replaces it -- see mv's semantics),
# these are also read and opened, which do follow a symlink to wherever it
# points.
refuse_symlink() {
  if [ -L "$1" ]; then
    echo "slop-haus: $1 is a symlink -- refusing to read or write through it" >&2
    return 1
  fi
  return 0
}

# write_credentials <access> <refresh> <expires_at> -- atomically replaces
# credentials.json: written to a temp file in the same directory (so the
# rename is on one filesystem), chmod 0600 before it has any content
# anyone could read, then renamed over the target. A crash between the
# write and the rename leaves the old, still-valid file in place rather
# than a half-written one or none at all -- which matters here more than
# anywhere else in this workspace, because the refresh token just spent is
# already gone server-side by the time this runs.
#
# access and refresh reach python3 on stdin, not as argv: any other local
# user can read another process's command line via ps or
# /proc/<pid>/cmdline for as long as it runs, and an argument is exactly
# that for the length of this call. expires_at is not a secret, so it stays
# a plain argument. Split on a single newline, which is safe precisely
# because these are the same opaque, alphanumeric tokens the no-python3
# fallback below already trusts not to contain one.
write_credentials() {
  local tmp
  tmp=$(mktemp "$root/.slop-haus/credentials.json.XXXXXX") || return 1
  chmod 600 "$tmp"
  printf '%s\n%s' "$1" "$2" | python3 -c '
import json, sys
access, refresh = sys.stdin.read().split("\n", 1)
json.dump({"access_token": access, "refresh_token": refresh, "expires_at": int(sys.argv[1])}, sys.stdout)
' "$3" > "$tmp" 2> /dev/null || {
    # No python3: build the object by hand. Safe because every value here
    # is one this script itself just received from the token endpoint --
    # an opaque, alphanumeric secret and an integer -- never something a
    # third party chose the shape of.
    printf '{"access_token":"%s","refresh_token":"%s","expires_at":%s}\n' "$1" "$2" "$3" > "$tmp"
  }
  mv -f "$tmp" "$CREDENTIALS"
}

# read_credentials -- sets ACCESS, REFRESH and EXPIRES_AT from
# credentials.json, or clears all three if it is missing or unreadable.
read_credentials() {
  ACCESS="" REFRESH="" EXPIRES_AT=0
  [ -f "$CREDENTIALS" ] || return 0
  refuse_symlink "$CREDENTIALS" || return 0
  local body
  body=$(cat "$CREDENTIALS" 2> /dev/null) || return 0
  ACCESS=$(field access_token <<< "$body")
  REFRESH=$(field refresh_token <<< "$body")
  EXPIRES_AT=$(numeric_or "$(field expires_at <<< "$body")" 0)
}

# acquire_lock -- opens the lock file on fd 9 and takes an exclusive lock,
# waiting up to LOCK_TIMEOUT. Left open (not released) for the rest of this
# process's life, which is always short: get and login both return soon
# after acquiring it.
acquire_lock() {
  if ! command -v flock > /dev/null 2>&1; then
    echo "slop-haus: flock (util-linux) is required to update this workspace's credentials without a race -- install it and try again" >&2
    return 1
  fi
  refuse_symlink "$LOCKFILE" || return 1
  exec 9> "$LOCKFILE"
  if ! flock -w "$LOCK_TIMEOUT" 9; then
    echo "slop-haus: timed out waiting for another slop-haus process to finish with this workspace's credentials" >&2
    return 1
  fi
}

# error_description <body> -- the human-readable half of an OAuth error
# response, falling back to the code alone if the body doesn't carry one.
error_description() {
  local desc code
  desc=$(field error_description <<< "$1")
  if [ -n "$desc" ]; then
    printf '%s' "$desc"
    return
  fi
  code=$(field error <<< "$1")
  printf '%s' "${code:-an unrecognised response}"
}

# refresh_request <refresh-token> <max-seconds> -- one POST to the token
# endpoint. Sets RESP_STATUS ("000" when nothing came back at all) and
# RESP_BODY.
#
# The refresh token goes to curl on stdin (--data-urlencode's "@-" reads
# one field's value from there) rather than as an argument: an argument
# sits in this process's argv, readable by any other local user via ps
# or /proc/<pid>/cmdline for as long as curl runs, and this is the one
# credential this workspace cannot afford to leak that way -- see this
# file's header comment on reuse detection. curl only reads stdin once
# per invocation, so only one field can come from there; client_id and
# grant_type aren't secret and stay ordinary arguments.
refresh_request() {
  local resp
  resp=$(printf '%s' "$1" | curl -sS --max-time "$2" -X POST "$SRV/oauth/token" \
    --data-urlencode "grant_type=refresh_token" \
    --data-urlencode "refresh_token@-" \
    --data-urlencode "client_id=$CLIENT_ID" \
    -w $'\n%{http_code}' 2> /dev/null) || {
    RESP_STATUS=000 RESP_BODY=""
    return 0
  }
  RESP_STATUS="${resp##*$'\n'}"
  RESP_BODY="${resp%$'\n'*}"
}

# refresh_locked <refresh-token> -- spends a refresh token for a new
# access/refresh pair, stores it, and prints the new access token. Must be
# called with the lock already held.
#
# A transit failure (no answer, or a 502/503/504 from whatever is in front of
# the server while it rolls) is retried within REFRESH_BUDGET seconds in
# total. That is safe because the server answers the previous refresh token,
# presented again within a few seconds of its rotation, with the pair it
# already issued: if the first request got through and only its response was
# lost, the retry collects that pair instead of reading as a stolen copy.
# Anything else -- invalid_grant above all, but any 4xx -- is the server's
# answer and is never retried.
#
# The budget is Claude Code's: it gives a headersHelper 10 seconds and then
# drops the MCP connection to needs-auth until a human intervenes. Failing
# at 7 leaves it room to report the error itself.
refresh_locked() {
  local status body access refresh_new ttl expires_at err attempt=0 delay
  local refresh="$1" deadline=$((SECONDS + REFRESH_BUDGET)) left
  SRV=$(server) || return 1
  while :; do
    left=$((deadline - SECONDS))
    ((left < 1)) && left=1
    refresh_request "$refresh" "$((left < REFRESH_ATTEMPT_MAX ? left : REFRESH_ATTEMPT_MAX))"
    status="$RESP_STATUS"
    body="$RESP_BODY"
    case "$status" in
      200) break ;;
      000 | 502 | 503 | 504)
        attempt=$((attempt + 1))
        delay=$(retry_delay "$attempt")
        if ((SECONDS + 1 >= deadline)) || ((attempt >= REFRESH_ATTEMPTS)); then
          echo "slop-haus: could not reach $SRV to refresh this workspace's credentials (gave up after $attempt attempt(s))" >&2
          return 1
        fi
        sleep "$delay"
        # Re-read first: another process may have refreshed in the meantime
        # (nothing else holds the lock, but a login does not wait for it), in
        # which case its tokens are the ones to use.
        read_credentials
        if [ -n "$ACCESS" ] && ((EXPIRES_AT - $(date +%s) > 60)); then
          printf '%s' "$ACCESS"
          return 0
        fi
        [ -n "$REFRESH" ] && refresh="$REFRESH"
        ;;
      *)
        err=$(field error <<< "$body")
        if [ "$err" = "invalid_grant" ]; then
          # Not deleted: the file is evidence of when this happened, and the
          # next successful login overwrites it atomically anyway. Deleting it
          # here would only turn a clear, explained failure into a confusing
          # "no such file" the next time something reads it.
          echo "slop-haus: this workspace's credentials were rejected -- run '.slop-haus/token.sh login' to re-authorize it" >&2
          return 1
        fi
        echo "slop-haus: $SRV refused to refresh this workspace's credentials: $(error_description "$body")" >&2
        return 1
        ;;
    esac
  done
  access=$(field access_token <<< "$body")
  refresh_new=$(field refresh_token <<< "$body")
  if [ -z "$access" ] || [ -z "$refresh_new" ]; then
    echo "slop-haus: $SRV sent an unrecognisable refresh response" >&2
    return 1
  fi
  ttl=$(numeric_or "$(field expires_in <<< "$body")" 900)
  expires_at=$(($(date +%s) + ttl))
  write_credentials "$access" "$refresh_new" "$expires_at" || {
    echo "slop-haus: could not save this workspace's refreshed credentials" >&2
    return 1
  }
  printf '%s' "$access"
}

# retry_delay <attempt> -- seconds to wait before the next attempt: doubling
# from REFRESH_BACKOFF.
retry_delay() {
  python3 -c 'import sys; sys.stdout.write(str(float(sys.argv[1]) * 2 ** (int(sys.argv[2]) - 1)))' "$REFRESH_BACKOFF" "$1" 2> /dev/null ||
    printf '%s' "$REFRESH_BACKOFF"
}

# cmd_get -- prints a currently-valid access token to stdout.
cmd_get() {
  local now
  read_credentials
  now=$(date +%s)
  if [ -n "$ACCESS" ] && ((EXPIRES_AT - now > 60)); then
    printf '%s' "$ACCESS"
    return 0
  fi

  acquire_lock || return 1
  # Re-read: whoever held the lock before us may have just refreshed, in
  # which case there is nothing left to do -- and nothing more to ask the
  # server, which is the whole point of taking the lock before checking.
  read_credentials
  now=$(date +%s)
  if [ -n "$ACCESS" ] && ((EXPIRES_AT - now > 60)); then
    printf '%s' "$ACCESS"
    return 0
  fi
  if [ -z "$REFRESH" ]; then
    echo "slop-haus: this workspace has no stored credentials -- run '.slop-haus/token.sh login'" >&2
    return 1
  fi
  refresh_locked "$REFRESH"
}

# cmd_headers [-server=URL] -- .mcp.json's headersHelper. Claude Code runs
# it on every MCP connection and again after a 401, and an Authorization
# header from it stops Claude Code falling back to its own browser OAuth --
# which is the point: that flow is a second sign-in, per workspace, that a
# headless machine can only finish by copying a localhost URL around, when
# this workspace is already signed in.
#
# -server= because a helper must refresh against the server .mcp.json
# names, not whatever SLOP_HAUS_URL a human left in their shell -- the same
# reason the hooks take it.
cmd_headers() {
  local arg token
  for arg in "$@"; do
    case "$arg" in
      -server=* | --server=*) SLOP_HAUS_URL="${arg#*=}" ;;
      *)
        echo "usage: $(basename "$0") headers [-server=URL]" >&2
        return 2
        ;;
    esac
  done
  token=$(cmd_get) || return 1
  # Spliced into JSON by hand, so refuse anything outside RFC 6750's
  # b64token alphabet rather than trust a tampered credentials.json not to
  # carry a quote or a backslash.
  if [[ ! "$token" =~ ^[A-Za-z0-9._~+/-]+=*$ ]]; then
    echo "slop-haus: this workspace's stored access token is malformed -- run '.slop-haus/token.sh login'" >&2
    return 1
  fi
  printf '{"Authorization":"Bearer %s"}\n' "$token"
}

# recorded_agent_name -- SLOP_HAUS_AGENT from .slop-haus.env, or nothing.
# Parsed rather than sourced: the file sits at the repo root, outside the
# gitignored .slop-haus/, so a pulled commit could carry one, and sourcing
# it would run whatever it holds. Only the single-quoted form setup writes
# (workspace.ShellQuote) is accepted; anything else is ignored.
recorded_agent_name() {
  local line value
  [ -f "$root/.slop-haus.env" ] || return 0
  refuse_symlink "$root/.slop-haus.env" || return 0
  line=$(grep -m 1 '^SLOP_HAUS_AGENT=' "$root/.slop-haus.env" 2> /dev/null) || return 0
  value=${line#SLOP_HAUS_AGENT=}
  [[ "$value" == \'*\' && ${#value} -ge 2 ]] || return 0
  value=${value:1:${#value}-2}
  value=${value//"'\\''"/"'"}
  printf '%s' "$value"
}

# cmd_login [agent-name] -- walks the device authorization flow (RFC 8628)
# and stores what it gets.
cmd_login() {
  local agent_name="${1:-${SLOP_HAUS_AGENT:-}}"
  # Falls back to the name setup recorded, so a bare re-login still
  # preselects this workspace's own agent rather than leaving the human to
  # pick it out of the fleet.
  [ -n "$agent_name" ] || agent_name=$(recorded_agent_name)
  local srv resp status body
  srv=$(server) || return 1

  # No resource parameter: it is optional (RFC 8628 doesn't require it,
  # and the server defaults to its own MCP endpoint when it's absent), and
  # $srv is merely wherever this workspace was told to send requests --
  # which is not always the server's own canonical public address (a
  # direct-to-API address in front of a proxy that hasn't learned to
  # forward the OAuth endpoints yet is exactly such a case). Sending a
  # guess here would only risk invalid_target for no benefit.
  resp=$(curl -sS --max-time 15 -X POST "$srv/oauth/device_authorization" \
    --data-urlencode "client_id=$CLIENT_ID" \
    -w $'\n%{http_code}' 2>&1) || {
    echo "slop-haus: could not reach $srv" >&2
    return 1
  }
  status="${resp##*$'\n'}"
  body="${resp%$'\n'*}"
  if [ "$status" != "200" ]; then
    echo "slop-haus: $srv would not start sign-in: $(error_description "$body")" >&2
    return 1
  fi

  local device_code user_code verify expires_in interval
  device_code=$(field device_code <<< "$body")
  user_code=$(field user_code <<< "$body")
  verify=$(field verification_uri_complete <<< "$body")
  expires_in=$(numeric_or "$(field expires_in <<< "$body")" 600)
  interval=$(numeric_or "$(field interval <<< "$body")" 5)
  if [ -z "$device_code" ] || [ -z "$user_code" ] || [ -z "$verify" ]; then
    echo "slop-haus: $srv sent an unrecognisable device authorization response" >&2
    return 1
  fi

  # &agent=... preselects this agent on the board's device page, so the
  # human approving it doesn't have to pick it out of the fleet by hand.
  if [ -n "$agent_name" ]; then
    verify="${verify}&agent=$(urlencode "$agent_name")"
  fi

  cat << EOF

Open this URL to approve this workspace's access to slop-haus:

  $verify

Or open the device page and enter this code by hand: $user_code

This code expires in $((expires_in / 60)) minute(s).
Waiting for approval...
EOF

  local deadline=$((SECONDS + expires_in)) access refresh ttl expires_at err
  while ((SECONDS < deadline)); do
    sleep "$interval"
    # device_code is bearer-like for the rest of this poll loop -- anyone
    # who has it can complete this sign-in -- so it goes to curl on stdin
    # for the same reason the refresh token does in refresh_locked above.
    resp=$(printf '%s' "$device_code" | curl -sS --max-time 15 -X POST "$srv/oauth/token" \
      --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:device_code" \
      --data-urlencode "device_code@-" \
      --data-urlencode "client_id=$CLIENT_ID" \
      -w $'\n%{http_code}' 2>&1) || {
      echo "slop-haus: could not reach $srv" >&2
      return 1
    }
    status="${resp##*$'\n'}"
    body="${resp%$'\n'*}"
    if [ "$status" = "200" ]; then
      access=$(field access_token <<< "$body")
      refresh=$(field refresh_token <<< "$body")
      if [ -z "$access" ] || [ -z "$refresh" ]; then
        echo "slop-haus: $srv sent an unrecognisable token response" >&2
        return 1
      fi
      ttl=$(numeric_or "$(field expires_in <<< "$body")" 900)
      expires_at=$(($(date +%s) + ttl))
      acquire_lock || return 1
      write_credentials "$access" "$refresh" "$expires_at" || {
        echo "slop-haus: could not save this workspace's credentials" >&2
        return 1
      }
      echo "Authorized. Credentials stored in .slop-haus/credentials.json."
      return 0
    fi
    err=$(field error <<< "$body")
    case "$err" in
      authorization_pending) continue ;;
      slow_down)
        # RFC 8628 section 3.5: back off by five seconds and keep polling
        # at the new, slower rate for the rest of this run.
        interval=$((interval + 5))
        continue
        ;;
      access_denied)
        echo "slop-haus: sign-in was denied" >&2
        return 1
        ;;
      expired_token)
        echo "slop-haus: the code expired before it was approved -- run '.slop-haus/token.sh login' again" >&2
        return 1
        ;;
      *)
        echo "slop-haus: $srv: $(error_description "$body")" >&2
        return 1
        ;;
    esac
  done
  echo "slop-haus: timed out waiting for approval -- run '.slop-haus/token.sh login' again" >&2
  return 1
}

# exec_python_impl "$@" -- hands every command to slop_haus_token.py, the default implementation.
# Returns, having said why on stderr, whenever the bash implementation below should run instead:
# no python3, one older than 3.9 (the module's floor), or a module that is missing or a symlink.
#
# SLOP_HAUS_TOKEN_IMPL=bash is the operator's force-bash switch, set where the host's own
# environment is configured (run.env): a rollback that needs a restart, not a revert. Anything
# else, unset included, is Python.
exec_python_impl() {
  local module="$root/.slop-haus/slop_haus_token.py"
  [ "${SLOP_HAUS_TOKEN_IMPL:-}" != "bash" ] || return 0
  if ! command -v python3 > /dev/null 2>&1; then
    echo "slop-haus: python3 is not installed -- using the bash implementation" >&2
    return 0
  fi
  if ! python3 -I -c 'import sys; sys.exit(sys.version_info < (3, 9))' 2> /dev/null; then
    echo "slop-haus: the Python token implementation needs python3 >= 3.9 -- using the bash implementation" >&2
    return 0
  fi
  if [ -L "$module" ] || [ ! -f "$module" ]; then
    echo "slop-haus: $module is missing or a symlink -- using the bash implementation" >&2
    return 0
  fi
  exec python3 -I "$module" "$@"
}

main() {
  exec_python_impl "$@"
  # Nothing this script ever writes should be readable by anyone else --
  # there is no case where that is merely a hardening measure and not the
  # whole point.
  umask 077
  mkdir -p "$root/.slop-haus"

  case "${1:-}" in
    login)
      shift
      cmd_login "$@"
      ;;
    headers | --headers-json)
      shift
      cmd_headers "$@"
      ;;
    "")
      cmd_get
      ;;
    *)
      echo "usage: $(basename "$0") [login [agent-name] | headers|--headers-json [-server=URL]]" >&2
      return 2
      ;;
  esac
}

# One compound command, which bash reads whole before running any of it: a
# run that rewrites this file in place (an update does) cannot make the
# rest of it come from the new bytes. shfmt keeps a brace group as one; it
# would split `main "$@"; exit $?` onto two lines, which does not hold.
{
  main "$@"
  exit $?
}
