#!/usr/bin/env bats
# reap_lib.sh against a real docker daemon: whether a compose stack comes
# down is a matter of what docker and compose actually label, which a stub
# can't answer. Everything a test makes is named after a prefix of its own,
# so a run on a shared daemon (the CI agent's) touches nothing else.

IMAGE="busybox:1.37"

setup_file() {
  docker pull -q "${IMAGE}" > /dev/null
}

setup() {
  bats_load_library repo_test
  require_command docker od
  # shellcheck source=SCRIPTDIR/reap_lib.sh
  source "${BATS_TEST_DIRNAME}/reap_lib.sh"
  PREFIX="$(unique_name reap-test)"
  LIVE_OWNER="$(docker run -d --label "${PREFIX}=owner" "${IMAGE}" sleep 600)"
  DEAD_OWNER="$(docker create --label "${PREFIX}=owner" "${IMAGE}")"
}

# Everything carrying this run's prefix, by project label or by name, found
# by the library or not: the prefix is random per test, so this touches
# nothing else on a shared daemon. A failing test leaves its stacks as they
# were, and some are made on purpose where the library would not look
# (another prefix that merely starts like this one).
teardown() {
  local project kind
  for project in $(_reap_compose_projects | grep "^${PREFIX}" || true); do
    reap_compose_down "${project}"
  done
  docker container ls -aq --filter "label=${PREFIX}=owner" | xargs -r docker container rm -f -v > /dev/null 2>&1 || true
  docker container ls -aq --filter "name=^${PREFIX}" | xargs -r docker container rm -f -v > /dev/null 2>&1 || true
  for kind in network volume; do
    docker "${kind}" ls -q --filter "name=^${PREFIX}" | xargs -r docker "${kind}" rm > /dev/null 2>&1 || true
  done
  docker image ls -q --filter "reference=${PREFIX}*" | xargs -r docker image rm > /dev/null 2>&1 || true
}

# stack <project> -- what a compose project leaves as the fewest docker
# calls can: a running container and a stopped one (a one-shot service,
# a finished `compose run`), and a network and a volume of its own. The
# running one also has an anonymous volume, which carries no project label;
# its name is kept in $BATS_TEST_TMPDIR/<project>.anon for assert_gone.
stack() {
  local label="${REAP_COMPOSE_LABEL}=$1" id
  docker network create --label "${label}" "$1_default" > /dev/null
  docker volume create --label "${label}" "$1_data" > /dev/null
  id="$(docker run -d --label "${label}" --network "$1_default" -v "$1_data:/data" -v /anon "${IMAGE}" sleep 600)"
  docker container inspect -f '{{range .Mounts}}{{if eq .Destination "/anon"}}{{.Name}}{{end}}{{end}}' "${id}" \
    > "${BATS_TEST_TMPDIR}/$1.anon"
  docker create --label "${label}" --network "$1_default" "${IMAGE}" > /dev/null
}

# left <project> -- every container, network, volume and image of it, one
# per line; nothing when it is gone.
left() {
  local filter="label=${REAP_COMPOSE_LABEL}=$1"
  docker container ls -aq --filter "${filter}"
  docker network ls -q --filter "${filter}"
  docker volume ls -q --filter "${filter}"
  docker image ls -q --filter "${filter}"
}

assert_gone() {
  run left "$1"
  assert_success
  assert_output ""
  if [[ -s "${BATS_TEST_TMPDIR}/$1.anon" ]]; then
    run docker volume inspect "$(cat "${BATS_TEST_TMPDIR}/$1.anon")"
    assert_failure
  fi
}

# assert_kept <project> -- all four things stack made are still there.
assert_kept() {
  run left "$1"
  assert_success
  assert_equal "${#lines[@]}" 4
}

# Two containers' ids, for mountinfo as docker hosts and containers have it.
ID_A="9325b8b652c11e6f0bbd9096d2818f5c1470a57c139aa914488897185e57cdf2"
ID_B="4f1c2d3e4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d"

@test "unique_name differs between calls in one shell, as the shell PID would not" {
  local a b
  a="$(unique_name net)"
  b="$(unique_name net)"
  assert_not_equal "${a}" "${b}"
  [[ "${a}" =~ ^net-[0-9a-f]{8}$ ]]
}

@test "on a docker host, the other containers' mounts are not taken to be this one's" {
  run _reap_mountinfo_container_id << EOF
29 1 253:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw,discard
33 27 0:28 / /dev/shm rw,nosuid,nodev shared:4 - tmpfs tmpfs rw,inode64
512 29 0:70 / /var/lib/docker/containers/${ID_A}/mounts/shm rw,nosuid,nodev,noexec,relatime shared:280 - tmpfs shm rw,size=65536k,inode64
520 29 0:46 / /var/lib/docker/overlay2/3a0358fc1e0d5e6e3e30f7e228770caf0a9bb27ded3a825c8325657b3ca540d7/merged rw,relatime shared:285 - overlay overlay rw
530 29 0:71 / /var/lib/docker/containers/${ID_B}/mounts/shm rw,nosuid,nodev,noexec,relatime shared:290 - tmpfs shm rw,size=65536k,inode64
EOF
  assert_success
  assert_output ""
}

@test "in a container, its id is the source of /etc/hostname, not of any other mount" {
  # Another container's directory mounted before it, as a step with the
  # docker root bind-mounted would have.
  run _reap_mountinfo_container_id << EOF
361 69 0:46 / / rw,relatime - overlay overlay rw
369 364 0:60 / /dev/shm rw,nosuid,nodev,noexec,relatime - tmpfs shm rw,size=65536k,inode64
370 361 253:1 /var/lib/docker/containers/${ID_B}/mounts/shm /mnt/shm rw,relatime - ext4 /dev/vda1 rw
371 361 253:1 /var/lib/docker/containers/${ID_A}/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/vda1 rw
372 361 253:1 /var/lib/docker/containers/${ID_A}/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw
373 361 253:1 /var/lib/docker/containers/${ID_A}/hosts /etc/hosts rw,relatime - ext4 /dev/vda1 rw
EOF
  assert_success
  assert_output "${ID_A}"

  # A docker-in-docker container: its own daemon's containers' shm mounts,
  # listed after its own /etc/hostname, are not it.
  run _reap_mountinfo_container_id << EOF
361 69 0:46 / / rw,relatime - overlay overlay rw
362 361 253:1 /var/lib/docker/containers/${ID_A}/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw
480 361 0:80 / /var/lib/docker/containers/${ID_B}/mounts/shm rw,nosuid,nodev,noexec,relatime shared:300 - tmpfs shm rw,size=65536k
EOF
  assert_success
  assert_output "${ID_A}"

  # The docker root on a filesystem of its own: the source is relative to it.
  run _reap_mountinfo_container_id << EOF
371 361 8:17 /containers/${ID_B}/hostname /etc/hostname rw,relatime - ext4 /dev/sdb1 rw
EOF
  assert_success
  assert_output "${ID_B}"
}

@test "another container's shareable /dev/shm, mounted on the host, is not this one" {
  # Docker mounts a shareable IPC container's shm under its directory on
  # the host (k8s pod sandboxes are), which a run on the host can see; in
  # a CI step it can't, and the answer is the step's own container.
  local other
  other="$(docker run -d --ipc shareable --label "${PREFIX}=owner" "${IMAGE}" sleep 600)"
  run reap_self_container
  assert_success
  refute_output "${other}"
  assert_output "$(_reap_mountinfo_container_id < /proc/self/mountinfo)"
}

@test "in a container on the daemon, as a CI step is, it finds its own id" {
  local id
  # The CI step's image, so its shape is what is tested; bash is all the
  # library needs that it lacks.
  id="$(docker create --label "${PREFIX}=owner" -v /var/run/docker.sock:/var/run/docker.sock docker:29-cli \
    sh -c 'apk add -q --no-cache bash > /dev/null && bash -c "source /reap_lib.sh && reap_self_container"' 2> /dev/null)"
  docker cp -q "${BATS_TEST_DIRNAME}/reap_lib.sh" "${id}:/reap_lib.sh"
  run docker start -a "${id}"
  assert_success
  assert_output "${id}"
}

@test "a project name carries its owner's short id, and no owner outside a container" {
  run reap_compose_project "${PREFIX}" "${LIVE_OWNER}"
  assert_success
  assert_output --regexp "^${PREFIX}-[0-9a-f]{8}-o${LIVE_OWNER:0:12}\$"

  run reap_compose_project "${PREFIX}" ""
  assert_success
  assert_output --regexp "^${PREFIX}-[0-9a-f]{8}\$"
}

@test "an owner that is stopped or removed has abandoned its stack; a running one has not" {
  run reap_is_abandoned "${LIVE_OWNER:0:12}" ""
  assert_failure
  run reap_is_abandoned "${DEAD_OWNER:0:12}" ""
  assert_success
  docker container rm "${DEAD_OWNER}" > /dev/null
  run reap_is_abandoned "${DEAD_OWNER:0:12}" ""
  assert_success
}

@test "an owner docker could not be asked about is not taken to be gone" {
  docker() {
    if [[ "$1 $2" == "container inspect" ]]; then
      echo "Cannot connect to the Docker daemon" >&2
      return 1
    fi
    command docker "$@"
  }
  run reap_is_abandoned "${DEAD_OWNER:0:12}" ""
  assert_failure
}

@test "no owner is abandoned only inside a CI step" {
  run reap_is_abandoned "" ""
  assert_failure
  run reap_is_abandoned "" "${LIVE_OWNER}"
  assert_success
}

@test "only the stacks of gone owners come down, outside a CI step" {
  local dead live unowned current
  dead="$(reap_compose_project "${PREFIX}" "${DEAD_OWNER}")"
  live="$(reap_compose_project "${PREFIX}" "${LIVE_OWNER}")"
  unowned="$(reap_compose_project "${PREFIX}" "")"
  current="$(reap_compose_project "${PREFIX}" "")"
  for project in "${dead}" "${live}" "${unowned}" "${current}"; do
    stack "${project}"
  done

  run reap_compose "${PREFIX}" "${current}" ""
  assert_success
  assert_output "--- taking down ${dead}, left by a run that never tidied up"

  assert_gone "${dead}"
  assert_kept "${live}"
  assert_kept "${unowned}"
  assert_kept "${current}"
}

@test "inside a CI step, unowned stacks come down too, but never the run's own" {
  local unowned current
  unowned="$(reap_compose_project "${PREFIX}" "")"
  current="$(reap_compose_project "${PREFIX}" "")"
  stack "${unowned}"
  stack "${current}"

  run reap_compose "${PREFIX}" "${current}" "${LIVE_OWNER}"
  assert_success

  assert_gone "${unowned}"
  assert_kept "${current}"
}

@test "a stack whose containers are already gone comes down, as does a lone built image" {
  local no_containers image_only
  no_containers="$(reap_compose_project "${PREFIX}" "${DEAD_OWNER}")"
  image_only="$(reap_compose_project "${PREFIX}" "${DEAD_OWNER}")"
  stack "${no_containers}"
  docker container ls -aq --filter "label=${REAP_COMPOSE_LABEL}=${no_containers}" | xargs docker container rm -f -v > /dev/null
  # As compose labels what it builds, for a run killed during its build.
  docker commit --change "LABEL ${REAP_COMPOSE_LABEL}=${image_only}" "${DEAD_OWNER}" "${image_only}-built" > /dev/null

  run reap_compose "${PREFIX}" "" ""
  assert_success

  rm "${BATS_TEST_TMPDIR}/${no_containers}.anon"
  assert_gone "${no_containers}"
  assert_gone "${image_only}"
}

@test "another prefix's stacks, or a name that only starts like one, are left alone" {
  local other lookalike
  other="$(unique_name "${PREFIX}x")-o${DEAD_OWNER:0:12}"
  lookalike="$(reap_compose_project "${PREFIX}" "${DEAD_OWNER}")-extra"
  stack "${other}"
  stack "${lookalike}"

  run reap_compose "${PREFIX}" "" "${LIVE_OWNER}"
  assert_success

  assert_kept "${other}"
  assert_kept "${lookalike}"
  # Not reap_compose's: its prefix is this one with an x after it.
  reap_compose_down "${other}"
}

@test "a real compose stack comes down whole: run containers, network, volume and built image" {
  local dir="${BATS_TEST_TMPDIR}/stack" project
  mkdir -p "${dir}"
  cat > "${dir}/compose.yaml" << EOF
services:
  built:
    build:
      dockerfile_inline: |
        FROM ${IMAGE}
        CMD ["sleep", "600"]
    volumes: [data:/data]
  runner:
    image: ${IMAGE}
    command: ["sleep", "600"]
    profiles: [run]
volumes:
  data: {}
EOF
  project="$(reap_compose_project "${PREFIX}" "${DEAD_OWNER}")"
  docker compose -p "${project}" -f "${dir}/compose.yaml" up -d --build --quiet-pull 2> /dev/null
  docker compose -p "${project}" -f "${dir}/compose.yaml" run -d runner > /dev/null 2>&1
  run left "${project}"
  # built's and runner's containers, the network, the volume, the image.
  assert_equal "${#lines[@]}" 5

  run reap_compose "${PREFIX}" "" ""
  assert_success

  assert_gone "${project}"
}
