#!/usr/bin/env bash
# Clones the repos the tenant's workspace manifest lists that are missing
# from this workspace. Run by setup and by update.sh with the
# manifest as arguments; it can also be run by hand:
#
#   .slop-haus/sync-repos.sh -- app github.com/acme/app deploy github.com/acme/deploy
#
# It only ever adds. A path that already exists -- a directory, a file, a
# link, even a dangling one -- is reported and left exactly as it is, however
# wrong its origin: slop-haus's layout report says what does not match, and
# what to do about it is the human's call. Nothing here moves, deletes,
# resets or overwrites anything, and there is no flag that makes it.
#
# Cloning uses this machine's own git credentials, the ones it already
# pushes with: the server never holds any, and none is put in a URL.
#
# Opting out (submodules, `repo`, a devcontainer, a layout kept by hand):
#   SLOP_HAUS_CLONE=no .slop-haus/sync-repos.sh   records the choice
# in .slop-haus/no-clone, which every later setup and update respects.
# Delete that file to opt back in. Validation does not depend on this: the
# layout report is sent whether or not anything is cloned.
#
# SLOP_HAUS_CLONE_PROTOCOL=ssh|https picks how a manifest remote
# (host/owner/repo) becomes a URL. The supervisor sets it, having decided
# the protocol once and logged why. Unset (a script run by hand), this script
# follows how an existing repo of the manifest was cloned, and falls back to
# https.
#
# Exit status is 1 if any repo failed to clone, 2 for bad usage; the rest of
# the repos are still attempted, and callers carry on either way.
#
# Installed into a workspace by setup and replaced by every update; the copy
# there is the same bytes as this one.
set -euo pipefail

OPT_OUT_FILE=".slop-haus/no-clone"

# Everything reaching here may have come from the server, or from whoever
# owns the manifest, so each value is checked against what a path or remote
# of ours can look like and anything else is skipped, not interpreted. The
# server restricts these already; this does not rely on it.

# valid_path: a single plain directory name. No separators or "..",
# nothing a git or a shell could take for an option (a leading "-"), and
# never ".git" in any case, which would aim a clone into the root's own
# repository directory.
valid_path() {
  local p="$1"
  [[ "$p" =~ ^[A-Za-z0-9_][A-Za-z0-9._-]*$ ]] || return 1
  # tr, not ${p,,}: workspaces include macs, whose bash 3.2 has no case
  # conversion.
  [ "$(printf '%s' "$p" | tr '[:upper:]' '[:lower:]')" != ".git" ]
}

# valid_remote: host/owner/repo (more segments for nested groups). The host
# starts with an alphanumeric, which is also what keeps an ssh:// or scp-like
# URL from carrying an ssh option in its host position.
valid_remote() {
  local r="$1" seg host rest
  [[ "$r" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*(/[A-Za-z0-9._-]+)+$ ]] || return 1
  host="${r%%/*}"
  rest="${r#*/}"
  [[ "$host" != *..* ]] || return 1
  local IFS=/
  for seg in $rest; do
    case "$seg" in
      . | .. | .git) return 1 ;;
    esac
  done
}

# clone_url <protocol> <remote>
clone_url() {
  local protocol="$1" remote="$2" host path
  host="${remote%%/*}"
  path="${remote#*/}"
  case "$protocol" in
    ssh) printf 'git@%s:%s.git' "$host" "$path" ;;
    *) printf 'https://%s.git' "$remote" ;;
  esac
}

# origin_url <dir> -- the origin of the repo at <dir>, empty if it has none
# or is not a repo. Only read; never printed, since it is the machine's own
# and may carry a token.
origin_url() {
  git -C "$1" config --get remote.origin.url 2> /dev/null || true
}

# detect_protocol <root> <path>... -- ssh if an existing manifest repo was
# cloned over ssh, otherwise https.
detect_protocol() {
  local root="$1" p url
  shift
  for p in "$@"; do
    url=$(origin_url "$root/$p")
    case "$url" in
      git@* | ssh://*)
        echo ssh
        return 0
        ;;
      https://* | http://*)
        echo https
        return 0
        ;;
    esac
  done
  echo https
}

# clone_into_place <root> <url> <target> -- succeeds only if <target> now is
# the finished clone. The target is checked again just before the rename, and
# is never replaced: if something appeared there meanwhile the clone is
# dropped. (mv would put a directory inside an existing one, hence the check.)
# git removes its own half-done clone when it fails; the scratch directory is
# ours and empty by then, so rmdir (never rm -r) takes it away.
clone_into_place() {
  local root="$1" url="$2" target="$3" scratch
  if [ ! -d "$root/.slop-haus" ] || [ -L "$root/.slop-haus" ]; then
    echo "slop-haus: $root/.slop-haus is not a plain directory -- not cloning" >&2
    return 1
  fi
  scratch=$(mktemp -d "$root/.slop-haus/clone.XXXXXX") || return 1
  if git clone --quiet -- "$url" "$scratch/repo" && [ ! -e "$target" ] && [ ! -L "$target" ]; then
    mv -- "$scratch/repo" "$target"
    rmdir -- "$scratch"
    return 0
  fi
  rmdir -- "$scratch" 2> /dev/null || true
  return 1
}

main() {
  local root
  root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." &> /dev/null && pwd)

  case "${1:-}" in
    --root)
      [ -n "${2:-}" ] || {
        echo "usage: $(basename "$0") [--root DIR] -- PATH REMOTE ..." >&2
        return 2
      }
      root=$(cd -- "$2" &> /dev/null && pwd) || {
        echo "slop-haus: $2 is not a directory" >&2
        return 2
      }
      shift 2
      ;;
  esac
  if [ "${1:-}" = "--" ]; then
    shift
  elif [ "$#" -gt 0 ]; then
    echo "usage: $(basename "$0") [--root DIR] -- PATH REMOTE ..." >&2
    return 2
  fi
  if (($# % 2 != 0)); then
    echo "slop-haus: manifest arguments must be PATH REMOTE pairs" >&2
    return 2
  fi

  local marker="$root/$OPT_OUT_FILE"
  case "$(printf '%s' "${SLOP_HAUS_CLONE:-}" | tr '[:upper:]' '[:lower:]')" in
    no | n | 0 | false | off)
      if [ -d "$root/.slop-haus" ] && [ ! -L "$marker" ]; then
        : >> "$marker"
        echo "slop-haus: cloning is off for this workspace ($OPT_OUT_FILE records it; delete that file to turn it back on)"
      fi
      return 0
      ;;
  esac
  if [ -e "$marker" ] || [ -L "$marker" ]; then
    echo "slop-haus: not cloning missing repos ($OPT_OUT_FILE is present); the layout is still reported and validated"
    return 0
  fi

  if [ "$#" -eq 0 ]; then
    return 0
  fi
  if ! command -v git > /dev/null 2>&1; then
    echo "slop-haus: git is not installed -- not cloning the manifest's repos" >&2
    return 1
  fi

  local -a paths=() remotes=()
  while [ "$#" -gt 0 ]; do
    paths+=("$1")
    remotes+=("$2")
    shift 2
  done

  local protocol="${SLOP_HAUS_CLONE_PROTOCOL:-}"
  case "$protocol" in
    ssh | https) ;;
    "") protocol=$(detect_protocol "$root" "${paths[@]}") ;;
    *)
      echo "slop-haus: SLOP_HAUS_CLONE_PROTOCOL must be ssh or https, not an arbitrary value" >&2
      return 2
      ;;
  esac

  # Unattended runs must fail rather than sit on a password prompt. The
  # user's own GIT_SSH_COMMAND is respected if they set one; BatchMode only
  # stops ssh asking, it does not accept an unknown host key.
  export GIT_TERMINAL_PROMPT=0
  : "${GIT_SSH_COMMAND:=ssh -o BatchMode=yes}"
  export GIT_SSH_COMMAND

  local i path remote target url failed=0
  for i in "${!paths[@]}"; do
    path="${paths[$i]}"
    remote="${remotes[$i]}"
    if ! valid_path "$path"; then
      echo "slop-haus: skipping a manifest path that is not a plain directory name" >&2
      failed=1
      continue
    fi
    if ! valid_remote "$remote"; then
      echo "slop-haus: skipping $path: its remote is not a host/owner/repo name" >&2
      failed=1
      continue
    fi

    target="$root/$path"
    if [ -e "$target" ] || [ -L "$target" ]; then
      echo "slop-haus: $path already exists -- left alone"
      continue
    fi

    url=$(clone_url "$protocol" "$remote")
    echo "slop-haus: cloning $path ($remote)"
    # Cloned beside, then renamed into place: a clone killed halfway (SIGKILL,
    # power loss) leaves a half-repo, and at the real path that would read as
    # "already exists" on every later run and never be retried. In a
    # directory of our own, only the scratch is left behind. `--` ends
    # options, and the URL begins with a scheme or git@ anyway.
    if ! clone_into_place "$root" "$url" "$target"; then
      echo "slop-haus: could not clone $path from $remote -- check this machine's git access to it" >&2
      failed=1
    fi
  done

  return "$failed"
}

# One compound command, which bash reads whole before running any of it: a
# run that rewrites this file in place (an update does) cannot make the
# rest of it come from the new bytes. shfmt keeps a brace group as one; it
# would split `main "$@"; exit $?` onto two lines, which does not hold.
{
  main "$@"
  exit $?
}
