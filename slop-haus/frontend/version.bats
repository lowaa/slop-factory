# Tests for the Dockerfile's version stage: the /version.json a deploy
# polls to see its build take over. Needs docker.
#
#   bats/run.sh -f version slop-haus/frontend

setup() {
  bats_load_library repo_test
  require_command docker
}

# build_version <tag> -- builds the version stage with BUILD_TAG, setting
# IMAGE to it.
build_version() {
  IMAGE="$(docker build --quiet --target version --build-arg "BUILD_TAG=$1" \
    -f "${BATS_TEST_DIRNAME}/Dockerfile" "${BATS_TEST_DIRNAME}/../..")"
}

@test "version.json reports the tag the image was built as" {
  build_version 20260929T080000Z-bbbbbbbb
  run docker run --rm --entrypoint cat "${IMAGE}" /version.json
  assert_success
  assert_output '{"version":"20260929T080000Z-bbbbbbbb"}'
}

@test "an image built without a tag says dev" {
  IMAGE="$(docker build --quiet --target version \
    -f "${BATS_TEST_DIRNAME}/Dockerfile" "${BATS_TEST_DIRNAME}/../..")"
  run docker run --rm --entrypoint cat "${IMAGE}" /version.json
  assert_output '{"version":"dev"}'
}

# The tag goes into JSON unescaped, so anything that could break out of
# the string fails the build rather than making an image.
@test "a tag outside the tag alphabet fails the build" {
  local tag
  for tag in 'x"y' 'a b' 'a\b' ''; do
    run docker build --quiet --target version --build-arg "BUILD_TAG=${tag}" \
      -f "${BATS_TEST_DIRNAME}/Dockerfile" "${BATS_TEST_DIRNAME}/../.."
    assert_failure
  done
}
