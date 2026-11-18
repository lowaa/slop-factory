#!/usr/bin/env bash
# Installs the slop-haus package this workspace's Remote Control host runs
# from, into .slop-haus/app, at the version the server ships now.
#
#   .slop-haus/install-app.sh
#
# run_remote_control.sh runs it on every start of the Remote Control, after the
# workspace update, so a restart is what picks up a new release of the host.
# It asks as this workspace's own agent, by way of token.sh, and installs
# nothing when what it holds is already the server's version.
#
# The package is Python run under uv, which supplies the interpreter: nothing
# here needs a python3 on the machine.
#
# Installed into a workspace by setup and replaced by every update.
set -euo pipefail

# How long a second install waits for the first before giving up; overridable
# so a test of the wait itself is quick, and passed on by run_remote_control.sh as it
# is to update.sh.
LOCK_TIMEOUT="${SLOP_HAUS_UPDATE_LOCK_TIMEOUT:-120}"

# Fixed by the server: the package it ships, and the header its version is in.
PACKAGE_PATH="/api/supervisor/package"
VERSION_HEADER="x-supervisor-version"

# Everything runs from inside main(), which bash has read in full before any
# of it executes: an update rewrites this file while a start is under way.
main() {
  local root app version_file server token work status version

  root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)
  app="$root/.slop-haus/app"
  version_file="$root/.slop-haus/app.version"

  # What this reads decides where the request goes and what runs afterwards,
  # so none of it may be a link.
  local link
  for link in "$root/.slop-haus" "$root/.slop-haus/server-url" "$root/.slop-haus/token.sh" "$app" "$version_file" \
    "$root/.slop-haus/update.lock" "$root/.slop-haus/.app-old"; do
    if [ -L "$link" ]; then
      echo "slop-haus: $link is a symlink -- refusing to use it" >&2
      return 1
    fi
  done

  local tool
  for tool in curl tar flock; do
    if ! command -v "$tool" > /dev/null 2>&1; then
      echo "slop-haus: $tool is required to install the Remote Control host -- install it and try again" >&2
      return 1
    fi
  done

  # The same lock as update.sh's: both rewrite .slop-haus, and a second start
  # should find the first one's install finished rather than race it.
  exec 8>> "$root/.slop-haus/update.lock"
  if ! flock -w "$LOCK_TIMEOUT" 8; then
    echo "slop-haus: timed out after ${LOCK_TIMEOUT}s waiting for another update of this workspace to finish (it holds .slop-haus/update.lock)" >&2
    return 1
  fi

  # A crash between the two moves below leaves no app: put the old one back
  # before anything else, so a start that cannot reach the server still has a
  # host to run.
  if [ ! -e "$app" ] && [ -d "$root/.slop-haus/.app-old" ]; then
    mv -- "$root/.slop-haus/.app-old" "$app"
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

  if [ ! -x "$root/.slop-haus/token.sh" ]; then
    echo "slop-haus: no agent token -- $root/.slop-haus/token.sh is missing; re-run setup" >&2
    return 1
  fi
  if ! token=$(SLOP_HAUS_URL="$server" "$root/.slop-haus/token.sh") || [ -z "$token" ]; then
    echo "slop-haus: could not get a valid token -- run $root/.slop-haus/token.sh login" >&2
    return 1
  fi

  work=$(mktemp -d "$root/.slop-haus/.app-fetch-XXXXXX")
  trap 'rm -rf -- "$work"' RETURN

  # The token goes to curl on stdin, not in its arguments, where any process
  # on the machine could read it.
  if ! status=$(printf 'Authorization: Bearer %s\n' "$token" |
    curl -sS --max-time 60 -o "$work/package.tar.gz" -D "$work/headers" -w '%{http_code}' -H @- \
      "$server$PACKAGE_PATH"); then
    echo "slop-haus: could not reach $server -- is it up, and is this machine on its network?" >&2
    return 1
  fi
  case "$status" in
    200) ;;
    401)
      echo "slop-haus: $server rejected this agent's token -- does it need .slop-haus/token.sh login?" >&2
      return 1
      ;;
    404)
      echo "slop-haus: $server does not ship the Remote Control host (no supervisor package) -- it cannot be started against this server" >&2
      return 1
      ;;
    *)
      echo "slop-haus: $server answered $status for the Remote Control host -- that is a problem on the server's side, not in this workspace" >&2
      return 1
      ;;
  esac

  version=$(tr -d '\r' < "$work/headers" | awk -F': *' -v h="$VERSION_HEADER" 'tolower($1) == h { print $2; exit }')
  if [ -z "$version" ]; then
    echo "slop-haus: $server sent a package with no version -- something between here and slop-haus answered instead" >&2
    return 1
  fi
  # Only a name from a hex digest ends up in a path or a file.
  case "$version" in
    *[!0-9a-f]*)
      echo "slop-haus: $server sent a package with an unusable version" >&2
      return 1
      ;;
  esac

  if [ -f "$app/pyproject.toml" ] && [ -f "$version_file" ] && [ "$(head -n 1 "$version_file")" = "$version" ]; then
    return 0
  fi

  # Files and directories only, and none outside the directory: the archive is the
  # server's, but what is written under .slop-haus runs with this workspace's
  # credentials in reach.
  if tar -tzvf "$work/package.tar.gz" 2> /dev/null | grep -qv '^[-d]'; then
    echo "slop-haus: the Remote Control host package holds something other than files and directories -- refusing to unpack it" >&2
    return 1
  fi
  if tar -tzf "$work/package.tar.gz" 2> /dev/null | grep -qE '(^/|(^|/)\.\.(/|$))'; then
    echo "slop-haus: the Remote Control host package names a path outside itself -- refusing to unpack it" >&2
    return 1
  fi
  mkdir "$work/app"
  if ! tar -xzf "$work/package.tar.gz" -C "$work/app" || [ ! -f "$work/app/pyproject.toml" ]; then
    echo "slop-haus: could not unpack the Remote Control host package" >&2
    return 1
  fi

  # The new tree is whole before the old one moves, so a bad download leaves
  # the version that was there. A host already running has its modules loaded.
  rm -rf -- "$root/.slop-haus/.app-old"
  [ ! -e "$app" ] || mv -- "$app" "$root/.slop-haus/.app-old"
  mv -- "$work/app" "$app"
  printf '%s\n' "$version" > "$version_file"
  rm -rf -- "$root/.slop-haus/.app-old"
  # The single-file host this replaced, which updates no longer write.
  rm -f -- "$root/.slop-haus/conductor-host.py"
}

# One compound command, which bash reads whole before running any of it: a
# run that rewrites this file in place (an update does) cannot make the
# rest of it come from the new bytes. shfmt keeps a brace group as one; it
# would split `main "$@"; exit $?` onto two lines, which does not hold.
{
  main "$@"
  exit $?
}
