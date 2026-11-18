# Tests for update.sh.
#
# Everything runs against a throwaway workspace ($ROOT) with its own
# .slop-haus/update.sh (a copy, so a test that lets the fetched script
# overwrite it doesn't touch the real one) and server-url. curl is stubbed
# -- see stub_curl below -- so nothing here ever reaches a real server, and
# .slop-haus/token.sh is stubbed too (see stub_token_sh) so a test's token
# does not depend on the real device/refresh flow, which is token.bats's
# job to cover.
#
#   bats/run.sh slop-haus/server/internal/workspace/assets/sh/update.bats

setup() {
  bats_load_library repo_test

  ROOT="${BATS_TEST_TMPDIR}/workspace"
  mkdir -p "${ROOT}/.slop-haus"
  cp "${BATS_TEST_DIRNAME}/update.sh" "${ROOT}/.slop-haus/update.sh"
  UPDATE_WORKER="${ROOT}/.slop-haus/update.sh"

  # Never the workspace root -- so a test that forgets update.sh's
  # "cd $root before running the fetched script" shows up here, not just in
  # the one test that checks it directly.
  CALLER_DIR="${BATS_TEST_TMPDIR}/caller"
  mkdir -p "${CALLER_DIR}"

  SERVER_URL="http://fake-slop-haus.example"
  echo "${SERVER_URL}" > "${ROOT}/.slop-haus/server-url"

  TOKEN="test-agent-token"
  TOKEN_SH_CALLS="${BATS_TEST_TMPDIR}/token-sh.calls"
  : > "${TOKEN_SH_CALLS}"
  stub_token_sh "${TOKEN}"

  CURL_CALLS="${BATS_TEST_TMPDIR}/curl.calls"
  CURL_BODY="${BATS_TEST_TMPDIR}/curl-body"
  CURL_STDIN="${BATS_TEST_TMPDIR}/curl.stdin"
  : > "${CURL_CALLS}"

  # The default "fetched" script: small, and observable by the file it
  # leaves behind rather than by parsing stdout.
  MARKER="${ROOT}/update-ran"
  set_curl_body << SCRIPT
#!/usr/bin/env bash
# slop-haus workspace update
pwd > $(printf %q "${MARKER}")
SCRIPT

  stub_curl

  # A TMPDIR bats can inspect for leftovers once a run finishes.
  TMPDIR="${BATS_TEST_TMPDIR}/tmp"
  mkdir -p "${TMPDIR}"
  export TMPDIR
}

# stub_curl -- a curl replacement for the one call update.sh makes
# (-o <file> -w '%{http_code}' -H ... <url>). stub_recorder can't be used
# here: it has no way to write a response body to the path curl was told to
# use, which every test in this file depends on. Records the full argv to
# $CURL_CALLS, saves what it was handed on stdin to $CURL_STDIN when told to
# read headers from there (-H @-), copies $CURL_BODY to the -o path, and
# prints a status code to stdout the way -w '%{http_code}' would.
#
#   CURL_STATUS=401   overrides the printed status (default 200)
#   CURL_FAIL=1       exits 7 without writing anything, like a dead server
stub_curl() {
  local bin="${BATS_TEST_TMPDIR}/bin"
  mkdir -p "${bin}"
  cat > "${bin}/curl" << STUB
#!/usr/bin/env bash
echo "\$*" >> $(printf %q "${CURL_CALLS}")
if [[ -n "\${CURL_FAIL:-}" ]]; then
  exit 7
fi
prev=""
out=""
for arg in "\$@"; do
  if [[ "\$prev" == "-o" ]]; then
    out="\$arg"
  fi
  if [[ "\$prev" == "-H" && "\$arg" == "@-" ]]; then
    cat > $(printf %q "${CURL_STDIN}")
  fi
  prev="\$arg"
done
if [[ -n "\$out" ]]; then
  cp $(printf %q "${CURL_BODY}") "\$out"
fi
printf '%s' "\${CURL_STATUS:-200}"
STUB
  chmod +x "${bin}/curl"
  [[ ":${PATH}:" == *":${bin}:"* ]] || PATH="${bin}:${PATH}"
}

# set_curl_body -- reads the canned response body from stdin, replacing
# whatever curl would "download" next. The path is fixed (from setup), so
# this can be called again mid-test without recreating the curl stub.
set_curl_body() {
  cat > "${CURL_BODY}"
}

# stub_token_sh <token> -- installs .slop-haus/token.sh as a script that
# records its argv to $TOKEN_SH_CALLS and prints <token> to stdout. This is
# what "this workspace already has a valid, cached token" looks like from
# update.sh's side; token.bats is where the device flow, refresh and
# locking behind a real token.sh are exercised.
stub_token_sh() {
  local token="$1"
  mkdir -p "${ROOT}/.slop-haus"
  cat > "${ROOT}/.slop-haus/token.sh" << STUB
#!/usr/bin/env bash
echo "\$*" >> $(printf %q "${TOKEN_SH_CALLS}")
printf '%s' $(printf %q "${token}")
STUB
  chmod +x "${ROOT}/.slop-haus/token.sh"
}

# stub_failing_token_sh <message> -- a token.sh that cannot produce a
# token: it prints <message> to stderr (the way the real one explains an
# invalid_grant or a missing login) and exits non-zero.
stub_failing_token_sh() {
  local message="$1"
  mkdir -p "${ROOT}/.slop-haus"
  cat > "${ROOT}/.slop-haus/token.sh" << STUB
#!/usr/bin/env bash
echo "\$*" >> $(printf %q "${TOKEN_SH_CALLS}")
echo $(printf %q "${message}") >&2
exit 1
STUB
  chmod +x "${ROOT}/.slop-haus/token.sh"
}

# update_worker [VAR=value...] [-- arg...] -- runs the copied
# update.sh from $CALLER_DIR, never from $ROOT, with SLOP_HAUS_URL and
# SLOP_HAUS_TOKEN cleared first so nothing leaks in from the environment
# running the suite.
update_worker() {
  local -a vars=() args=()
  while (($#)); do
    if [[ "$1" == "--" ]]; then
      shift
      args=("$@")
      break
    fi
    vars+=("$1")
    shift
  done
  run env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN "${vars[@]}" \
    bash -c 'cd "$1" && shift && exec bash "$@"' _ "${CALLER_DIR}" "${UPDATE_WORKER}" "${args[@]}"
}

# assert_curl_requested <partial>... -- checks the recorded curl call(s),
# re-reading the log fresh so an earlier `run`'s $output can't leak in.
assert_curl_requested() {
  local partial
  run cat "${CURL_CALLS}"
  for partial in "$@"; do
    assert_output --partial "${partial}"
  done
}

# assert_token_sent_on_stdin <token> -- the token reached curl as a header
# on stdin and is nowhere in its arguments, where any process on the machine
# could read it.
assert_token_sent_on_stdin() {
  local token="$1"
  run grep -qF -- "${token}" "${CURL_CALLS}"
  assert_failure
  assert_equal "$(cat "${CURL_STDIN}")" "Authorization: Bearer ${token}"
}

# assert_no_request_made -- the log is created empty in setup, so anything in
# it means curl ran.
assert_no_request_made() {
  run cat "${CURL_CALLS}"
  assert_output ""
}

# set_self_overwrite_update_body -- a canned update that rewrites the
# installed update.sh, in place, with a different and much longer
# file: real bytes for a good while, then a dangerous line sitting well past
# the original file's length. This is the case main() exists for: the
# process running update.sh already has the whole original script
# buffered by the time this write happens, so it must finish on the old
# logic rather than read into the new bytes now sitting at the file offset
# it would otherwise carry on from. The overwrite is a truncate-in-place
# (">", not a rename) so it lands on the same inode the running process
# opened -- a rename would sidestep the whole scenario.
set_self_overwrite_update_body() {
  local original_len line_len=66 pad_lines i padded
  original_len=$(wc -c < "${UPDATE_WORKER}")
  pad_lines=$(((original_len + 8192) / line_len + 1))

  padded="${BATS_TEST_TMPDIR}/padded-update.sh"
  {
    for ((i = 0; i < pad_lines; i++)); do
      printf '# padding line %05d, pushed well past the original file length\n' "${i}"
    done
    printf 'echo SHOULD-NOT-RUN > %q\n' "${ROOT}/should-not-run"
    printf 'exit 1\n'
  } > "${padded}"

  set_curl_body << SCRIPT
#!/usr/bin/env bash
# slop-haus workspace update
cat $(printf %q "${padded}") > .slop-haus/update.sh
touch $(printf %q "${ROOT}/self-overwrite-ran")
SCRIPT
}

@test "a successful update runs the fetched script from the workspace root, even from another cwd" {
  update_worker
  assert_success
  assert_equal "$(cat "${MARKER}")" "$(cd "${ROOT}" && pwd -P)"
}

@test "the request carries token.sh's token, on curl's stdin, and hits <server-url>/api/workspace/update_script" {
  # A trailing slash on the configured server url must not become a double
  # slash in the request path.
  echo "${SERVER_URL}/" > "${ROOT}/.slop-haus/server-url"
  update_worker
  assert_success
  assert_curl_requested "${SERVER_URL}/api/workspace/update_script"
  assert_token_sent_on_stdin "${TOKEN}"
  # And it went through token.sh -- exactly once, with no arguments -- not
  # some other path.
  assert_equal "$(cat "${TOKEN_SH_CALLS}")" ""
  assert_equal "$(wc -l < "${TOKEN_SH_CALLS}")" 1
}

@test "SLOP_HAUS_TOKEN and SLOP_HAUS_URL in the environment take precedence over token.sh" {
  # A raw bearer token handed in directly -- the way an OAuth access token
  # obtained some other way could be -- must win outright, and must not
  # even try token.sh: that is what makes this scriptable in a test or a
  # one-off run without walking the whole device flow.
  echo "http://wrong-server.example" > "${ROOT}/.slop-haus/server-url"
  stub_failing_token_sh "token.sh must not be called when SLOP_HAUS_TOKEN is set"
  update_worker SLOP_HAUS_URL="${SERVER_URL}" SLOP_HAUS_TOKEN="${TOKEN}"
  assert_success
  assert_curl_requested "${SERVER_URL}/api/workspace/update_script"
  assert_token_sent_on_stdin "${TOKEN}"
  assert_equal "$(cat "${TOKEN_SH_CALLS}")" ""
}

@test "--dry-run prints the fetched script and does not run it" {
  update_worker -- --dry-run
  assert_success
  assert_output --partial "# slop-haus workspace update"
  assert_output --partial "pwd >"
  refute [ -e "${MARKER}" ]
}

@test "an unknown argument is a usage error and fetches nothing" {
  update_worker -- --bogus
  assert_failure
  assert_equal "$status" 2
  assert_output --partial "usage"
  assert_no_request_made
}

@test "extra arguments after --dry-run are a usage error and fetch nothing" {
  update_worker -- --dry-run unexpected
  assert_failure 2
  assert_output --partial "usage:"
  assert_no_request_made
}

@test "a body whose second line is not the update marker is refused" {
  # e.g. a proxy's HTML error page, served with a 200.
  set_curl_body << 'BODY'
<html>
<body>502 Bad Gateway</body>
</html>
BODY
  update_worker
  assert_failure
  assert_output --partial "did not send a workspace update script"
  refute [ -e "${MARKER}" ]
}

@test "a 401 reports the token problem and runs nothing" {
  CURL_STATUS=401 update_worker
  assert_failure
  assert_output --partial "rejected this agent's token"
  assert_output --partial "disabled"
  refute [ -e "${MARKER}" ]
}

@test "another non-200 status runs nothing" {
  CURL_STATUS=500 update_worker
  assert_failure
  assert_output --partial "answered 500"
  assert_output --partial "server's side"
  refute [ -e "${MARKER}" ]
}

@test "an unreachable server runs nothing and fails" {
  CURL_FAIL=1 update_worker
  assert_failure
  assert_output --partial "could not reach ${SERVER_URL}"
  refute [ -e "${MARKER}" ]
}

@test "a missing curl is named, before anything is fetched or locked" {
  local bin="${BATS_TEST_TMPDIR}/nocurl" tool
  mkdir -p "${bin}"
  for tool in bash env head mktemp rm cat sed dirname; do
    ln -sf "$(command -v "${tool}")" "${bin}/${tool}"
  done
  run env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN PATH="${bin}" "$(command -v bash)" "${UPDATE_WORKER}"
  assert_failure
  assert_output --partial "curl is required"
  refute [ -e "${MARKER}" ]
}

@test "a missing flock is named, before anything is fetched" {
  local bin="${BATS_TEST_TMPDIR}/noflock" tool
  mkdir -p "${bin}"
  for tool in bash env head mktemp rm cat sed dirname; do
    ln -sf "$(command -v "${tool}")" "${bin}/${tool}"
  done
  ln -sf "${BATS_TEST_TMPDIR}/bin/curl" "${bin}/curl"
  run env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN PATH="${bin}" "$(command -v bash)" "${UPDATE_WORKER}"
  assert_failure
  assert_output --partial "flock (util-linux) is required"
  assert_no_request_made
}

@test "fails before any request when token.sh is missing" {
  rm "${ROOT}/.slop-haus/token.sh"
  update_worker
  assert_failure
  assert_output --partial "no agent token"
  assert_output --partial "token.sh is missing"
  assert_no_request_made
}

@test "fails clearly, before any request, when token.sh cannot produce a token" {
  stub_failing_token_sh "slop-haus: this workspace has no stored credentials -- run '.slop-haus/token.sh login'"
  update_worker
  assert_failure
  assert_output --partial "could not get a valid token"
  assert_output --partial "token.sh login"
  assert_no_request_made
}

@test "fails before any request when there is no server address anywhere" {
  rm "${ROOT}/.slop-haus/server-url"
  update_worker
  assert_failure
  assert_output --partial "no server address"
  assert_no_request_made
}

@test "cleans up the downloaded temp file after a successful run" {
  update_worker
  assert_success
  assert_equal "$(ls -A "${TMPDIR}")" ""
}

@test "cleans up the downloaded temp file after a refusal" {
  CURL_STATUS=500 update_worker
  assert_failure
  assert_equal "$(ls -A "${TMPDIR}")" ""
}

# The scenario main() exists for: see set_self_overwrite_update_body.
@test "a fetched script that overwrites update.sh itself still completes cleanly" {
  set_self_overwrite_update_body
  update_worker
  assert_success
  assert [ -e "${ROOT}/self-overwrite-ran" ]
  refute [ -e "${ROOT}/should-not-run" ]
}

# server-url decides where the token is sent. A link would let whatever it
# points at choose the server -- and receive the token -- before any fetched
# script got the chance to refuse anything.
@test "refuses a symlinked server address before any request" {
  echo "http://attacker.example" > "${BATS_TEST_TMPDIR}/elsewhere"
  rm "${ROOT}/.slop-haus/server-url"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere" "${ROOT}/.slop-haus/server-url"
  update_worker
  assert_failure
  assert_output --partial "is a symlink"
  assert_no_request_made
}

# token.sh is about to be executed, with this workspace's credentials in
# scope -- a link there would run whatever it points at instead.
@test "refuses a symlinked token.sh before any request" {
  cat > "${BATS_TEST_TMPDIR}/elsewhere" << 'EOF'
#!/usr/bin/env bash
echo "someone-elses-token"
EOF
  chmod +x "${BATS_TEST_TMPDIR}/elsewhere"
  rm "${ROOT}/.slop-haus/token.sh"
  ln -s "${BATS_TEST_TMPDIR}/elsewhere" "${ROOT}/.slop-haus/token.sh"
  update_worker
  assert_failure
  assert_output --partial "is a symlink"
  assert_no_request_made
}

# -- the update lock --

# start_update <name> -- runs update.sh in the background, the way a
# second terminal would, recording its exit status to $BATS_TEST_TMPDIR/<name>.rc.
# fd 3 is closed because bats waits on it: a background update still
# holding it would hang the whole suite rather than fail one test.
start_update() {
  (
    env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN bash -c 'cd "$1" && shift && exec bash "$@"' \
      _ "${CALLER_DIR}" "${UPDATE_WORKER}" > "${BATS_TEST_TMPDIR}/$1.out" 2>&1
    echo $? > "${BATS_TEST_TMPDIR}/$1.rc"
  ) 3>&- &
  STARTED_PID=$!
}

# wait_for_lock_wait <pid> -- waits until a flock process is running among
# <pid>'s descendants: the update is provably parked on the lock, rather
# than merely given some time to get there. Bounded only so a broken
# update fails instead of hanging.
wait_for_lock_wait() {
  local i
  for ((i = 0; i < 100; i++)); do
    ps -e -o pid=,ppid=,comm= | awk -v root="$1" '
      { parent[$1] = $2; comm[$1] = $3 }
      END {
        for (p in comm) {
          if (comm[p] != "flock") continue
          for (a = parent[p]; a > 1; a = parent[a]) if (a == root) exit 0
        }
        exit 1
      }' && return 0
    sleep 0.1
  done
  return 1
}

# A failed assertion ends a test before it releases what it started, so
# the release happens here too -- or a held update spins until killed.
teardown() {
  [ -z "${RELEASE:-}" ] || touch "${RELEASE}"
}

# wait_for <file> / wait_for_line <text> -- polls for up to ten seconds.
wait_for() {
  local i
  for ((i = 0; i < 100; i++)); do
    [ -e "$1" ] && return 0
    sleep 0.1
  done
  return 1
}

wait_for_line() {
  local i
  for ((i = 0; i < 100; i++)); do
    grep -q "$1" "${CURL_CALLS}" && return 0
    sleep 0.1
  done
  return 1
}

# The rollback the lock exists for: an update that fetched earlier but
# applies later would overwrite the newer files with its older ones. A
# second update has to wait out the first one's fetch *and* apply, and
# only then fetch -- so what it applies is never older than what is there.
@test "a second update waits for the first to finish applying before it fetches" {
  RELEASE="${BATS_TEST_TMPDIR}/release"
  set_curl_body << SCRIPT
#!/usr/bin/env bash
# slop-haus workspace update
echo apply-start >> $(printf %q "${CURL_CALLS}")
while [ ! -e $(printf %q "${RELEASE}") ]; do sleep 0.1; done
echo apply-end >> $(printf %q "${CURL_CALLS}")
SCRIPT

  start_update first
  wait_for_line apply-start

  start_update second
  wait_for_lock_wait "${STARTED_PID}"
  assert_equal "$(grep -c update_script "${CURL_CALLS}")" "1"

  touch "${RELEASE}"
  wait_for "${BATS_TEST_TMPDIR}/first.rc"
  wait_for "${BATS_TEST_TMPDIR}/second.rc"
  assert_equal "$(cat "${BATS_TEST_TMPDIR}/first.rc")" "0"
  assert_equal "$(cat "${BATS_TEST_TMPDIR}/second.rc")" "0"

  run grep -oE 'update_script|apply-start|apply-end' "${CURL_CALLS}"
  assert_output "$(printf '%s\n' update_script apply-start apply-end update_script apply-start apply-end)"
}

@test "an update gives up, clearly and without fetching, if another holds the lock too long" {
  exec 7> "${ROOT}/.slop-haus/update.lock"
  flock 7
  # Capped from outside too: an update that ignored its timeout would
  # otherwise block on the lock this test holds, and hang the suite.
  run timeout 10 env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN SLOP_HAUS_UPDATE_LOCK_TIMEOUT=1 bash "${UPDATE_WORKER}"
  exec 7>&-
  assert_failure
  assert_output --partial "waiting for another update of this workspace"
  assert_output --partial "update.lock"
  assert_no_request_made
}

@test "--dry-run applies nothing, so it does not wait on the update lock" {
  exec 7> "${ROOT}/.slop-haus/update.lock"
  flock 7
  # A dry run that did wait would time out and fail in a second, not two
  # minutes.
  update_worker SLOP_HAUS_UPDATE_LOCK_TIMEOUT=1 -- --dry-run
  exec 7>&-
  assert_success
  assert_output --partial "# slop-haus workspace update"
}

@test "opening the update lock never truncates it" {
  printf 'keep me\n' > "${ROOT}/.slop-haus/update.lock"
  update_worker
  assert_success
  assert_equal "$(cat "${ROOT}/.slop-haus/update.lock")" "keep me"
}

@test "refuses a symlinked update lock before any request" {
  ln -s "${BATS_TEST_TMPDIR}/elsewhere" "${ROOT}/.slop-haus/update.lock"
  update_worker
  assert_failure
  assert_output --partial "is a symlink"
  assert_no_request_made
  assert [ ! -e "${BATS_TEST_TMPDIR}/elsewhere" ]
}
