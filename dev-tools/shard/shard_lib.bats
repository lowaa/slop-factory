#!/usr/bin/env bats
# shard_lib.sh against a fake shard script: a stack per shard is what the
# real callers start, but what the library owns -- running every shard at
# once, telling each which it is, keeping their output apart and passing
# their failures and signals on -- needs no stack to show.

setup() {
  bats_load_library repo_test
  # shellcheck source=SCRIPTDIR/shard_lib.sh
  source "${BATS_TEST_DIRNAME}/shard_lib.sh"
  load fake_npx
  DIR="${BATS_TEST_TMPDIR}"
  SHARD="${DIR}/shard.sh"
  # Records what it was told (its name and arguments too), prints a line before and after a barrier every
  # shard has to reach, then exits with FAIL_<index> (default 0). The barrier
  # is the proof the shards run at once: run one at a time, the first would
  # wait for the others forever -- bounded, so a broken library fails rather
  # than hangs. HOLD makes a shard wait past the barrier, for a signal.
  cat > "${SHARD}" << 'EOF'
#!/usr/bin/env bash
dir="$(dirname "$0")"
echo "${SHARDS} ${SHARD_INDEX}" > "${dir}/args.${SHARD_INDEX}"
printf '%s\n' "${SHARD_NAME}" "$@" > "${dir}/named.${SHARD_INDEX}"
echo run >> "${dir}/runs.${SHARD_INDEX}"
trap 'touch "${dir}/term.${SHARD_INDEX}"; exit 143' TERM
echo "${SHARD_INDEX}:before"
touch "${dir}/started.${SHARD_INDEX}"
for _ in $(seq 400); do
  (($(find "${dir}" -name 'started.*' | wc -l) >= SHARDS)) && break
  sleep 0.05
done
(($(find "${dir}" -name 'started.*' | wc -l) >= SHARDS)) || exit 99
echo "${SHARD_INDEX}:after"
if [[ -n "${HOLD:-}" ]]; then
  for _ in $(seq 400); do sleep 0.05; done
  exit 98
fi
fail="FAIL_${SHARD_INDEX}"
exit "${!fail:-0}"
EOF
  chmod +x "${SHARD}"
}

@test "every shard runs at once, told the count and its own index" {
  run shard_run_all 3 "${SHARD}"
  [ "${status}" -eq 0 ]
  for i in 1 2 3; do
    [ "$(cat "${DIR}/args.${i}")" = "3 ${i}" ]
  done
}

@test "each shard's output is printed whole, under a header of its own" {
  run shard_run_all 3 "${SHARD}"
  [ "${status}" -eq 0 ]
  for i in 1 2 3; do
    local header
    header="$(printf '%s\n' "${lines[@]}" | grep -n "^::group::shard ${i}/3 (exit 0, " | cut -d: -f1)"
    [ -n "${header}" ]
    [ "${lines[${header}]}" = "${i}:before" ]
    [ "${lines[$((header + 1))]}" = "${i}:after" ]
    [ "${lines[$((header + 2))]}" = "::endgroup::" ]
  done
}

@test "a failing shard fails the run, and the others still finish and report" {
  FAIL_2=7 run shard_run_all 3 "${SHARD}"
  [ "${status}" -eq 7 ]
  [[ "${output}" == *"::group::shard 2/3 (exit 7, "* ]]
  [[ "${output}" == *"::group::shard 1/3 (exit 0, "* ]]
  [[ "${output}" == *"::group::shard 3/3 (exit 0, "* ]]
}

@test "with several failing, the status is the lowest-numbered one's" {
  FAIL_1=3 FAIL_3=5 run shard_run_all 3 "${SHARD}"
  [ "${status}" -eq 3 ]
}

@test "a single shard is still run" {
  run shard_run_all 1 "${SHARD}"
  [ "${status}" -eq 0 ]
  [ "$(cat "${DIR}/args.1")" = "1 1" ]
}

@test "a count that isn't a positive integer runs nothing" {
  local count
  for count in 0 -1 x "" 1.5; do
    run shard_run_all "${count}" "${SHARD}"
    [ "${status}" -eq 2 ]
    [[ "${output}" == *"SHARDS must be a positive integer"* ]]
  done
  [ -z "$(find "${DIR}" -name 'args.*')" ]
}

@test "a TERM is passed on to every shard, and waited out" {
  HOLD=1 bash -c 'source "$1"; shard_run_all 2 "$2"' _ "${BATS_TEST_DIRNAME}/shard_lib.sh" "${SHARD}" \
    > "${DIR}/out" 2>&1 &
  local runner=$!
  for _ in $(seq 400); do
    (($(find "${DIR}" -name 'started.*' | wc -l) >= 2)) && break
    sleep 0.05
  done
  kill -TERM "${runner}"
  local status=0
  wait "${runner}" || status=$?
  [ "${status}" -eq 143 ]
  [ -f "${DIR}/term.1" ]
  [ -f "${DIR}/term.2" ]
  grep -q "^::group::shard 1/2 (exit 143, " "${DIR}/out"
  grep -q "^::group::shard 2/2 (exit 143, " "${DIR}/out"
}

@test "shard_run_each runs the command once per name, told its name, with the arguments" {
  run shard_run_each claude pi codex -- "${SHARD}" --scenario "a b"
  [ "${status}" -eq 0 ]
  local i=1 name
  for name in claude pi codex; do
    [ "$(cat "${DIR}/args.${i}")" = "3 ${i}" ]
    [ "$(cat "${DIR}/named.${i}")" = "$(printf '%s\n' "${name}" --scenario "a b")" ]
    [ "$(wc -l < "${DIR}/runs.${i}")" -eq 1 ]
    i=$((i + 1))
  done
}

@test "shard_run_each prints each run whole, under its name" {
  FAIL_2=4 run shard_run_each claude pi -- "${SHARD}"
  [ "${status}" -eq 4 ]
  local i=1 name header
  for name in claude pi; do
    header="$(printf '%s\n' "${lines[@]}" | grep -n "^::group::${name} (exit $(((i - 1) * 4)), " | cut -d: -f1)"
    [ -n "${header}" ]
    [ "${lines[${header}]}" = "${i}:before" ]
    [ "${lines[$((header + 1))]}" = "${i}:after" ]
    [ "${lines[$((header + 2))]}" = "::endgroup::" ]
    i=$((i + 1))
  done
}

@test "shard_run_each returns the first-named failing run's status" {
  FAIL_2=3 FAIL_3=5 run shard_run_each claude pi codex -- "${SHARD}"
  [ "${status}" -eq 3 ]
}

@test "shard_run_each ends with one line giving every run's status" {
  FAIL_2=3 run shard_run_each claude pi -- "${SHARD}"
  [ "${lines[-1]}" = "shards: returning 3 (claude: 0,pi: 3)" ]
}

@test "shard_run_each without names, the separator or a command runs nothing" {
  local usage
  for usage in "-- ${SHARD}" "claude ${SHARD}" "claude --" ""; do
    # shellcheck disable=SC2086 # split on purpose: each is an argument list
    run shard_run_each ${usage}
    [ "${status}" -eq 2 ]
    [[ "${output}" == *"usage: shard_run_each"* ]]
  done
  [ -z "$(find "${DIR}" -name 'args.*')" ]
}

# A docker that lists, for `image ls` in the repository:tag format, the refs
# a test wrote to images.<project> for the project its label filter names
# (or fails, with FAIL_LS), records `image tag` (or fails on FAIL_TAG's ref),
# and fails on anything else.
fake_docker() {
  mkdir -p "${DIR}/bin"
  cat > "${DIR}/bin/docker" << 'EOF_DOCKER'
#!/usr/bin/env bash
dir="$(dirname "$0")/.."
case "$1 $2" in
  "image ls")
    echo "$*" >> "${dir}/ls_calls"
    [[ -z "${FAIL_LS:-}" ]] || exit 7
    [[ "$*" =~ label=com\.docker\.compose\.project=([^ ]+) ]] || exit 3
    [[ "$*" == *" --format {{.Repository}}:{{.Tag}}"* ]] || exit 5
    cat "${dir}/images.${BASH_REMATCH[1]}" 2> /dev/null || true
    ;;
  "image tag")
    [[ "$3" != "${FAIL_TAG:-}" ]] || exit 6
    echo "$3 $4" >> "${dir}/tags"
    ;;
  *) echo "unexpected: docker $*" >&2; exit 4 ;;
esac
EOF_DOCKER
  chmod +x "${DIR}/bin/docker"
  PATH="${DIR}/bin:${PATH}"
}

@test "a shard's images are the parent's, tagged under the shard's project" {
  fake_docker
  printf '%s\n' parent-api:latest parent-frontend:latest parent-e2e-tests-ts:latest > "${DIR}/images.parent"
  run shard_share_images parent shard-1
  [ "${status}" -eq 0 ]
  [ "$(sort "${DIR}/tags")" = "$(printf '%s\n' \
    "parent-api:latest shard-1-api:latest" \
    "parent-e2e-tests-ts:latest shard-1-e2e-tests-ts:latest" \
    "parent-frontend:latest shard-1-frontend:latest" | sort)" ]
  grep -q -- "--filter label=com.docker.compose.project=parent " "${DIR}/ls_calls"
}

@test "an image the parent built under another name is left alone" {
  fake_docker
  printf '%s\n' parent-api:latest elsewhere-api:latest > "${DIR}/images.parent"
  run shard_share_images parent shard-1
  [ "${status}" -eq 0 ]
  [ "$(cat "${DIR}/tags")" = "parent-api:latest shard-1-api:latest" ]
}

@test "with nothing built, nothing is tagged" {
  fake_docker
  run shard_share_images parent shard-1
  [ "${status}" -eq 0 ]
  [ ! -e "${DIR}/tags" ]
}

@test "a docker that can't list the images fails it" {
  fake_docker
  printf '%s\n' parent-api:latest > "${DIR}/images.parent"
  FAIL_LS=1 run shard_share_images parent shard-1
  [ "${status}" -eq 7 ]
  [ ! -e "${DIR}/tags" ]
}

@test "an image that can't be tagged fails it" {
  fake_docker
  printf '%s\n' parent-api:latest parent-frontend:latest > "${DIR}/images.parent"
  FAIL_TAG=parent-api:latest run shard_share_images parent shard-1
  [ "${status}" -eq 6 ]
}

# The command shard_playwright_command set, run as the container would.
run_shard_command() {
  shard_playwright_command "$@"
  run "${SHARD_COMMAND[@]}"
}

@test "a shard past the first runs only its share of the split project" {
  fake_npx "${DIR}"
  run_shard_command 2 3 e2e e2e_support
  [ "${status}" -eq 0 ]
  [ "$(cat "${DIR}/npx_calls")" = "playwright test --project=e2e --shard=2/3" ]
}

@test "the first shard runs the unsplit projects whole, then its share" {
  fake_npx "${DIR}"
  run_shard_command 1 3 e2e e2e_support other
  [ "${status}" -eq 0 ]
  [ "$(cat "${DIR}/npx_calls")" = "$(printf '%s\n' \
    "playwright test --project=e2e_support --project=other" \
    "playwright test --project=e2e --shard=1/3")" ]
}

@test "with no unsplit projects, the first shard runs only its share" {
  fake_npx "${DIR}"
  run_shard_command 1 2 e2e
  [ "${status}" -eq 0 ]
  [ "$(cat "${DIR}/npx_calls")" = "playwright test --project=e2e --shard=1/2" ]
}

@test "an unsplit project's failure fails the shard, and its share still runs" {
  fake_npx "${DIR}"
  FAIL_e2e_support=3 run_shard_command 1 2 e2e e2e_support
  [ "${status}" -eq 3 ]
  [ "$(wc -l < "${DIR}/npx_calls")" -eq 2 ]
  [ "$(tail -n 1 "${DIR}/npx_calls")" = "playwright test --project=e2e --shard=1/2" ]
}

@test "the split project's failure fails the shard, with its own status" {
  fake_npx "${DIR}"
  FAIL_e2e=5 run_shard_command 1 2 e2e e2e_support
  [ "${status}" -eq 5 ]
  FAIL_e2e=5 FAIL_e2e_support=3 run_shard_command 1 2 e2e e2e_support
  [ "${status}" -eq 5 ]
}

@test "a project name is passed as one argument, unexpanded" {
  fake_npx "${DIR}"
  cd "${DIR}"
  # shellcheck disable=SC2016 # the expansion is what must not happen
  run_shard_command 1 2 'e2e $(touch pwned)' 'a b*'
  [ "${status}" -eq 0 ]
  [ ! -e pwned ]
  [ "$(cat "${DIR}/npx_calls")" = "$(printf '%s\n' \
    'playwright test --project=a\ b\*' \
    'playwright test --project=e2e\ \$\(touch\ pwned\) --shard=1/2')" ]
}
