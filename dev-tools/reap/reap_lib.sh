# shellcheck shell=bash
# Tidies up after test runs that were killed before they could tidy up
# after themselves. A run takes its throwaway docker resources (a k3d
# cluster, a compose stack) down from an EXIT trap, but a CI step killed
# outright -- CI cancelling a superseded run -- never runs it,
# and what it left keeps running on the agent: the daily prune can't
# remove a running container, so they pile up until the agent slows down.
# Sourced, not run:
#
#   source dev-tools/reap/reap_lib.sh
#   reap_self_container               this CI step's container id, if any
#   reap_is_abandoned <owner> <self>  whether what <owner> made may go
#   reap_compose_project <prefix> <self>
#                                     a compose project name for a new run,
#                                     carrying its owner
#   reap_compose <prefix> <current> <self>
#                                     the abandoned <prefix> projects, down
#   reap_compose_down <project>       one project, down (a run's own teardown)
#
# Not only for compose: anything a run makes (a network, containers started
# with `docker run`, an image it built) is part of a project once it carries
# the label ${REAP_COMPOSE_LABEL}=<project>, so a bats suite that drives docker
# directly gets the same reaping by labelling what it creates.
#
# The owner of what a run makes is the CI step container it ran in: gone,
# and what it made is abandoned. What has no owner was made outside a
# container -- a developer's run, perhaps still going -- or by a version
# from before owners were recorded. Only inside a CI step, where every run
# now records its owner, is it taken to be the latter.
#
# Nothing here is named CI_*: the CI system fills that namespace in every step.

# reap_self_container -- this process's container id, if it runs in a
# container on the same daemon docker talks to (a CI step with the host's
# socket mounted); nothing otherwise. ${REAP_SELF_CONTAINER} names the
# container instead, so a test can run a suite as if from a CI step that has
# since gone.
reap_self_container() {
  local id="${REAP_SELF_CONTAINER-}"
  if [[ -z "${id}" ]]; then
    # stderr first, so a machine without /proc (macOS) says nothing.
    id="$(_reap_mountinfo_container_id 2> /dev/null < /proc/self/mountinfo)" || true
  fi
  if [[ -n "${id}" ]] && docker container inspect "${id}" > /dev/null 2>&1; then
    echo "${id}"
  fi
}

# _reap_mountinfo_container_id < mountinfo -- the id of the container whose
# hostname file is bind-mounted on /etc/hostname, as docker does in every
# container it runs. Only that mount will do: a docker host's own mountinfo
# lists the directory of every shareable-IPC container (their /dev/shm
# mounts; k8s pod sandboxes are shareable), the cgroup is no help since
# cgroup v2 namespaces it to "/", and --hostname overrides the hostname.
# A container run with --network container:<other> gets <other>'s hostname
# file, so it is taken for <other> -- whose network it is on.
_reap_mountinfo_container_id() {
  local root mountpoint
  # mountinfo: id parent major:minor root mountpoint ...
  while read -r _ _ _ root mountpoint _; do
    if [[ "${mountpoint}" == /etc/hostname && "${root}" =~ /containers/([0-9a-f]{64})/hostname$ ]]; then
      echo "${BASH_REMATCH[1]}"
      return
    fi
  done
}

# reap_is_abandoned <owner> <self> -- succeeds when what <owner> made may be
# removed: <owner> is stopped or gone, or there is no owner and <self>
# (reap_self_container's answer) says this is a CI step.
reap_is_abandoned() {
  local owner="$1" self="$2" out
  if [[ -z "${owner}" ]]; then
    [[ -n "${self}" ]]
    return
  fi
  if out="$(docker container inspect -f '{{.State.Running}}' "${owner}" 2>&1)"; then
    [[ "${out}" != true ]]
    return
  fi
  # Gone only when docker says so: a daemon that failed to answer says
  # nothing about the owner, and taking its stack down would kill a run.
  [[ "${out}" == *"No such container"* ]]
}

# The project label compose puts on everything it makes, built images
# included -- so a project can be taken down without its compose files.
REAP_COMPOSE_LABEL="com.docker.compose.project"

# reap_compose_project <prefix> <self> -- <prefix>-<random>, and -o<owner>
# after it when <self> is set. Random rather than $$: two CI steps, each in
# its own container, can easily share a PID. The owner goes in the name
# because compose has no label for a whole project, and a name is on
# every container, network and volume from the moment each is made. Twelve
# hex digits, as docker abbreviates ids: unambiguous in practice, and short
# enough for the names compose builds from it.
reap_compose_project() {
  local prefix="$1" self="$2" name
  name="${prefix}-$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')"
  if [[ -n "${self}" ]]; then
    name+="-o${self:0:12}"
  fi
  echo "${name}"
}

# reap_compose <prefix> <current> <self> -- takes down every compose project
# reap_compose_project named for <prefix>, except <current> (this run's own),
# whose owner reap_is_abandoned says is gone.
reap_compose() {
  local prefix="$1" current="$2" self="$3" project
  while read -r project; do
    [[ "${project}" != "${current}" ]] || continue
    [[ "${project}" =~ ^"${prefix}"-[0-9a-f]{8}(-o([0-9a-f]{12}))?$ ]] || continue
    reap_is_abandoned "${BASH_REMATCH[2]}" "${self}" || continue
    echo "--- taking down ${project}, left by a run that never tidied up"
    reap_compose_down "${project}"
  done < <(_reap_compose_projects)
}

# Every project with a container, network, volume or built image on the
# daemon: a run killed mid-way -- during its build, say -- may have left
# any of them without the others.
_reap_compose_projects() {
  local filter="label=${REAP_COMPOSE_LABEL}" format="{{.Label \"${REAP_COMPOSE_LABEL}\"}}"
  {
    docker container ls -a --filter "${filter}" --format "${format}"
    docker network ls --filter "${filter}" --format "${format}"
    docker volume ls --filter "${filter}" --format "${format}"
    # image ls has no .Label to format with.
    docker image ls -q --filter "${filter}" |
      xargs -r docker image inspect -f "{{index .Config.Labels \"${REAP_COMPOSE_LABEL}\"}}" 2> /dev/null
  } | sort -u
}

# reap_compose_down <project> -- everything carrying the project's label.
# By label, not `docker compose -p <project> down`: without the compose
# files, down leaves `compose run` containers (and so the network they are
# on) and the images it built, which it only finds through containers.
# Containers first, since nothing else can go while one uses it.
reap_compose_down() {
  local filter="label=${REAP_COMPOSE_LABEL}=$1"
  # -v: the anonymous volumes, which carry no project label of their own.
  docker container ls -aq --filter "${filter}" | xargs -r docker container rm -f -v > /dev/null 2>&1 || true
  local kind
  for kind in network volume image; do
    docker "${kind}" ls -q --filter "${filter}" | xargs -r docker "${kind}" rm > /dev/null 2>&1 || true
  done
}
