# Tests for nginx.conf.template's forwarding and response headers.
#
# Unlike most suites here, this one runs the real thing: nginx's handling
# of headers is what's under test, so a stub would only test the stub. It
# starts nginx with this app's configuration in front of a whoami echo
# server standing in for the api, and reads back the headers the "api"
# received. Needs docker.
#
# Everything talks over the suite's own Docker network -- requests are made
# from a curl container on it, and nothing is published to the host -- so
# it runs the same wherever the Docker daemon is, including from inside a
# CI step, whose localhost isn't the daemon's.
#
#   bats/run.sh slop-haus/frontend             # the Dockerfile's nginx-conf
#                                              # stage, configuration baked in
#   ATTACH=1 bats/run.sh slop-haus/frontend    # the same nginx with the two
#                                              # files mounted from here, to
#                                              # iterate without a rebuild

setup_file() {
  # The name is random rather than $$ (CI steps run in their own containers,
  # where two concurrent runs on one daemon can easily share a PID), and
  # everything the suite makes carries it as its reap project, so a run
  # killed before teardown_file has its stack removed by the next one.
  # shellcheck source=SCRIPTDIR/../../dev-tools/reap/reap_lib.sh
  source "${BATS_TEST_DIRNAME}/../../dev-tools/reap/reap_lib.sh"
  local self
  self="$(reap_self_container)"
  export NGINX_TEST_NET
  NGINX_TEST_NET="$(reap_compose_project slop-haus-nginx-bats "${self}")"
  reap_compose slop-haus-nginx-bats "${NGINX_TEST_NET}" "${self}"
  REAP_LABEL="${REAP_COMPOSE_LABEL}=${NGINX_TEST_NET}"
  export NGINX_TEST_API="${NGINX_TEST_NET}-api"
  export NGINX_TEST_WEB="${NGINX_TEST_NET}-web"
  export NGINX_TEST_CURL="${NGINX_TEST_NET}-curl"
  export NGINX_TEST_URL="http://${NGINX_TEST_WEB}:80"

  local image
  local -a config=()
  if [[ "${ATTACH:-0}" == "1" ]]; then
    image="$(awk '/^FROM nginx:.* AS nginx-conf$/ { print $2 }' "${BATS_TEST_DIRNAME}/Dockerfile")"
    [ -n "${image}" ] || {
      echo "no 'FROM nginx:... AS nginx-conf' line in the Dockerfile" >&2
      return 1
    }
    config=(
      -v "${BATS_TEST_DIRNAME}/nginx.conf.template:/etc/nginx/templates/default.conf.template:ro"
      -v "${BATS_TEST_DIRNAME}/security_headers.conf:/etc/nginx/security_headers.conf:ro"
    )
  else
    # By ID, not a shared tag a concurrent run could retag under this one.
    image="$(docker build --quiet --label "${REAP_LABEL}" --target nginx-conf \
      -f "${BATS_TEST_DIRNAME}/Dockerfile" "${BATS_TEST_DIRNAME}/../..")"
  fi

  docker network create --label "${REAP_LABEL}" "${NGINX_TEST_NET}" > /dev/null
  docker run -d --rm --label "${REAP_LABEL}" --name "${NGINX_TEST_API}" --network "${NGINX_TEST_NET}" \
    traefik/whoami:v1.12.0 --port 8080 > /dev/null
  docker run -d --rm --label "${REAP_LABEL}" --name "${NGINX_TEST_WEB}" --network "${NGINX_TEST_NET}" \
    "${config[@]}" \
    -e API_UPSTREAM="${NGINX_TEST_API}:8080" \
    -e DNS_RESOLVER=127.0.0.11 \
    -e GITHUB_WEB_ORIGIN=https://github.com \
    "${image}" > /dev/null
  docker run -d --rm --label "${REAP_LABEL}" --name "${NGINX_TEST_CURL}" --network "${NGINX_TEST_NET}" \
    --entrypoint sleep curlimages/curl:8.10.1 3600 > /dev/null

  local i
  for i in $(seq 1 50); do
    nginx_curl -sf -o /dev/null "${NGINX_TEST_URL}/api/ready" && return 0
    sleep 0.1
  done
  docker logs "${NGINX_TEST_WEB}" >&2
  return 1
}

# curl, from the suite's network.
nginx_curl() {
  docker exec "${NGINX_TEST_CURL}" curl "$@"
}

teardown_file() {
  # shellcheck source=SCRIPTDIR/../../dev-tools/reap/reap_lib.sh
  source "${BATS_TEST_DIRNAME}/../../dev-tools/reap/reap_lib.sh"
  reap_compose_down "${NGINX_TEST_NET}"
}

setup() {
  bats_load_library repo_test
}

# The upstream's view of one header, for a request through nginx.
upstream_header() {
  local path="$1" header="$2"
  shift 2
  nginx_curl -sf "$@" "${NGINX_TEST_URL}${path}" | tr -d '\r' | sed -n "s/^${header}: //p"
}

# The case the front door depends on: TLS ends at the proxy in front of
# nginx, so the scheme it reports must reach the api, or the api hands
# agents an http:// address for an https-only front door.
@test "the scheme a proxy in front reports reaches the api, on every proxied path" {
  local path
  for path in /api/x /mcp /hooks/stop; do
    run upstream_header "${path}" X-Forwarded-Proto -H "X-Forwarded-Proto: https"
    assert_success
    assert_output "https"
  done
}

@test "with no proxy in front, the api is told nginx's own scheme, on every proxied path" {
  local path
  for path in /api/x /mcp /hooks/stop; do
    run upstream_header "${path}" X-Forwarded-Proto
    assert_success
    assert_output "http"
  done
}

# The other half of the address the api hands agents.
@test "the Host the client used reaches the api, port included, on every proxied path" {
  local path
  for path in /api/x /mcp /hooks/stop; do
    run upstream_header "${path}" Host -H "Host: slop-haus.example:8443"
    assert_success
    assert_output "slop-haus.example:8443"
  done
}

# A header nginx itself sent back, for a request through it.
response_header() {
  local path="$1" header="$2"
  nginx_curl -s -o /dev/null -D - "${NGINX_TEST_URL}${path}" | tr -d '\r' | sed -n "s/^${header}: //Ip"
}

# One path per location, so a location that sets its own add_header (and so
# drops the server-level ones) without including the snippet is caught.
# /admin/x is a 404 once whoami's 200 lets it past the admin gate, which
# also covers error responses. /api/x covers the proxied locations.
security_header_paths=(/ /deep/link /version.json /assets/missing.js /admin/x /admin/assets/missing.js /api/x /mcp /oauth/token)

@test "every location sends the security headers, with their intended values" {
  local csp
  csp="$(response_header / Content-Security-Policy)"
  [ -n "${csp}" ] || fail "/ sent no Content-Security-Policy"

  local -A want=(
    ["Content-Security-Policy"]="${csp}"
    ["X-Frame-Options"]="DENY"
    ["X-Content-Type-Options"]="nosniff"
    ["Referrer-Policy"]="same-origin"
    ["Strict-Transport-Security"]="max-age=31536000"
  )
  local path header
  for path in "${security_header_paths[@]}"; do
    for header in "${!want[@]}"; do
      run response_header "${path}" "${header}"
      assert_success
      [ "${output}" = "${want[${header}]}" ] || fail "${path} sent ${header}: '${output}', want '${want[${header}]}'"
    done
  done
}

@test "the CSP allows only this origin (and GitHub to post the App manifest to), and no framing or inline code" {
  run response_header / Content-Security-Policy
  assert_success
  assert_output "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self' https://github.com; frame-ancestors 'none'"
}

@test "a location's own Cache-Control survives alongside the security headers" {
  run response_header / Cache-Control
  assert_output "no-cache"
}

@test "version.json is served as JSON, never from a cache" {
  docker exec "${NGINX_TEST_WEB}" sh -c 'echo "{\"version\":\"t-1\"}" > /usr/share/nginx/html/version.json'
  run response_header /version.json Cache-Control
  assert_output "no-store"
  run response_header /version.json Content-Type
  assert_output "application/json"
  run nginx_curl -sf "${NGINX_TEST_URL}/version.json"
  assert_success
  assert_output '{"version":"t-1"}'
}

@test "a missing version.json is a 404, not the SPA standing in for it" {
  docker exec "${NGINX_TEST_WEB}" rm -f /usr/share/nginx/html/version.json
  run nginx_curl -s -o /dev/null -w '%{http_code}' "${NGINX_TEST_URL}/version.json"
  assert_output "404"
  run response_header /version.json Cache-Control
  assert_output "no-store"
}

# The access log goes to VictoriaLogs, so what it holds is a privacy matter.
# The image's /var/log/nginx/access.log is a symlink to stdout, so it is
# read back from the container's logs.
@test "the access log has the request's path but no query string, cookie or other header" {
  run nginx_curl -s -o /dev/null -H "Cookie: session=COOKIE_SECRET" -H "Authorization: Bearer AUTH_SECRET" \
    -H "Referer: http://x/REFERRER_SECRET" -A "AGENT_SECRET" "${NGINX_TEST_URL}/api/tickets/7?code=QUERY_SECRET&state=x"
  assert_success
  local log="" i
  for i in $(seq 1 50); do
    log="$(docker logs "${NGINX_TEST_WEB}" 2>&1 | grep '"path":"/api/tickets/7"' || true)"
    [ -n "${log}" ] && break
    sleep 0.1
  done
  [ -n "${log}" ] || fail "no access log line for the request"
  [[ "${log}" =~ ^\{\"time\":\"[^\"]+\",\"method\":\"GET\",\"path\":\"/api/tickets/7\",\"status\":200,\"bytes\":[0-9]+,\"duration\":[0-9.]+,\"request_id\":\"[0-9a-f]{32}\"\}$ ]] ||
    fail "unexpected access log line: ${log}"
  run docker logs "${NGINX_TEST_WEB}"
  refute_output --partial QUERY_SECRET
  refute_output --partial COOKIE_SECRET
  refute_output --partial AUTH_SECRET
  refute_output --partial REFERRER_SECRET
  refute_output --partial AGENT_SECRET
}

@test "every location's Server header names nginx but not its version" {
  local path
  for path in "${security_header_paths[@]}"; do
    run response_header "${path}" Server
    assert_success
    [ "${output}" = "nginx" ] || fail "${path} sent Server: '${output}', want 'nginx'"
  done
}
