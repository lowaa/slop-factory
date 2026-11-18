#!/usr/bin/env bash
# Brings this slop-haus workspace up to date with what the server hands out
# now: hooks, settings, the briefing, the token helper,
# the launchers, and this script. Run it from anywhere; it works on
# the workspace it lives in.
#
# run_remote_control.sh runs this on every start, so nobody
# has to remember to. Run it by hand only to look (--dry-run) or to update
# without starting anything.
#
#   .slop-haus/update.sh             fetch the update and apply it
#   .slop-haus/update.sh --dry-run   print the update instead
#
# The workspace's OAuth credentials (.slop-haus/credentials.json) are the
# request's own authentication, by way of .slop-haus/token.sh, and this
# never rewrites them: they are not among the files an update touches at
# all, so there is nothing here to preserve on purpose -- see workspace.go's
# Files doc comment. A harness already running keeps the files it started
# with: Claude Code only notices a brand-new .claude/agents directory when
# it starts, and pi loads its extension and briefing only when it starts.
#
# Installed into a workspace by setup and replaced by every update; the copy
# there is the same bytes as this one.
set -euo pipefail

# How long a second update waits for the first before giving up. Long
# enough to cover the first one's fetch (30s at most) and applying it.
# Overridable so a test of the wait itself doesn't take two minutes.
UPDATE_LOCK_TIMEOUT="${SLOP_HAUS_UPDATE_LOCK_TIMEOUT:-120}"

# lock_updates <root> -- takes this workspace's update lock on fd 8 and
# holds it until this process exits, i.e. across the fetch *and* applying
# what came back. Two updates at once would otherwise each replace the
# managed files one at a time: whichever fetched the older script and
# applied it last rolls the workspace back, or leaves it half of each.
# Taken before the fetch, so the one that waits then fetches the newest.
#
# Not credentials.lock: a token refresh inside this run takes that one,
# and it has to stay free for the hooks while an update is applied.
lock_updates() {
  local lockfile="$1/.slop-haus/update.lock"
  if ! command -v flock > /dev/null 2>&1; then
    echo "slop-haus: flock (util-linux) is required so two updates cannot overwrite each other -- install it and try again" >&2
    return 1
  fi
  if [ -L "$lockfile" ]; then
    echo "slop-haus: $lockfile is a symlink -- refusing to open it" >&2
    return 1
  fi
  # Append, not truncate: the -L check above and this open are two steps,
  # so a link swapped in between them could still be followed -- but
  # opened this way it cannot clobber what it points at.
  exec 8>> "$lockfile"
  if ! flock -w "$UPDATE_LOCK_TIMEOUT" 8; then
    echo "slop-haus: timed out after ${UPDATE_LOCK_TIMEOUT}s waiting for another update of this workspace to finish (it holds .slop-haus/update.lock)" >&2
    return 1
  fi
}

# Everything runs from inside main(), which bash has read in full before any
# of it executes. The update rewrites this very file, and bash reads a
# script as it goes: without this, it would carry on from the same byte
# offset in the new version.
main() {
  local root dry_run=0 server token script status

  root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)

  case "${1:-}" in
    "") [ $# -eq 0 ] || {
      echo "usage: $(basename "$0") [--dry-run]" >&2
      return 2
    } ;;
    --dry-run)
      dry_run=1
      [ $# -eq 1 ] || {
        echo "usage: $(basename "$0") [--dry-run]" >&2
        return 2
      }
      ;;
    *)
      echo "usage: $(basename "$0") [--dry-run]" >&2
      return 2
      ;;
  esac

  # What this reads decides where the request goes, so none of it may be a
  # link: a server-url pointing elsewhere would hand this workspace's
  # access token to whoever it points at, before any script arrived to
  # check anything. token.sh is about to be executed, not merely read, so a
  # link there would run whatever it points at with this workspace's
  # credentials in scope.
  local link
  for link in "$root/.slop-haus" "$root/.slop-haus/server-url" "$root/.slop-haus/token.sh"; do
    if [ -L "$link" ]; then
      echo "slop-haus: $link is a symlink -- refusing to read through it" >&2
      return 1
    fi
  done

  if ! command -v curl > /dev/null 2>&1; then
    echo "slop-haus: curl is required to fetch the update -- install it and try again" >&2
    return 1
  fi

  if ((! dry_run)); then
    lock_updates "$root" || return 1
  fi

  server="${SLOP_HAUS_URL:-}"
  if [ -z "$server" ] && [ -f "$root/.slop-haus/server-url" ]; then
    server=$(head -n 1 "$root/.slop-haus/server-url")
  fi
  if [ -z "$server" ]; then
    echo "slop-haus: no server address -- set SLOP_HAUS_URL, or re-run setup" >&2
    return 1
  fi
  server="${server%/}"

  # SLOP_HAUS_TOKEN, if set, wins outright: it is a way to hand this run a
  # bearer token directly, which is what makes this scriptable against a
  # token gotten some other way. Otherwise this workspace's own
  # credentials, by way of token.sh -- refreshed under its own lock if
  # they need it.
  token="${SLOP_HAUS_TOKEN:-}"
  if [ -z "$token" ]; then
    if [ ! -x "$root/.slop-haus/token.sh" ]; then
      echo "slop-haus: no agent token -- $root/.slop-haus/token.sh is missing; re-run setup" >&2
      return 1
    fi
    # $server is already resolved above (env override, else server-url);
    # passed explicitly so token.sh is guaranteed to agree with this
    # script about which server the token is for, rather than resolving it
    # again independently.
    if ! token=$(SLOP_HAUS_URL="$server" "$root/.slop-haus/token.sh"); then
      echo "slop-haus: could not get a valid token -- run $root/.slop-haus/token.sh login" >&2
      return 1
    fi
  fi
  if [ -z "$token" ]; then
    echo "slop-haus: no agent token -- run $root/.slop-haus/token.sh login" >&2
    return 1
  fi

  script=$(mktemp)
  trap 'rm -f "$script"' RETURN

  # The token goes to curl on stdin, not in its arguments, where any
  # process on the machine could read it.
  if ! status=$(printf 'Authorization: Bearer %s\n' "$token" |
    curl -sS --max-time 30 -o "$script" -w '%{http_code}' -H @- \
      "$server/api/workspace/update_script"); then
    echo "slop-haus: could not reach $server (from $root/.slop-haus/server-url) -- is it up, and is this machine on its network?" >&2
    return 1
  fi
  case "$status" in
    200) ;;
    401)
      echo "slop-haus: $server rejected this agent's token -- is the agent disabled, or does it need .slop-haus/token.sh login?" >&2
      return 1
      ;;
    *)
      echo "slop-haus: $server answered $status for the update -- that is a problem on the server's side, not in this workspace" >&2
      return 1
      ;;
  esac

  # A proxy's error page can arrive with a 200. Only run what is recognisably
  # an update script.
  if [ "$(sed -n 2p "$script")" != "# slop-haus workspace update" ]; then
    echo "slop-haus: $server did not send a workspace update script -- something between here and slop-haus answered instead; that is not a problem with this workspace" >&2
    return 1
  fi

  if ((dry_run)); then
    cat "$script"
    return 0
  fi

  (cd "$root" && bash "$script")
}

# One compound command, which bash reads whole before running any of it: a
# run that rewrites this file in place (an update does) cannot make the
# rest of it come from the new bytes. shfmt keeps a brace group as one; it
# would split `main "$@"; exit $?` onto two lines, which does not hold.
{
  main "$@"
  exit $?
}
