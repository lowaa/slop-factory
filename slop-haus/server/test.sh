#!/usr/bin/env bash
# Runs the Go test suite inside a container. The tests start their own
# Postgres containers via testcontainers-go (see internal/db/dbtest), so
# this container needs the host's Docker socket mounted
# (Docker-outside-of-Docker) rather than Docker installed inside the image,
# and --network host so it can reach those sibling containers' mapped ports
# the same way a host-run `go test` would.
#
# Two modes:
#   ./test.sh [go test args]   fully containerized (the default, ATTACH=0):
#                              the source is baked into the Dockerfile's test
#                              stage, so this runs anywhere a Docker daemon
#                              is -- including a CI step, where the daemon
#                              can't see the step's checkout to mount it
#   SUITE=go|workspace|supervisor|lint ./test.sh
#                              one of the four suites (default: all four:
#                              see run_all)
#   ATTACH=1 ./test.sh         a shell in the base stage with the repo
#                              mounted at /srv, for iterating: edit on the
#                              host, run `go test ./...` in the shell
#
# The repo root (not just this directory) is the build context and, in
# ATTACH=1, the mount: slop-haus/server depends on github.com/lowaa/slop-factory/go-db and
# github.com/lowaa/slop-factory/go-auth via go.work, not a tagged release (same reason
# ../Dockerfile's build context is the repo root)..
set -euo pipefail
# Inline rather than dev-tools/require_bash.sh: test_sh.bats runs this in an
# image without dev-tools/. bash 3.2 (macOS's /bin/bash) treats the empty
# arrays below as unbound under set -u.
if ((BASH_VERSINFO[0] < 5)); then
  echo "test.sh: needs bash >= 5 (this is ${BASH_VERSION}); on macOS \`brew install bash\` and make sure it is first on PATH" >&2
  exit 1
fi

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

# The image carries no downloaded modules (see the Dockerfile); named volumes
# keep them across runs and go.mod changes instead of one image layer each.
GO_CACHE_VOLUMES=(-v go-mod-cache:/go/pkg/mod -v go-build-cache:/root/.cache/go-build)

if [[ "${ATTACH:-0}" == "1" ]]; then
  # Modules and build cache come from the shared volumes, under the live source. The real go.work
  # comes with the mount, in place of the base stage's generated one.
  docker build --target base -t slop-haus-server-test-base -f slop-haus/server/Dockerfile .
  exec docker run --rm -it \
    --network host \
    -v "$(pwd)":/srv \
    -v /var/run/docker.sock:/var/run/docker.sock \
    "${GO_CACHE_VOLUMES[@]}" \
    -w /srv/slop-haus/server \
    slop-haus-server-test-base \
    bash
fi

# Run by the ID this build produced, not a tag: a concurrent run on the same
# daemon (two CI workflows on one agent) would retag a shared name between
# the build and the run, and this run would test the other's source.
# One file per suite, since run_all builds them concurrently.
WORK="$(mktemp -d)"
# Every suite container this run starts carries the label, so cleanup can
# remove them: a docker client killed with this script leaves its container
# running on the daemon, and --rm only acts once it has exited.
RUN_LABEL="slophaus-server-test=${WORK##*/}"
cleanup() {
  docker ps -q --filter "label=${RUN_LABEL}" | xargs -r docker rm -f > /dev/null 2>&1 || true
  rm -rf "${WORK}"
}
trap cleanup EXIT
# EXIT traps don't run on an untrapped signal; these send it through cleanup.
trap 'exit 130' INT
trap 'exit 143' TERM
# Extra docker run options for the workspace and supervisor suites (run_all).
SIDE_RUN_ARGS=()

run_go() {
  # In a CI step, Ryuk is off and the Postgres containers carry a reap label
  # instead (dbtest.reapOption): on the shared agents Ryuk has been seen
  # removing the container mid-run, while a step killed outright
  # leaves it running, which dev-tools/reap cleans up by that label.
  # Locally Ryuk stays on, as the only thing that tidies a killed run.
  local reap_env=(-e RYUK_VERBOSE=true)
  local self project
  # shellcheck source=SCRIPTDIR/../../dev-tools/reap/reap_lib.sh
  source dev-tools/reap/reap_lib.sh
  self="$(reap_self_container)"
  if [[ -n "${self}" ]]; then
    project="$(reap_compose_project slophaus-dbtest "${self}")"
    reap_compose slophaus-dbtest "${project}" "${self}"
    reap_env+=(-e TESTCONTAINERS_RYUK_DISABLED=true -e "DBTEST_REAP_PROJECT=${project}")
  fi

  # -timeout: go test's 10m default is per package binary. cmd/api takes ~2min
  # on a quiet machine but hit 10m on a loaded shared CI agent, where
  # the panic named whichever test happened to be running, not a hung one.
  # 20m keeps real hangs failing while leaving headroom for slow agents.
  # The landing soak's knobs (SOAK_SEED, SOAK_STEPS), when set: the nightly
  # soak pipeline runs a longer pass through here.
  local soak_env=() v
  for v in SOAK_SEED SOAK_STEPS; do
    [[ -z "${!v:-}" ]] || soak_env+=(-e "${v}=${!v}")
  done

  docker build --target test --iidfile "${WORK}/go.iid" -f slop-haus/server/Dockerfile .
  IMAGE="$(cat "${WORK}/go.iid")"

  # RYUK_VERBOSE: Ryuk logs which containers it removes only when verbose, and
  # dbtest prints that log when its Postgres container vanishes.
  docker run --rm --label "${RUN_LABEL}" \
    --network host \
    -v /var/run/docker.sock:/var/run/docker.sock \
    "${GO_CACHE_VOLUMES[@]}" \
    "${reap_env[@]}" \
    "${soak_env[@]}" \
    "${IMAGE}" \
    go test -timeout 20m ./... "$@"
}

run_workspace() {
  # The bats, node and Python suites for the scripts the server hands agents
  # (update.sh, token.sh, the pi extension, usage-report.py), and this
  # script's own (test_sh.bats), in the
  # Dockerfile's workspace-test stage: a Debian userland like the workspaces
  # they run in, not whatever this machine or a CI step has. By image ID,
  # like the above.
  docker build --target workspace-test --iidfile "${WORK}/workspace.iid" -f slop-haus/server/Dockerfile .
  docker run --rm --label "${RUN_LABEL}" "${SIDE_RUN_ARGS[@]}" "$(cat "${WORK}/workspace.iid")"
}

run_supervisor() {
  # The supervisor package's suite (the Remote Control host's tests among them),
  # in the Dockerfile's supervisor-test stage.
  docker build --target supervisor-test --iidfile "${WORK}/supervisor.iid" -f slop-haus/server/Dockerfile .
  docker run --rm --label "${RUN_LABEL}" "${SIDE_RUN_ARGS[@]}" "$(cat "${WORK}/supervisor.iid")"
}

run_lint() {
  # The Go linters and the deadcode check, and ruff over the workspace
  # scripts' Python (dev-tools/lint/README.md), on the host: they need only
  # Go and uv, not the database. Niced, since in run_all they share the
  # machine with the Go suite.
  nice -n 10 "${LINT_GO_SH:-dev-tools/lint/go.sh}" slop-haus/server
  nice -n 10 "${LINT_RUFF_SH:-dev-tools/lint/ruff.sh}" slop-haus/server
}

run_all() {
  # The workspace and supervisor suites (~1 core each, 3-5 min together), and
  # the lint (on the host, niced), run alongside the Go suite, not after it:
  # the job takes the slowest suite, not the sum. The two in containers run
  # at a low CPU weight, so on a busy agent they take what
  # the Go suite leaves idle rather than slowing it -- as separate CI steps at
  # equal weight they did (7422e844c). The weight is the containers', since
  # the work runs on the daemon, out of reach of a nice on this script.
  # Their output goes to a file each, printed whole once all are done,
  # so the log reads one suite at a time; the summary comes last, where
  # the tail of a CI log shows it. A failed side suite's last lines
  # are repeated just before the summary.
  local side=(workspace supervisor lint) pids=() suite rc status=0 i results=() failed=()
  SIDE_RUN_ARGS=(--cpu-shares=128)
  for suite in "${side[@]}"; do
    "run_${suite}" > "${WORK}/${suite}.log" 2>&1 &
    pids+=($!)
  done
  # Go streams to the log as it runs. In the background like the others, so
  # set -e still holds inside it (it doesn't in an `||` list).
  run_go "$@" &
  pids+=($!)

  rc=0
  wait "${pids[${#side[@]}]}" || rc=$?
  results+=("go: $(verdict "${rc}")")
  ((rc == 0)) || status=1
  for i in "${!side[@]}"; do
    suite=${side[i]}
    rc=0
    wait "${pids[i]}" || rc=$?
    echo "== ${suite} (exit ${rc})"
    cat "${WORK}/${suite}.log"
    results+=("${suite}: $(verdict "${rc}")")
    ((rc == 0)) || {
      status=1
      failed+=("${suite}")
    }
  done
  # CI shows only the log's last 200 lines, which the full logs above push
  # out of view: repeat a failed side suite's tail just before the summary.
  for suite in "${failed[@]}"; do
    echo "== ${suite} failed: last 100 lines"
    tail -n 100 "${WORK}/${suite}.log"
  done
  printf '== %s\n' "$(printf '%s, ' "${results[@]}" | sed 's/, $//')"
  return "${status}"
}

verdict() {
  if (($1 == 0)); then echo ok; else echo "FAIL (exit $1)"; fi
}

case "${SUITE:-all}" in
  go) run_go "$@" ;;
  workspace) run_workspace ;;
  supervisor) run_supervisor ;;
  lint) run_lint ;;
  all) run_all "$@" ;;
  *)
    echo "unknown SUITE '${SUITE}' (go, workspace, supervisor, lint, all)" >&2
    exit 2
    ;;
esac
