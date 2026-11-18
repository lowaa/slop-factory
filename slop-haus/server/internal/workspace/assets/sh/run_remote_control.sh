#!/usr/bin/env bash
# Runs this workspace's Remote Control: the host that serves the Claude Code
# sessions the owner drives from the board.
#
#   .slop-haus/run_remote_control.sh [--idle-timeout SECONDS]
#   .slop-haus/run_remote_control.sh --harness pi
#   .slop-haus/run_remote_control.sh --no-update   start on the files as they are
#
# --harness pi serves pi sessions instead. The session runs on this machine, in this
# working copy, as you, with permissions bypassed: run it only in a checkout
# whose contents you trust.
#
# This is a shim, and stays one: it checks the sign-in, brings this workspace's
# slop-haus files and the host's package up to date, and hands over to the
# package's slop-haus-remote-control, which fetches the harness config, loads
# run.env and checks the harness itself. If the update fails the start stops
# and says why: starting on files of unknown age is the thing this prevents.
# --no-update is this launcher's own flag, so it comes first and never reaches
# the host; it starts anyway, with a warning -- for a server outage, or for
# debugging the updater.
#
# Installed into a workspace by setup and replaced by every update -- the
# update that every start runs included, which is why main() holds all of it.
set -uo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)
APP="$root/.slop-haus/app"
# Passed on to update.sh, which would otherwise never see an override: the
# SLOP_HAUS_* variables are cleared before it runs.
UPDATE_LOCK_TIMEOUT="${SLOP_HAUS_UPDATE_LOCK_TIMEOUT:-}"

usage() {
  echo "usage: $(basename "$0") [--no-update] [--harness claude|pi] [host args...]" >&2
  echo "       --no-update starts without updating this workspace first (a server outage; debugging update.sh)" >&2
  echo "       pi runs every tool call without asking -- run it in a container or VM" >&2
}

clear_slop_haus_env() {
  local name
  while IFS= read -r name; do
    unset "$name"
  done < <(compgen -e | grep '^SLOP_HAUS_')
}

# refuse_links <path>... -- fails if any of them is a symlink. Each is
# something about to run with this workspace's credentials in reach, so a
# link there would run whatever it points at instead.
refuse_links() {
  local link
  for link in "$@"; do
    if [ -L "$link" ]; then
      echo "slop-haus: $link is a symlink -- refusing to run through it" >&2
      return 1
    fi
  done
}

# ensure_signed_in -- a harness started with dead credentials looks fine
# until its first slop-haus call fails, so find out now, while a human is
# still here to approve a new sign-in.
#
# This runs before the host loads run.env, so SLOP_HAUS_TOKEN_IMPL set there
# does not apply to this check: it always uses token.sh's default. Reading
# run.env here would mean a second parser beside the host's.
ensure_signed_in() {
  local err answer
  err=$("$root/.slop-haus/token.sh" 2>&1 > /dev/null) && return 0
  echo "$err" >&2
  if [ -t 0 ] && [ -t 2 ]; then
    read -r -p "Sign this workspace in again now? [Y/n] " answer
    case "$answer" in
      [nN]*) return 1 ;;
    esac
    "$root/.slop-haus/token.sh" login
    return
  fi
  echo "slop-haus: run .slop-haus/token.sh login, then start the agent again" >&2
  return 1
}

# run_script <path> <what> <failure hint> -- runs update.sh or install-app.sh,
# which each explain their own failures; this adds only that the start stops.
run_script() {
  local script="$1" what="$2" hint="$3"
  refuse_links "$script" || return 1
  if [ ! -x "$script" ]; then
    echo "slop-haus: $script is missing, so $what -- re-run setup, or start with --no-update" >&2
    return 1
  fi
  if ! env ${UPDATE_LOCK_TIMEOUT:+"SLOP_HAUS_UPDATE_LOCK_TIMEOUT=$UPDATE_LOCK_TIMEOUT"} "$script" >&2; then
    echo "slop-haus: not starting: $hint (see above). Fix that and start again, or start on what is installed with --no-update" >&2
    return 1
  fi
}

main() {
  local update=1 install=1
  while (($#)); do
    case "$1" in
      --no-update)
        update=0
        install=0
        ;;
      # What a launcher from before this shim passes the one it has just
      # written, having updated: skip that, but still install the package,
      # which that launcher did next. Dropped, not passed on: the host does
      # not know it.
      --slop-haus-updated) update=0 ;;
      -h | --help)
        usage
        return 0
        ;;
      *) break ;;
    esac
    shift
  done

  # The harness reads its project config from where it starts.
  cd "$root" || return 1
  # token.sh runs with this workspace's credentials in scope.
  refuse_links "$root/.slop-haus" "$root/.slop-haus/token.sh" || return 1
  clear_slop_haus_env
  ensure_signed_in || return 1
  if ((update)); then
    run_script "$root/.slop-haus/update.sh" "this workspace cannot be updated" \
      "this workspace could not be updated" || return 1
  fi
  if ((install)); then
    run_script "$root/.slop-haus/install-app.sh" "the Remote Control host cannot be installed" \
      "the Remote Control host could not be installed" || return 1
  else
    echo "slop-haus: --no-update: starting on this workspace's current files, which may be out of date" >&2
  fi
  if ! command -v uv > /dev/null 2>&1; then
    echo "slop-haus: uv is required to run the Remote Control host -- install it from https://docs.astral.sh/uv/getting-started/installation/" >&2
    return 127
  fi
  # server-url too: the host sends the token to the address it names.
  refuse_links "$APP" "$root/.slop-haus/server-url" || return 1
  if [ ! -f "$APP/pyproject.toml" ]; then
    echo "slop-haus: $APP is missing -- start again to update this workspace (not with --no-update)" >&2
    return 1
  fi
  # PYTHONPATH and PYTHONHOME are dropped: run.env must not change what the
  # host imports. --python-launch is the host doing the harness config, run.env
  # and harness check itself.
  exec env -u PYTHONPATH -u PYTHONHOME \
    uv run --frozen --no-dev --project "$APP" \
    slop-haus-remote-control --workspace "$root" --python-launch "$@"
}

main "$@"
