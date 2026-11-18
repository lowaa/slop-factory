bats_require_minimum_version 1.5.0

# Tests for token.sh: the OAuth device-flow / access-token helper every
# hook and script in a workspace calls to get a bearer token, since there
# is no static one to read out of the environment any more.
#
# curl is stubbed (see stub_curl) so nothing here ever reaches a real
# server, and sleep is stubbed (see stub_sleep) so a poll loop's backoff is
# observable without a test actually waiting for it. Both stubs are driven
# by files under $STATE_DIR, which the set_* helpers below write.
#
# python3 is also stubbed (see stub_python3), but only to record its argv
# before handing off to the real interpreter -- token.sh's own JSON
# handling has to keep working, just observably, so the "no secret on
# argv" tests below have something to check.
#
# Run against both implementations: token_python.bats is a symlink to this file, and a run under
# that name leaves SLOP_HAUS_TOKEN_IMPL unset, so token.sh's shim hands every command to
# slop_haus_token.py by default, login included; a run under token.bats sets it to bash. The module cannot be given a curl
# or sleep stub, so the python3 stub swaps in token_harness.py, which runs the same module with
# a transport and sleep driven by the same $STATE_DIR files.
#
#   bats/run.sh slop-haus/server/internal/workspace/assets/sh/token.bats

setup() {
  bats_load_library repo_test

  ROOT="${BATS_TEST_TMPDIR}/workspace"
  mkdir -p "${ROOT}/.slop-haus"
  cp "${BATS_TEST_DIRNAME}/token.sh" "${ROOT}/.slop-haus/token.sh"
  chmod +x "${ROOT}/.slop-haus/token.sh"
  cp "${BATS_TEST_DIRNAME}/../py/slop_haus_token.py" "${ROOT}/.slop-haus/slop_haus_token.py"
  MODULE="${ROOT}/.slop-haus/slop_haus_token.py"
  HARNESS="${BATS_TEST_DIRNAME}/token_harness.py"
  if [[ "${BATS_TEST_FILENAME}" == *_python.bats ]]; then
    TOKEN_IMPL=python
    unset SLOP_HAUS_TOKEN_IMPL
  else
    TOKEN_IMPL=bash
    export SLOP_HAUS_TOKEN_IMPL=bash
  fi
  export MODULE HARNESS
  TOKEN_SH="${ROOT}/.slop-haus/token.sh"
  CREDENTIALS="${ROOT}/.slop-haus/credentials.json"

  SERVER_URL="http://fake-slop-haus.example"
  echo "${SERVER_URL}" > "${ROOT}/.slop-haus/server-url"

  STATE_DIR="${BATS_TEST_TMPDIR}/state"
  mkdir -p "${STATE_DIR}"
  CURL_LOG="${STATE_DIR}/curl.log"
  # The request body curl would actually have sent, reconstructed field by
  # field (see stub_curl): a --data-urlencode value read from stdin is
  # decoded there and re-encoded the same way curl itself would, so a test
  # can confirm a secret both stayed off argv *and* still reached the
  # request correctly.
  CURL_BODY_LOG="${STATE_DIR}/curl_body.log"
  SLEEP_LOG="${STATE_DIR}/sleep.log"
  PYTHON_LOG="${STATE_DIR}/python.log"
  : > "${CURL_LOG}"
  : > "${CURL_BODY_LOG}"
  : > "${SLEEP_LOG}"
  : > "${PYTHON_LOG}"
  # Captured before stub_sleep shadows the real one on PATH: the curl stub
  # uses this to make a refresh request block for real, on request, which
  # is what the concurrency test needs to synchronize on.
  REAL_SLEEP=$(command -v sleep)
  REAL_PYTHON3=$(command -v python3)
  export CREDENTIALS STATE_DIR CURL_LOG CURL_BODY_LOG SLEEP_LOG PYTHON_LOG REAL_SLEEP REAL_PYTHON3

  # A device authorization that never expires mid-test and polls fast by
  # default; individual tests override the poll/refresh responses.
  set_device_auth '{"device_code":"dev-1","user_code":"ABCD-EFGH","verification_uri":"http://fake-slop-haus.example/device","verification_uri_complete":"http://fake-slop-haus.example/device?code=ABCD-EFGH","expires_in":600,"interval":1}'
  set_poll_default '{"error":"authorization_pending"}' 400

  stub_curl
  stub_sleep
  stub_python3
}

# A failed test must not leave a background holder or caller behind: bats waits for them.
teardown() {
  local pid
  for pid in $(jobs -p); do kill "${pid}" 2> /dev/null || true; done
}

# -- stubs and state helpers --

set_device_auth() {
  printf '%s' "$1" > "${STATE_DIR}/device_auth.body"
  printf '%s' "${2:-200}" > "${STATE_DIR}/device_auth.status"
}
set_poll_default() {
  printf '%s' "$1" > "${STATE_DIR}/poll.body"
  printf '%s' "${2:-200}" > "${STATE_DIR}/poll.status"
}
set_poll_at() {
  printf '%s' "$2" > "${STATE_DIR}/poll.$1.body"
  printf '%s' "${3:-200}" > "${STATE_DIR}/poll.$1.status"
}
set_refresh() {
  printf '%s' "$1" > "${STATE_DIR}/refresh.body"
  printf '%s' "${2:-200}" > "${STATE_DIR}/refresh.status"
}

set_refresh_at() {
  printf '%s' "$2" > "${STATE_DIR}/refresh.$1.body"
  printf '%s' "${3:-200}" > "${STATE_DIR}/refresh.$1.status"
}
refresh_calls() { wc -l < "${STATE_DIR}/refresh.calls" | tr -d ' '; }

seed_credentials() {
  printf '{"access_token":"%s","refresh_token":"%s","expires_at":%s}' "$1" "$2" "$3" > "${CREDENTIALS}"
  chmod 600 "${CREDENTIALS}"
}

# stub_curl -- records every call to $CURL_LOG and answers device
# authorization, device-code polling and refresh requests from the
# STATE_DIR files above, in the shape token.sh itself expects: the body,
# then a status line, exactly as `-w $'\n%{http_code}'` produces.
stub_curl() {
  local bin="${BATS_TEST_TMPDIR}/bin"
  mkdir -p "${bin}"
  cat > "${bin}/curl" << 'STUB'
#!/usr/bin/env bash
echo "$*" >> "${CURL_LOG}"

# Walk argv by index, rather than `for arg in "$@"`, so a --data-urlencode
# flag and the value right after it can be handled as a pair: a value
# ending in "@-" is how token.sh now hands curl a secret on stdin instead
# of on its own argv, and reconstructing the request body it stands for is
# what proves that round-trip still works, not just that the secret is off
# argv.
url="" grant=""
args=("$@")
i=0
data_parts=()
while ((i < ${#args[@]})); do
  arg="${args[$i]}"
  case "$arg" in
    http://*|https://*) url="$arg" ;;
    --data-urlencode)
      i=$((i + 1))
      val="${args[$i]}"
      case "$val" in
        grant_type=refresh_token) grant="refresh" ;;
        grant_type=urn:ietf:params:oauth:grant-type:device_code) grant="device" ;;
      esac
      if [[ "$val" == *"@-" ]]; then
        name="${val%@-}"
        secret=$(cat)
        encoded=$(printf '%s' "$secret" | "${REAL_PYTHON3}" -c 'import urllib.parse, sys; sys.stdout.write(urllib.parse.quote(sys.stdin.read(), safe=""))')
        data_parts+=("${name}=${encoded}")
      else
        data_parts+=("$val")
      fi
      ;;
  esac
  i=$((i + 1))
done
if ((${#data_parts[@]})); then
  (IFS='&'; echo "${data_parts[*]}") >> "${CURL_BODY_LOG}"
fi

body="" status=""
case "$url" in
  */oauth/device_authorization)
    body=$(cat "${STATE_DIR}/device_auth.body")
    status=$(cat "${STATE_DIR}/device_auth.status")
    ;;
  */oauth/token)
    if [[ "$grant" == "refresh" ]]; then
      echo refresh >> "${STATE_DIR}/refresh.calls"
      # A concurrency test needs to know, from outside, the exact moment a
      # refresh request is in flight -- which by construction is also the
      # moment the caller holds the credentials lock (refresh_locked is
      # only ever reached after acquire_lock succeeds). Delaying the
      # response here, only when a test asks for it, is what makes that
      # moment observable without guessing at real timing.
      if [[ -f "${STATE_DIR}/refresh.delay" ]]; then
        "${REAL_SLEEP}" "$(cat "${STATE_DIR}/refresh.delay")"
      fi
      n=$(wc -l < "${STATE_DIR}/refresh.calls")
      # refresh.N.* scripts the Nth refresh request; a status of "fail" is
      # curl itself failing (exit 28, nothing printed), which is what a
      # refused connection or a timeout looks like to token.sh.
      if [[ -f "${STATE_DIR}/refresh.$n.status" ]]; then
        body=$(cat "${STATE_DIR}/refresh.$n.body")
        status=$(cat "${STATE_DIR}/refresh.$n.status")
        # Another process finishing a refresh while this request was in flight.
        if [[ -f "${STATE_DIR}/refresh.$n.credentials" ]]; then
          cp "${STATE_DIR}/refresh.$n.credentials" "${CREDENTIALS}"
        fi
        if [[ "$status" == "fail" ]]; then
          exit 28
        fi
      else
        body=$(cat "${STATE_DIR}/refresh.body")
        status=$(cat "${STATE_DIR}/refresh.status")
      fi
    else
      n=$(( $(cat "${STATE_DIR}/poll.count" 2> /dev/null || echo 0) + 1 ))
      echo "$n" > "${STATE_DIR}/poll.count"
      if [[ -f "${STATE_DIR}/poll.$n.body" ]]; then
        body=$(cat "${STATE_DIR}/poll.$n.body")
        status=$(cat "${STATE_DIR}/poll.$n.status")
      else
        body=$(cat "${STATE_DIR}/poll.body")
        status=$(cat "${STATE_DIR}/poll.status")
      fi
    fi
    ;;
  *)
    body='{"error":"not_found"}'
    status=404
    ;;
esac
printf '%s\n%s' "$body" "$status"
STUB
  chmod +x "${bin}/curl"
  [[ ":${PATH}:" == *":${bin}:"* ]] || PATH="${bin}:${PATH}"
}

# stub_sleep -- records the requested duration and returns immediately, so
# a poll loop's backoff is provable without a test actually waiting.
stub_sleep() {
  local bin="${BATS_TEST_TMPDIR}/bin"
  mkdir -p "${bin}"
  cat > "${bin}/sleep" << 'STUB'
#!/usr/bin/env bash
echo "$*" >> "${SLEEP_LOG}"
STUB
  chmod +x "${bin}/sleep"
  [[ ":${PATH}:" == *":${bin}:"* ]] || PATH="${bin}:${PATH}"
}

# stub_python3 -- records every python3 invocation's argv to $PYTHON_LOG,
# then hands off to the real interpreter (found before this shadows it on
# PATH) so token.sh's JSON handling keeps working exactly as it does
# outside the test. This is what makes "no secret on python3's argv"
# checkable at all: without a stand-in on PATH, the real python3 runs
# invisibly and there is nothing to assert against.
stub_python3() {
  local bin="${BATS_TEST_TMPDIR}/bin"
  mkdir -p "${bin}"
  cat > "${bin}/python3" << 'STUB'
#!/usr/bin/env bash
echo "$*" >> "${PYTHON_LOG}"
# The shim's own call to the module becomes the harness, which runs that module with the stubs.
if [[ "${1:-}" == "-I" && "${2:-}" == "${MODULE}" ]]; then
  shift 2
  exec "${REAL_PYTHON3}" -I "${HARNESS}" "${MODULE}" "$@"
fi
exec "${REAL_PYTHON3}" "$@"
STUB
  chmod +x "${bin}/python3"
  [[ ":${PATH}:" == *":${bin}:"* ]] || PATH="${bin}:${PATH}"
}

# run_token_sh [VAR=value...] [-- arg...] -- runs the copied token.sh with
# SLOP_HAUS_URL/SLOP_HAUS_AGENT cleared first, so nothing leaks in from the
# environment running the suite.
run_token_sh() {
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
  run env -u SLOP_HAUS_URL -u SLOP_HAUS_AGENT "${vars[@]}" "${TOKEN_SH}" "${args[@]}"
}

credentials_field() {
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2]))' "${CREDENTIALS}" "$1"
}

# -- login: device flow --

@test "login: happy path stores credentials, prints the code and URL, and exits 0" {
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","token_type":"Bearer","expires_in":900}'

  run_token_sh -- login test-agent
  assert_success
  assert_output --partial "Open this URL to approve"
  assert_output --partial "ABCD-EFGH"
  assert_output --partial "agent=test-agent"
  assert_output --partial "Authorized."

  assert_equal "$(credentials_field access_token)" "AT1"
  assert_equal "$(credentials_field refresh_token)" "RT1"
  assert_equal "$(stat -c '%a' "${CREDENTIALS}")" "600"
}

@test "login: with no name given, preselects the agent setup recorded in .slop-haus.env" {
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","token_type":"Bearer","expires_in":900}'
  printf "SLOP_HAUS_URL='%s'\nSLOP_HAUS_AGENT='recorded agent'\n" "${SERVER_URL}" > "${ROOT}/.slop-haus.env"

  run_token_sh -- login
  assert_success
  assert_output --partial "agent=recorded%20agent"
}

@test "login: .slop-haus.env is read, never executed" {
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","token_type":"Bearer","expires_in":900}'
  printf 'touch "%s/pwned"\nSLOP_HAUS_AGENT=$(touch "%s/pwned")\n' "${STATE_DIR}" "${STATE_DIR}" > "${ROOT}/.slop-haus.env"

  run_token_sh -- login
  assert_success
  assert [ ! -e "${STATE_DIR}/pwned" ]
  refute_output --partial "agent="
}

@test "login: an apostrophe in the recorded name survives setup's quoting" {
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","token_type":"Bearer","expires_in":900}'
  # Exactly what workspace.ShellQuote writes for o'brien.
  cat > "${ROOT}/.slop-haus.env" << 'ENV'
SLOP_HAUS_AGENT='o'\''brien'
ENV

  run_token_sh -- login
  assert_success
  assert_output --partial "agent=o%27brien"
}

@test "login: a name given on the command line wins over .slop-haus.env's" {
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","token_type":"Bearer","expires_in":900}'
  printf "SLOP_HAUS_AGENT='recorded-agent'\n" > "${ROOT}/.slop-haus.env"

  run_token_sh -- login given-agent
  assert_success
  assert_output --partial "agent=given-agent"
  refute_output --partial "recorded-agent"
}

@test "login: authorization_pending is retried until approved" {
  set_poll_at 1 '{"error":"authorization_pending"}' 400
  set_poll_at 2 '{"error":"authorization_pending"}' 400
  set_poll_at 3 '{"access_token":"AT1","refresh_token":"RT1","expires_in":900}'

  run_token_sh -- login
  assert_success
  assert_equal "$(credentials_field access_token)" "AT1"
  assert_equal "$(cat "${STATE_DIR}/poll.count")" "3"
}

@test "login: slow_down backs off by five seconds and keeps polling" {
  set_poll_at 1 '{"error":"slow_down"}' 400
  set_poll_at 2 '{"access_token":"AT1","refresh_token":"RT1","expires_in":900}'

  run_token_sh -- login
  assert_success
  assert_equal "$(credentials_field access_token)" "AT1"
  # One sleep at the original interval (1s) before the first poll, then a
  # second sleep at interval+5 (6s) before the retry -- proof the backoff
  # actually widened, not just that the login eventually succeeded.
  assert_equal "$(cat "${STATE_DIR}/sleep.log")" "$(printf '1\n6')"
}

@test "login: access_denied fails clearly and stores nothing" {
  set_poll_at 1 '{"error":"access_denied"}' 400

  run_token_sh -- login
  assert_failure
  assert_output --partial "denied"
  assert [ ! -e "${CREDENTIALS}" ]
}

@test "login: expired_token fails clearly and tells the human to try again" {
  set_poll_at 1 '{"error":"expired_token"}' 400

  run_token_sh -- login
  assert_failure
  assert_output --partial "expired"
  assert_output --partial "token.sh login"
  assert [ ! -e "${CREDENTIALS}" ]
}

# -- get: cached token, refresh, and failure --

@test "get: a cached, not-yet-expiring token is returned without any request" {
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"

  run_token_sh
  assert_success
  assert_equal "$output" "AT-cached"
  assert_equal "$(cat "${CURL_LOG}")" ""
}

@test "get: a token expiring soon is refreshed, and the new credentials are written atomically with 0600" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","token_type":"Bearer","expires_in":900}'

  run_token_sh
  assert_success
  assert_equal "$output" "AT-new"
  assert_equal "$(credentials_field access_token)" "AT-new"
  assert_equal "$(credentials_field refresh_token)" "RT-new"
  assert_equal "$(stat -c '%a' "${CREDENTIALS}")" "600"
  # No leftover temp files from the atomic write.
  assert_equal "$(find "${ROOT}/.slop-haus" -name 'credentials.json.*')" ""
}

@test "get: invalid_grant on refresh fails non-zero, explains re-authorizing, and does not touch the stored credentials" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"error":"invalid_grant","error_description":"the code or token is invalid, expired, revoked or already used"}' 400

  run_token_sh
  assert_failure
  assert_output --partial "token.sh login"
  assert_output --partial "re-authorize"
  # Not deleted, not blanked -- still exactly what was there before.
  assert_equal "$(credentials_field access_token)" "AT-old"
  assert_equal "$(credentials_field refresh_token)" "RT-old"
}

# -- get: a refresh that fails in transit is retried --
#
# Safe only because the server answers the previous refresh token, presented
# again within seconds of its rotation, with the pair it already issued (see
# the oauth package's replay.go). Claude Code gives a headersHelper 10
# seconds, so the retries have to finish well inside that.

OK_REFRESH='{"access_token":"AT-new","refresh_token":"RT-new","token_type":"Bearer","expires_in":900}'

@test "get: a refresh that fails in transit twice then succeeds is retried with the same token and backoff" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh_at 1 '' fail
  set_refresh_at 2 '<html>bad gateway</html>' 502
  set_refresh_at 3 "$OK_REFRESH"

  run_token_sh
  assert_success
  assert_equal "$output" "AT-new"
  assert_equal "$(refresh_calls)" "3"
  assert_equal "$(credentials_field refresh_token)" "RT-new"
  # The refresh token was spent from the one place the server knows it as,
  # three times over: a retry must never send anything else.
  assert_equal "$(grep -c 'refresh_token=RT-old' "${CURL_BODY_LOG}")" "3"
  assert_equal "$(cat "${SLEEP_LOG}" | tr '\n' ' ')" "0.5 1.0 "
}

@test "get: 503 and 504 are retried like 502" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh_at 1 '' 503
  set_refresh_at 2 '' 504
  set_refresh_at 3 "$OK_REFRESH"

  run_token_sh
  assert_success
  assert_equal "$(refresh_calls)" "3"
}

@test "get: it gives up after the attempt bound and leaves the stored credentials alone" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '' 502

  run_token_sh
  assert_failure
  assert_output --partial "could not reach"
  assert_equal "$(refresh_calls)" "4"
  assert_equal "$(credentials_field refresh_token)" "RT-old"
}

@test "get: each attempt is capped well inside Claude Code's 10 second headersHelper allowance" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '' 502

  # The cap is min(REFRESH_ATTEMPT_MAX, budget left) and the budget runs on
  # bash's real SECONDS, so a slow runner can eat into the default. A budget
  # far larger than any plausible runtime leaves the attempt cap as the only limit.
  run_token_sh SLOP_HAUS_REFRESH_BUDGET=1000
  assert_failure
  assert_equal "$(grep -c -- '--max-time 4 ' "${CURL_LOG}")" "4"
}

# The overall budget is what keeps four capped attempts plus backoff under the
# allowance. The stubs never let time pass, so the test spends it by setting the
# budget to nothing: one attempt, at the shortest cap, and no retry.
@test "get: a spent budget stops the retries and shortens the attempt" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '' 502

  run_token_sh SLOP_HAUS_REFRESH_BUDGET=0
  assert_failure
  assert_equal "$(refresh_calls)" "1"
  assert_equal "$(grep -c -- '--max-time 1 ' "${CURL_LOG}")" "1"
}

@test "get: the attempt bound can be set, and a malformed override falls back to the default" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '' 502

  run_token_sh SLOP_HAUS_REFRESH_ATTEMPTS=2
  assert_failure
  assert_equal "$(refresh_calls)" "2"

  : > "${STATE_DIR}/refresh.calls"
  run_token_sh 'SLOP_HAUS_REFRESH_ATTEMPTS=$(touch /tmp/pwned)' SLOP_HAUS_REFRESH_BACKOFF=x
  assert_failure
  assert_equal "$(refresh_calls)" "4"
}

@test "get: invalid_grant is never retried" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"error":"invalid_grant"}' 400

  run_token_sh
  assert_failure
  assert_output --partial "re-authorize"
  assert_equal "$(refresh_calls)" "1"
}

@test "get: no other 4xx is retried either" {
  for code in 400 401 403 429; do
    : > "${STATE_DIR}/refresh.calls"
    seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
    set_refresh '{"error":"invalid_request"}' "$code"

    run_token_sh
    assert_failure
    assert_equal "$(refresh_calls)" "1"
  done
}

@test "get: a retry re-reads credentials.json, and uses what another process refreshed meanwhile" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  printf '{"access_token":"AT-other","refresh_token":"RT-other","expires_at":%s}' "$(($(date +%s) + 900))" > "${STATE_DIR}/refresh.1.credentials"
  set_refresh_at 1 '' fail

  run_token_sh
  assert_success
  assert_equal "$output" "AT-other"
  assert_equal "$(refresh_calls)" "1" "nothing left to ask the server"
}

@test "get: a retry sends the refresh token another process stored, if it has rotated but not yet expired" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  printf '{"access_token":"AT-other","refresh_token":"RT-other","expires_at":%s}' "$(($(date +%s) + 30))" > "${STATE_DIR}/refresh.1.credentials"
  set_refresh_at 1 '' fail
  set_refresh_at 2 "$OK_REFRESH"

  run_token_sh
  assert_success
  assert_equal "$(grep -c 'refresh_token=RT-other' "${CURL_BODY_LOG}")" "1"
}

# expires_at, expires_in and interval all end up inside a bash arithmetic
# context ($(( )) or (( )). Measured against bash 5.2: a value containing
# "$(cmd)" there is a syntax error, not an executed command -- arithmetic
# expansion evaluates the substituted text as a single arithmetic
# expression rather than re-running shell expansions over it -- so this is
# not the arbitrary-command-execution vector it can be mistaken for.
# Falling back to a sane default for anything non-numeric is still the
# right call, and it is what these test: without it, a hand-edited
# credentials file or a malformed/misbehaving server response degrades to
# a raw "syntax error: operand expected" on stderr and an arithmetic
# command that quietly evaluates false, rather than the clean, predictable
# behaviour a default gives -- fragile enough (a different shell, a future
# refactor that adds an eval or an array index) that leaving it to bash's
# incidental safety here would be the wrong thing to rely on.
@test "get: a non-numeric expires_at in a tampered credentials.json falls back to a default instead of a raw shell error" {
  local canary="${BATS_TEST_TMPDIR}/canary"
  printf '{"access_token":"AT-old","refresh_token":"RT-old","expires_at":"$(touch %s)"}' "${canary}" > "${CREDENTIALS}"
  chmod 600 "${CREDENTIALS}"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'

  run_token_sh
  assert_success
  assert_equal "$output" "AT-new"
  refute_output --partial "syntax error"
  refute [ -e "${canary}" ]
}

@test "get: a non-numeric expires_in in a refresh response falls back to a default instead of a raw shell error" {
  local canary="${BATS_TEST_TMPDIR}/canary"
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh "{\"access_token\":\"AT-new\",\"refresh_token\":\"RT-new\",\"expires_in\":\"\$(touch ${canary})\"}"

  run_token_sh
  assert_success
  assert_equal "$output" "AT-new"
  refute_output --partial "syntax error"
  refute [ -e "${canary}" ]
  assert_equal "$(credentials_field access_token)" "AT-new"
}

@test "login: a non-numeric expires_in/interval in the device authorization response falls back to a default instead of a raw shell error" {
  local canary="${BATS_TEST_TMPDIR}/canary"
  set_device_auth "{\"device_code\":\"dev-1\",\"user_code\":\"ABCD-EFGH\",\"verification_uri\":\"http://fake-slop-haus.example/device\",\"verification_uri_complete\":\"http://fake-slop-haus.example/device?code=ABCD-EFGH\",\"expires_in\":\"\$(touch ${canary})\",\"interval\":\"\$(touch ${canary})\"}"
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","expires_in":900}'

  run_token_sh -- login
  assert_success
  assert_equal "$(credentials_field access_token)" "AT1"
  refute_output --partial "syntax error"
  refute [ -e "${canary}" ]
}

@test "get: no stored credentials at all fails clearly, telling the human to log in" {
  run_token_sh
  # The exact status, not just "non-zero": a typo on the "return 1" this
  # path relies on (e.g. a stray "ret 1") would also exit non-zero -- from
  # bash's own "command not found" for the misspelled builtin -- which
  # assert_failure alone would not catch, and the echo just above it would
  # still have printed the expected message either way.
  assert_failure
  assert_equal "$status" 1
  assert_output --partial "token.sh login"
  refute_output --partial "command not found"
  assert_equal "$(cat "${CURL_LOG}")" ""
}

# -- the lock: this is the load-bearing test --

# A refresh token is single-use; presenting it twice revokes the whole
# grant (RFC 8628 reuse detection). Two of this workspace's own processes
# racing to refresh -- two hooks firing together, or a hook and
# update.sh -- must never both reach the token endpoint. This is
# what the lock and the re-read-after-lock exist for; without them this
# test fails by making two refresh requests instead of one, and would fail
# for real by losing the workspace's only refresh token.
@test "get: two concurrent calls near expiry make exactly one refresh request" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'
  # The first caller's refresh request blocks for a second once it is in
  # flight -- which, by construction (refresh_locked only runs after
  # acquire_lock succeeds), is also exactly when it holds the credentials
  # lock. This is what makes the second caller's overlap deterministic
  # rather than a hope about scheduling: without it, a caller that simply
  # finishes first (with no lock at all) could make this pass for the
  # wrong reason by leaving nothing left for the second caller to race.
  echo 1 > "${STATE_DIR}/refresh.delay"

  local out1="${BATS_TEST_TMPDIR}/out1" out2="${BATS_TEST_TMPDIR}/out2"
  env -u SLOP_HAUS_URL -u SLOP_HAUS_AGENT "${TOKEN_SH}" > "${out1}" 2>&1 &
  local pid1=$!

  # Wait until the first caller's refresh request is actually in flight
  # (proof it holds the lock) before starting the second -- the interleaving
  # the whole test exists to prove, made certain rather than merely likely.
  local waited=0
  while [[ ! -s "${STATE_DIR}/refresh.calls" ]]; do
    # The real sleep, not the stubbed one on PATH -- this loop needs an
    # actual wait between checks, or it just spins through its retry
    # budget before the backgrounded process gets anywhere near curl.
    "${REAL_SLEEP}" 0.1
    waited=$((waited + 1))
    if ((waited > 50)); then
      fail "the first caller never reached its refresh request"
    fi
  done

  env -u SLOP_HAUS_URL -u SLOP_HAUS_AGENT "${TOKEN_SH}" > "${out2}" 2>&1 &
  local pid2=$!

  wait "${pid1}"
  local status1=$?
  wait "${pid2}"
  local status2=$?

  assert_equal "${status1}" 0
  assert_equal "${status2}" 0
  assert_equal "$(cat "${out1}")" "AT-new"
  assert_equal "$(cat "${out2}")" "AT-new"
  assert_equal "$(grep -c refresh "${STATE_DIR}/refresh.calls" 2> /dev/null || echo 0)" "1"
}

# -- no token ever appears on stderr --

@test "no access or refresh token ever appears on stderr, on login or on refresh" {
  set_poll_at 1 '{"access_token":"AT-secret","refresh_token":"RT-secret","expires_in":900}'
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_AGENT "${TOKEN_SH}" login
  assert_success
  refute_output --partial "AT-secret"
  assert [ "$(printf '%s' "${stderr}" | grep -c 'AT-secret\|RT-secret' || true)" = "0" ]

  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-refreshed","refresh_token":"RT-refreshed","expires_in":900}'
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_AGENT "${TOKEN_SH}"
  assert_success
  assert [ "$(printf '%s' "${stderr}" | grep -c 'AT-refreshed\|RT-refreshed' || true)" = "0" ]
}

# -- secrets stay off argv (ps, /proc/<pid>/cmdline) --
#
# stderr and stdout aren't the only place a secret can leak: for as long as
# a process runs, any other local user can read its command line via ps or
# /proc/<pid>/cmdline, which an argument -- unlike stdin or an environment
# variable owned by this user -- always sits on. The tests above already
# cover stdout/stderr; these cover curl's and python3's argv specifically,
# for every call site that used to pass a secret that way.

@test "get: the refresh token reaches curl on stdin, never on its argv, and still lands correctly in the request" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'

  run_token_sh
  assert_success

  run cat "${CURL_LOG}"
  refute_output --partial "RT-old"

  run cat "${CURL_BODY_LOG}"
  assert_output --partial "refresh_token=RT-old"
}

@test "get: a refresh token needing percent-encoding is still sent correctly once it comes off stdin" {
  local secret='RT old+value/slash'
  seed_credentials "AT-old" "${secret}" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'

  run_token_sh
  assert_success

  run cat "${CURL_LOG}"
  refute_output --partial "${secret}"

  run cat "${CURL_BODY_LOG}"
  assert_output --partial "refresh_token=RT%20old%2Bvalue%2Fslash"
}

@test "get: the access and refresh tokens written to disk never reach python3's argv" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'

  run_token_sh
  assert_success

  run cat "${PYTHON_LOG}"
  refute_output --partial "AT-new"
  refute_output --partial "RT-new"
  if [[ "${TOKEN_IMPL}" == bash ]]; then
    # write_credentials still ran through python3 -- otherwise this test
    # would pass for the wrong reason (nothing on argv because nothing ran).
    assert_output --partial "expires_at"
  else
    # The only python3 run is the module itself, named by path.
    assert_output --partial "-I ${MODULE}"
  fi
}

@test "login: the device code reaches curl on stdin, never on its argv, and still lands correctly in the request" {
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","expires_in":900}'

  run_token_sh -- login
  assert_success

  run cat "${CURL_LOG}"
  refute_output --partial "dev-1"

  run cat "${CURL_BODY_LOG}"
  assert_output --partial "device_code=dev-1"
}

@test "login: the access and refresh tokens written to disk never reach python3's argv" {
  set_poll_at 1 '{"access_token":"AT-secret","refresh_token":"RT-secret","expires_in":900}'

  run_token_sh -- login
  assert_success

  run cat "${PYTHON_LOG}"
  refute_output --partial "AT-secret"
  refute_output --partial "RT-secret"
}

@test "flock is required: a missing flock refuses to refresh rather than racing" {
  if [[ "${TOKEN_IMPL}" == python ]]; then
    skip "the Python body takes the lock with fcntl.flock and does not need flock(1)"
  fi
  # A minimal PATH with everything token.sh needs except flock, so
  # `command -v flock` genuinely finds nothing.
  local bin="${BATS_TEST_TMPDIR}/no-flock-bin"
  mkdir -p "${bin}"
  local tool
  for tool in bash curl python3 date mktemp cat mv chmod mkdir sed grep head dirname basename; do
    local real
    real=$(command -v "${tool}") || continue
    ln -sf "${real}" "${bin}/${tool}"
  done
  # Keep the stubbed sleep too, in case login is exercised elsewhere.
  ln -sf "$(command -v sleep)" "${bin}/sleep" 2> /dev/null || true

  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'
  local before
  before=$(cat "${CREDENTIALS}")

  run env -u SLOP_HAUS_URL -u SLOP_HAUS_AGENT PATH="${bin}" "${TOKEN_SH}"
  assert_failure
  assert_output --partial "flock"
  assert_equal "$(cat "${CREDENTIALS}")" "${before}"
}

# -- symlinked credentials/lock files --

# credentials.json is read with `cat`, which follows a symlink to whatever
# it points at -- another workspace's real credentials, or anything else on
# the machine. Treating it as unusable rather than reading through it is
# what keeps a planted link from leaking someone else's refresh token into
# a refresh request.
@test "get: a symlinked credentials.json is refused, not read through" {
  local victim="${BATS_TEST_TMPDIR}/victim-credentials.json"
  printf '{"access_token":"someone-elses","refresh_token":"someone-elses-refresh","expires_at":%s}' \
    "$(($(date +%s) + 3600))" > "${victim}"
  ln -s "${victim}" "${CREDENTIALS}"

  run_token_sh
  assert_failure
  assert_output --partial "is a symlink"
  assert_equal "$(cat "${CURL_LOG}")" ""
}

# credentials.lock is opened for writing (`exec 9>`), which follows a
# symlink and truncates whatever it points at -- refused before that ever
# happens.
@test "get: a symlinked credentials.lock is refused before it is opened" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'
  local victim="${BATS_TEST_TMPDIR}/victim-lock"
  printf 'not a lock file' > "${victim}"
  ln -s "${victim}" "${ROOT}/.slop-haus/credentials.lock"

  run_token_sh
  assert_failure
  assert_output --partial "is a symlink"
  assert_equal "$(cat "${victim}")" "not a lock file"
}

# -- headers: .mcp.json's headersHelper --

@test "headers: prints the cached token as a JSON Authorization header" {
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"

  run_token_sh -- headers
  assert_success
  assert_equal "$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["Authorization"])' "$output")" "Bearer AT-cached"
}

@test "headers: --headers-json is the same command, for Codex's http_headers_helper" {
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"

  run_token_sh -- --headers-json
  assert_success
  assert_equal "$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["Authorization"])' "$output")" "Bearer AT-cached"
}

@test "headers: -server= wins over a SLOP_HAUS_URL left in the shell when it refreshes" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","token_type":"Bearer","expires_in":900}'

  run_token_sh SLOP_HAUS_URL=http://some-other-slop-haus.example -- headers -server=http://named-in-mcp-json.example
  assert_success
  assert_output --partial "Bearer AT-new"
  run cat "${CURL_LOG}"
  assert_output --partial "http://named-in-mcp-json.example/oauth/token"
  refute_output --partial "some-other-slop-haus"
}

@test "headers: a stored token that would break out of the JSON is refused, and nothing reaches stdout" {
  # Escaped in the file, so it still parses as JSON and decodes to AT"},"X
  seed_credentials 'AT\"},\"X' "RT-cached" "$(($(date +%s) + 3600))"

  run --separate-stderr env -u SLOP_HAUS_URL "${TOKEN_SH}" headers
  assert_failure
  assert_equal "$output" ""
  [[ "$stderr" == *"malformed"* ]]
}

@test "headers: no stored credentials fails with nothing on stdout" {
  run --separate-stderr env -u SLOP_HAUS_URL "${TOKEN_SH}" headers
  assert_failure
  assert_equal "$output" ""
  [[ "$stderr" == *"token.sh login"* ]]
}

@test "headers: an unknown argument is a usage error" {
  run_token_sh -- headers --bogus
  assert_failure 2
}

# -- the lock is shared with the Python module --
#
# util-linux flock(1) and fcntl.flock are the same advisory flock(2), which is what lets the two
# implementations coexist during the soak. A holder written in Python must keep token.sh (either
# body) out of the refresh until it lets go.
@test "lock: a refresh waits for a lock held from Python, then makes exactly one request" {
  seed_credentials "AT-old" "RT-old" "$(($(date +%s) + 30))"
  set_refresh '{"access_token":"AT-new","refresh_token":"RT-new","expires_in":900}'
  local release="${BATS_TEST_TMPDIR}/release" held="${BATS_TEST_TMPDIR}/held"
  mkfifo "${release}"
  "${REAL_PYTHON3}" -I -c '
import fcntl, os, sys
fd = os.open(sys.argv[1], os.O_RDWR | os.O_CREAT, 0o600)
fcntl.flock(fd, fcntl.LOCK_EX)
print("held", flush=True)
open(sys.argv[2]).read()
' "${ROOT}/.slop-haus/credentials.lock" "${release}" > "${held}" &
  local holder=$!
  local waited=0
  until [[ -s "${held}" ]]; do
    "${REAL_SLEEP}" 0.1
    ((++waited > 100)) && fail "the Python holder never took the lock"
  done

  env -u SLOP_HAUS_URL -u SLOP_HAUS_AGENT "${TOKEN_SH}" > "${BATS_TEST_TMPDIR}/out" 2>&1 &
  local caller=$!
  "${REAL_SLEEP}" 1
  assert [ ! -s "${STATE_DIR}/refresh.calls" ]
  kill -0 "${caller}"

  echo > "${release}"
  wait "${holder}"
  wait "${caller}"
  assert_equal "$(cat "${BATS_TEST_TMPDIR}/out")" "AT-new"
  assert_equal "$(refresh_calls)" "1"
}

# -- the shim: SLOP_HAUS_TOKEN_IMPL --
#
# These drive token.sh's own choice between the module and its bash body, so each clears the knob
# itself and only means something once: under the python run's setup, which leaves it unset.

python_ran() { grep -q -- "-I ${MODULE}" "${PYTHON_LOG}"; }

@test "shim: with the knob unset get and headers go to the module, isolated, and print no warning" {
  [[ "${TOKEN_IMPL}" == python ]] || skip "covered by the python run"
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN_IMPL "${TOKEN_SH}"
  assert_success
  assert_equal "$output" "AT-cached"
  assert_equal "$stderr" ""
  python_ran
  : > "${PYTHON_LOG}"
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN_IMPL "${TOKEN_SH}" headers
  assert_success
  assert_output --partial "Bearer AT-cached"
  python_ran
}

@test "shim: exactly 'bash' forces the bash body; any other value is python" {
  [[ "${TOKEN_IMPL}" == python ]] || skip "covered by the python run"
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"
  run_token_sh SLOP_HAUS_TOKEN_IMPL=bash
  assert_success
  ! python_ran
  for value in Bash python python3 1 ""; do
    : > "${PYTHON_LOG}"
    run_token_sh "SLOP_HAUS_TOKEN_IMPL=${value}"
    assert_success
    python_ran
  done
}

@test "shim: python hands login to the module too" {
  [[ "${TOKEN_IMPL}" == python ]] || skip "covered by the python run"
  set_poll_at 1 '{"access_token":"AT1","refresh_token":"RT1","expires_in":900}'
  run_token_sh -- login
  assert_success
  python_ran
  assert_equal "$(credentials_field access_token)" "AT1"
}

@test "shim: a python3 older than 3.9 falls back to bash with a one-line warning" {
  [[ "${TOKEN_IMPL}" == python ]] || skip "covered by the python run"
  local bin="${BATS_TEST_TMPDIR}/bin"
  cat > "${bin}/python3" << 'STUB'
#!/usr/bin/env bash
if [[ "$1 $2" == "-I -c" && "$3" == *"version_info"* ]]; then exit 1; fi
echo "$*" >> "${PYTHON_LOG}"
exec "${REAL_PYTHON3}" "$@"
STUB
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN_IMPL "${TOKEN_SH}"
  assert_success
  assert_equal "$output" "AT-cached"
  assert_equal "$(printf '%s\n' "$stderr" | wc -l)" "1"
  [[ "$stderr" == *"3.9"* ]]
  ! python_ran
}

@test "shim: no python3 at all falls back to bash with a one-line warning" {
  [[ "${TOKEN_IMPL}" == python ]] || skip "covered by the python run"
  local bin="${BATS_TEST_TMPDIR}/no-python-bin" tool
  mkdir -p "${bin}"
  for tool in bash dirname cat head mkdir date grep sed; do
    ln -sf "$(command -v "${tool}")" "${bin}/${tool}"
  done
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN_IMPL PATH="${bin}" "${TOKEN_SH}"
  assert_success
  assert_equal "$output" "AT-cached"
  assert_equal "$(printf '%s\n' "$stderr" | wc -l)" "1"
  [[ "$stderr" == *"python3 is not installed"* ]]
}

@test "shim: a missing or symlinked module falls back to bash with a warning" {
  [[ "${TOKEN_IMPL}" == python ]] || skip "covered by the python run"
  seed_credentials "AT-cached" "RT-cached" "$(($(date +%s) + 3600))"
  rm "${MODULE}"
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN_IMPL "${TOKEN_SH}"
  assert_success
  [[ "$stderr" == *"missing or a symlink"* ]]
  ln -s "${BATS_TEST_DIRNAME}/../py/slop_haus_token.py" "${MODULE}"
  run --separate-stderr env -u SLOP_HAUS_URL -u SLOP_HAUS_TOKEN_IMPL "${TOKEN_SH}"
  assert_success
  [[ "$stderr" == *"missing or a symlink"* ]]
  ! python_ran
}
