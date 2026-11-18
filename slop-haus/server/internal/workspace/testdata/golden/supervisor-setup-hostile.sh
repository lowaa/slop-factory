#!/usr/bin/env bash
# slop-haus supervisor setup.
#
# Sets up the directory a slop-haus supervisor runs from on this machine:
# checks that uv is installed, then writes supervisor.toml, secrets.env and the
# harness directories, and unpacks the supervisor into app/. Run it again to
# update; slots and secrets already there are kept.
#
# This file carries no credential: it names the server and the tenant, and the
# supervisor package.
set -euo pipefail

server='http://x/'\''; touch pwned; '\'''
tenant_id='$(touch pwned)'
tenant_name='touch-pwned'
archive_b64=$(cat <<'SLOPHAUS_ARCHIVE'
<<archive entry "pyproject.toml" type='0' mode=0644 bytes="[project]\n">>
<<archive entry "src/pkg/__init__.py" type='0' mode=0644 bytes="V = 1\n">>
SLOPHAUS_ARCHIVE
)

# The body of the supervisor setup script. SupervisorScript puts four
# variables above it: server, tenant_id, tenant_name and archive_b64 (the package as
# base64), so this part reads the same whether it runs from the served script
# or from a test.
#
# The package is carried in the script rather than fetched because the person
# running it has no credential for the package route yet: that route answers
# agents, and the first agent only exists once a slot has signed in.

usage() {
  cat << 'SLOPHAUS_EOF'
usage: setup [--root DIR] [--tenant NAME] [--harness claude|pi] [--variant NAME]

  --root DIR      where the supervisor lives (default: $SLOP_HAUS_HOME, then
                  ~/.config/slop-haus/supervisor.toml, then ~/slop-haus/NAME)
  --tenant NAME   a short name for this tenant on this machine (default: the
                  tenant's name on the server; if that folder already belongs
                  to a different tenant, NAME-2 and so on)
SLOPHAUS_EOF
}

main() {
  local root="" tenant="" harness="claude" variant="" host

  while [ $# -gt 0 ]; do
    case "$1" in
      --root | --tenant | --harness | --variant)
        [ $# -ge 2 ] || {
          echo "slop-haus: $1 needs a value" >&2
          return 2
        }
        case "$1" in
          --root) root=$2 ;;
          --tenant) tenant=$2 ;;
          --harness) harness=$2 ;;
          --variant) variant=$2 ;;
        esac
        shift 2
        ;;
      -h | --help)
        usage
        return 0
        ;;
      *)
        usage >&2
        return 2
        ;;
    esac
  done

  # Before anything is written: the supervisor runs under uv, which also
  # supplies the Python it needs, so there is nothing else to check for.
  if ! command -v uv > /dev/null 2>&1; then
    echo "slop-haus: uv is required to run the supervisor, and was not found on PATH." >&2
    echo "Install it from https://docs.astral.sh/uv/getting-started/installation/ and run this again." >&2
    return 1
  fi

  # Only an explicit --tenant is final: a suggested name is made unique by init
  # against the other roots on this machine.
  local suggested=$tenant_name
  if [ -z "$tenant" ] && [ -z "$suggested" ]; then
    host=${server#*://}
    host=${host%%[/:]*}
    suggested=${host%%.*}
  fi

  work=$(mktemp -d)
  # Quoted when it runs rather than built from $work now: TMPDIR can hold a
  # quote. $work is not local, since the trap fires after main has returned.
  trap 'rm -rf -- "$work"' EXIT

  printf '%s' "$archive_b64" | base64 -d > "$work/package.tar.gz"
  mkdir "$work/bootstrap"
  tar -xzf "$work/package.tar.gz" -C "$work/bootstrap"

  local name_args=(--default-tenant "$suggested")
  if [ -n "$tenant" ]; then
    name_args=(--tenant "$tenant")
  fi
  local args=(init --server "$server" --tenant-id "$tenant_id" "${name_args[@]}"
    --package "$work/package.tar.gz" --harness "$harness" --variant "$variant")
  if [ -n "$root" ]; then
    args+=(--root "$root")
  fi

  # Run from the unpacked copy, because the root (and so <root>/app) is not
  # known until the package has worked it out; init then installs the same
  # package where it belongs.
  PYTHONPATH="$work/bootstrap/src" uv run --no-project --python '>=3.12' \
    python -m slop_haus_supervisor "${args[@]}" > "$work/init.out" || {
    cat "$work/init.out"
    return 1
  }
  cat "$work/init.out"

  # init says which root it set up; `slop-haus` cannot guess it when several tenants share the host.
  local set_up_root
  set_up_root=$(sed -n 's/^set up //p' "$work/init.out" | tail -n 1)

  # The guided part needs a person to answer, and this script is often piped from curl, so it
  # reads the terminal rather than stdin. Without one, it says what to run.
  local cmd=slop-haus
  if ! command -v slop-haus > /dev/null 2>&1 && [ -x "$HOME/.local/bin/slop-haus" ]; then
    cmd=$HOME/.local/bin/slop-haus
  fi
  if [ -n "$set_up_root" ] && [ -r /dev/tty ] && (: < /dev/tty) 2> /dev/null && "$cmd" --help > /dev/null 2>&1; then
    echo
    echo "Next: guided setup (checks this machine, signs in, asks for the harness login token, adds slots)."
    "$cmd" setup --root "$set_up_root" < /dev/tty || echo "slop-haus: setup stopped; run \`slop-haus setup\` again to finish." >&2
  else
    echo
    echo "Next, run the guided setup:  slop-haus setup"
  fi

  cat << 'SLOPHAUS_EOF'

Then run the supervisor with `slop-haus start` (detached, in screen or tmux), and watch
it with `slop-haus status` and `slop-haus logs`; `slop-haus stop` drains it.

The workers push with git over ssh or https, so the account that runs the supervisor must
be able to push without a prompt (an ssh key with no passphrase, an ssh-agent the launcher
provides, or an https credential helper). Setup checks this.

With several tenants on this machine, `slop-haus use NAME` picks the default,
`-t NAME` picks one for a single command, and `slop-haus status` lists them all.

To keep it running unattended under a service manager instead (systemd, launchd or WSL2),
use the recipes in slop-haus/supervisor/service/ in the slop-haus repository (see its README.md).
SLOPHAUS_EOF
}

main "$@"
