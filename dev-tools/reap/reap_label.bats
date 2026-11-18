#!/usr/bin/env bats
# What a suite leaves behind when its CI step is killed (a cancelled
# pipeline never runs teardown_file) holds one of an agent's few docker
# networks, or runs on, until someone removes it by hand. Anything a suite
# makes that outlives its command carries the reap label, so the next run
# removes it (reap_lib.sh). unlabelled_docker.sh finds the commands that
# don't; the suites below are the exceptions, and why.

setup() {
  bats_load_library repo_test
  SCAN="${BATS_TEST_DIRNAME}/unlabelled_docker.sh"
}

# file|reason. Only an entry that still has an unlabelled command passes
# (see the last test), so this list can't outlive what it excuses.
ALLOWED=(
  # Images only: tagged or by id, no container and no network, so nothing
  # runs on or holds a network slot. The same tags are rebuilt from cache,
  # and the daily prune takes the untagged ones.
  "slop-haus/frontend/version.bats|image builds only"
  # Tests the reaping itself: labels with prefixes of its own, which its
  # teardown removes, and which must not be the real label.
  "dev-tools/reap/reap_lib.bats|exercises the reaper with labels of its own"
)

allowed_files() {
  local entry
  for entry in "${ALLOWED[@]}"; do
    echo "${entry%%|*}"
  done
}

tracked_suites() {
  git -C "${BATS_TEST_DIRNAME}/../.." ls-files -z -- '*.bats'
}

@test "no bats suite makes a network, container or image without the reap label" {
  cd "${BATS_TEST_DIRNAME}/../.."
  local allowed
  allowed="$(allowed_files)"
  local -a suites=()
  local f
  while IFS= read -r -d '' f; do
    grep -qxF -- "${f}" <<< "${allowed}" || suites+=("${f}")
  done < <(tracked_suites)
  run "${SCAN}" "${suites[@]}"
  assert_success
  assert_output ""
}

@test "the scan flags each kind of command, naming file and line" {
  run "${SCAN}" "${BATS_TEST_DIRNAME}/testdata/unlabelled.sh"
  assert_success
  assert_line --regexp 'unlabelled.sh:3: .*network create'
  assert_line --regexp 'unlabelled.sh:4: .*docker create'
  assert_line --regexp 'unlabelled.sh:5: .*docker build'
  assert_line --regexp 'unlabelled.sh:6: .*-d --rm'
  assert_line --regexp 'unlabelled.sh:7: .*run img'
  assert_equal "${#lines[@]}" 5
}

@test "the scan passes labelled commands, joined across continuations" {
  run "${SCAN}" "${BATS_TEST_DIRNAME}/testdata/labelled.sh"
  assert_success
  assert_output ""
}

@test "every allowed suite still has an unlabelled command" {
  cd "${BATS_TEST_DIRNAME}/../.."
  local f
  while IFS= read -r f; do
    git ls-files --error-unmatch -- "${f}" > /dev/null
    [[ -n "$("${SCAN}" "${f}")" ]] || {
      echo "${f} is allowed but has nothing unlabelled: drop it from ALLOWED" >&2
      return 1
    }
  done < <(allowed_files)
}
