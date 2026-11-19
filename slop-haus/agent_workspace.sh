#!/usr/bin/env bash
# Creates a throwaway agent workspace pointed at the local dev stack, so
# running a real agent against slop-haus is repeatable rather than a
# sequence of remembered curl commands.
#
# A workspace is a directory with a git repo to work in, a .claude/
# settings.json wiring up the MCP server and both hooks, and this
# workspace's own OAuth credentials. That is exactly what a real workspace
# on the cluster looks like -- the only difference is the server URL. It is
# built the same way too: a signed-in user asks for the setup script, and
# it is run in the repo -- which in turn signs the workspace in over OAuth's
# device flow. `up` plays the human's part of that flow itself, using the
# same signed-in session, so it works end to end with nothing to click.
#
# Usage:
#   ./agent_workspace.sh up [name]      create (and re-create) a workspace
#   ./agent_workspace.sh seed [n]       file n sample tickets
#   ./agent_workspace.sh down [name]    remove it and disable its agent
#   ./agent_workspace.sh list           show workspaces and the fleet
#
# Then, to actually run an agent in it:
#   add .workspaces/<name> as a supervisor slot (slop-haus slot add), or
#   .workspaces/<name>/.slop-haus/run_remote_control.sh
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)
cd "$SCRIPT_DIR"

SERVER="${SLOP_HAUS_URL:-http://localhost:8190}"
# Where the board is served -- the api's PUBLIC_BASE_URL, the dev stack's
# frontend. The api refuses a browser-style write from any other origin, so
# this script, writing with a signed-in session, names it as the board's
# own page would.
BOARD_ORIGIN="${SLOP_HAUS_BOARD_URL:-http://localhost:${FRONTEND_PORT:-5273}}"
# The default account docker-compose-dev.yaml's seed service creates. This
# only ever talks to a local dev stack.
USER_EMAIL="${DEV_USER_EMAIL:-dev@slop-haus.local}"
USER_PASSWORD="${DEV_USER_PASSWORD:-slop-haus-dev-password}"
# Outside the repo, not under it. A workspace is its own git repo, and
# nesting one inside another confuses which project root Claude Code reads
# .claude/settings.json and .mcp.json from -- so the workspace's own config
# is quietly ignored in favour of the enclosing repository's.
WORKSPACES="${SLOP_HAUS_WORKSPACES:-$HOME/.slop-haus-workspaces}"

require_server() {
  if ! curl -sf -o /dev/null "$SERVER/healthz"; then
    echo "no slop-haus at $SERVER -- run ./dev_env.sh up -d first" >&2
    exit 1
  fi
}

# The session lives in a cookie jar for this run only: it is a credential,
# and nothing here needs it once the script exits. CSRF_TOKEN is the
# session's CSRF token, which the sign-in answers with.
JAR=""
CSRF_TOKEN=""
sign_in() {
  [[ -n "$JAR" ]] && return
  JAR=$(mktemp)
  trap 'rm -f "$JAR"' EXIT
  local profile
  if ! profile=$(curl -sf -c "$JAR" -X POST "$SERVER/api/auth/login" \
    -H "Origin: $BOARD_ORIGIN" \
    -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,sys; print(json.dumps({"email": sys.argv[1], "password": sys.argv[2]}))' "$USER_EMAIL" "$USER_PASSWORD")"); then
    echo "could not sign in to $SERVER as $USER_EMAIL -- set DEV_USER_EMAIL/DEV_USER_PASSWORD" \
      "(and SLOP_HAUS_BOARD_URL, if the board is not at $BOARD_ORIGIN)" >&2
    exit 1
  fi
  CSRF_TOKEN=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["csrf_token"])' <<< "$profile")
}

# api is curl as the signed-in user, sending what the server requires of a
# write the session cookie authenticates: the board's Origin and the
# session's CSRF token (see authn.BrowserGuard).
api() {
  curl -sf -b "$JAR" -H "Origin: $BOARD_ORIGIN" -H "X-CSRF-Token: $CSRF_TOKEN" "$@"
}

cmd_up() {
  local name="${1:-builder-1}"
  local dir="$WORKSPACES/$name"
  require_server

  # Re-creating a workspace is the common case while iterating, so tear
  # down first rather than failing on a half-built one.
  cmd_down "$name" > /dev/null 2>&1 || true
  mkdir -p "$dir"

  # A real repo to work in: without one, an agent asked to change code has
  # nothing to change, and the run tells you nothing about the tools.
  git -C "$dir" init -q
  cat > "$dir/README.md" << 'REPO'
# sample repo

A throwaway checkout for exercising slop-haus. Agents are given tickets
scoped to paths in here.
REPO
  mkdir -p "$dir/src"
  cat > "$dir/src/uploader.py" << 'REPO'
def upload(client, blob):
    """Upload a blob. Gives up on the first failure."""
    return client.put(blob)
REPO
  git -C "$dir" add -A
  git -C "$dir" -c user.email=slop@haus -c user.name=slop commit -qm "initial"

  sign_in
  # The script carries no credential any more -- an agent is authorized
  # through OAuth's device flow, which it walks on its own by running
  # .slop-haus/token.sh login. That call blocks polling until approved, so
  # it runs in the background here, and this function plays the human's
  # part of the flow: watch for the code it prints, then approve it with
  # the same signed-in session used to fetch the script.
  local script
  script=$(api -X POST "$SERVER/api/agents/setup_script" \
    -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,sys; print(json.dumps({"name": sys.argv[1]}))' "$name")")

  local setup_log
  setup_log=$(mktemp)
  (cd "$dir" && bash -s <<< "$script") > "$setup_log" 2>&1 &
  local setup_pid=$!

  approve_device_login "$name" "$setup_log" "$setup_pid"

  local setup_status=0
  wait "$setup_pid" || setup_status=$?
  cat "$setup_log"
  rm -f "$setup_log"
  if ((setup_status != 0)); then
    echo "setup script failed" >&2
    exit 1
  fi

  echo
  if [[ -f "$dir/.slop-haus/credentials.json" ]]; then
    echo "Signed in: $dir/.slop-haus/credentials.json"
  else
    echo "warning: this workspace did not finish signing in -- run" \
      "'.slop-haus/token.sh login' in $dir" >&2
  fi

  echo
  echo "Run an agent in it: add it to a supervisor as a slot, or serve your own"
  echo "sessions from it with:"
  echo "  $dir/.slop-haus/run_remote_control.sh"
}

# approve_device_login watches setup_log (the running setup script's
# combined output) for the user code token.sh login prints, then approves
# it as agent $name -- the one step of onboarding a real workspace needs a
# human's browser for, done here instead with the signed-in cookie jar. If
# the code never appears (the background process already exited, or
# nothing showed up within a minute), it gives up and lets the caller
# report a workspace that isn't signed in yet.
approve_device_login() {
  local name="$1" setup_log="$2" setup_pid="$3"
  local user_code="" waited=0

  while ((waited < 60)); do
    # grep exits 1 when it finds nothing, which is the expected case on
    # every iteration but the last -- under pipefail (set at the top of
    # this file) that would otherwise abort the whole script the first
    # time the code hasn't shown up yet, so it is explicitly not a failure
    # here.
    user_code=$(grep -oE '\b[A-Z]{4}-[A-Z]{4}\b' "$setup_log" 2> /dev/null | head -1) || true
    [[ -n "$user_code" ]] && break
    kill -0 "$setup_pid" 2> /dev/null || break
    sleep 1
    waited=$((waited + 1))
  done

  if [[ -z "$user_code" ]]; then
    echo "warning: never saw a device code from token.sh login" >&2
    return 0
  fi

  local agent_id
  agent_id=$(api "$SERVER/api/agents" 2> /dev/null |
    python3 -c "import json,sys; print(next((a['id'] for a in json.load(sys.stdin)['agents'] if a['name']==sys.argv[1]), ''))" "$name" 2> /dev/null) || true
  if [[ -z "$agent_id" ]]; then
    echo "warning: could not find agent $name to approve its device code $user_code" >&2
    return 0
  fi

  # lookup first, the way the board's device page does before showing the
  # approve button -- a code that already expired or was never issued
  # fails clearly here rather than as a confusing decision error.
  api -o /dev/null -X POST "$SERVER/api/oauth/device/lookup" \
    -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,sys; print(json.dumps({"user_code": sys.argv[1]}))' "$user_code")" || {
    echo "warning: device code $user_code was not found (expired?) -- could not approve it" >&2
    return 0
  }
  api -o /dev/null -X POST "$SERVER/api/oauth/device/decision" \
    -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,sys; print(json.dumps({"user_code": sys.argv[1], "approve": True, "agent_id": sys.argv[2]}))' "$user_code" "$agent_id")"
  echo "approved device code $user_code for agent $name"
}

cmd_seed() {
  local count="${1:-2}"
  require_server
  sign_in

  # Scoped to paths that exist in the sample repo, so path-scope conflicts
  # are real rather than theoretical.
  local titles=(
    "Add a retry to the uploader|src/uploader.py gives up after one failure. Add a bounded retry with backoff.|src/uploader.py"
    "Document the upload behaviour|README.md does not describe what upload() does on failure.|README.md"
    "Add a test for the uploader|There is no test covering the failure path.|src/"
  )
  for ((i = 0; i < count && i < ${#titles[@]}; i++)); do
    IFS='|' read -r title body path <<< "${titles[$i]}"
    api -o /dev/null -X POST "$SERVER/api/tickets" \
      -H 'Content-Type: application/json' \
      -d "$(printf '{"title":%s,"body":%s,"paths":[{"path":%s}]}' \
        "$(printf '%s' "$title" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')" \
        "$(printf '%s' "$body" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')" \
        "$(printf '%s' "$path" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')")"
    echo "filed: $title"
  done
}

cmd_down() {
  local name="${1:-builder-1}"
  local dir="$WORKSPACES/$name"

  # Disable rather than delete: an agent's leases and its authorship on
  # past comments should survive the workspace being thrown away.
  if curl -sf -o /dev/null "$SERVER/healthz" 2> /dev/null; then
    sign_in
    local id
    id=$(api "$SERVER/api/agents" 2> /dev/null |
      python3 -c "import json,sys; print(next((a['id'] for a in json.load(sys.stdin)['agents'] if a['name']==sys.argv[1]), ''))" "$name" 2> /dev/null || true)
    if [[ -n "$id" ]]; then
      api -o /dev/null -X PATCH "$SERVER/api/agents/$id" \
        -H 'Content-Type: application/json' \
        -d '{"disabled": true}' || true
      echo "disabled agent $name"
    fi
  fi

  rm -rf "$dir"
  echo "removed $dir"
}

cmd_list() {
  echo "workspaces:"
  ls -1 "$WORKSPACES" 2> /dev/null | sed 's/^/  /' || echo "  (none)"
  echo "fleet:"
  if ! curl -sf -o /dev/null "$SERVER/healthz" 2> /dev/null; then
    echo "  (server unreachable)"
    return
  fi
  sign_in
  api "$SERVER/api/agents" 2> /dev/null |
    python3 -c "import json,sys; [print(f\"  {a['name']:<24} {'disabled' if a['disabled_at'] else 'enabled':<9} {'holding #'+str(a['lease']['ticket_id']) if a.get('lease') else 'idle'}\") for a in json.load(sys.stdin)['agents']]" \
      2> /dev/null || echo "  (server unreachable)"
}

case "${1:-}" in
  up)
    shift
    cmd_up "$@"
    ;;
  seed)
    shift
    cmd_seed "$@"
    ;;
  down)
    shift
    cmd_down "$@"
    ;;
  list)
    shift
    cmd_list "$@"
    ;;
  *)
    sed -n '2,20p' "$0" | sed -E 's/^# ?//'
    exit 1
    ;;
esac
