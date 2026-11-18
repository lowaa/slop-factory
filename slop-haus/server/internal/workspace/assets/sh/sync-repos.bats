# Tests for sync-repos.sh, which clones the manifest's missing repos.
#
# Nothing here touches the network: a manifest remote is host/owner/repo, and
# git's own url.<base>.insteadOf (in a per-test global config) points
# example.test at bare repos in $BATS_TEST_TMPDIR. The machine's own git
# config is never read.
#
#   bats/run.sh slop-haus/server/internal/workspace/assets/sh/sync-repos.bats

setup() {
  bats_load_library repo_test

  export HOME="${BATS_TEST_TMPDIR}/home"
  mkdir -p "${HOME}"
  export GIT_CONFIG_NOSYSTEM=1
  export GIT_CONFIG_GLOBAL="${BATS_TEST_TMPDIR}/gitconfig"
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.test
  export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.test
  unset SLOP_HAUS_CLONE SLOP_HAUS_CLONE_PROTOCOL

  REMOTES="${BATS_TEST_TMPDIR}/remotes"
  mkdir -p "${REMOTES}"
  git config --global url."file://${REMOTES}/".insteadOf "https://example.test/"
  git config --global url."file://${REMOTES}/".insteadOf "git@example.test:" --add
  git config --global init.defaultBranch main

  make_remote acme/app
  make_remote acme/deploy

  ROOT="${BATS_TEST_TMPDIR}/workspace"
  mkdir -p "${ROOT}/.slop-haus"
  cp "${BATS_TEST_DIRNAME}/sync-repos.sh" "${ROOT}/.slop-haus/sync-repos.sh"
  SYNC="${ROOT}/.slop-haus/sync-repos.sh"
}

# make_remote <owner/repo> -- a bare repo with one commit, at the path a
# clone of example.test/<owner/repo> is redirected to.
make_remote() {
  local name="$1" seed="${BATS_TEST_TMPDIR}/seed-${1//\//-}"
  mkdir -p "${REMOTES}/${name%/*}"
  git init -q "${seed}"
  echo "from ${name}" > "${seed}/README"
  git -C "${seed}" add README
  git -C "${seed}" commit -q -m seed
  git clone -q --bare "${seed}" "${REMOTES}/${name}.git"
}

# snapshot <dir> -- every path, mode and content hash under <dir>, including
# .git, so "left alone" means byte for byte.
snapshot() {
  (cd "$1" && find . -printf '%p %m %y %l\n' | sort && find . -type f -exec sha256sum {} + | sort)
}

@test "clones a missing repo, and only that" {
  run "${SYNC}" -- app example.test/acme/app
  assert_success
  assert_output --partial "cloning app"
  assert_equal "$(cat "${ROOT}/app/README")" "from acme/app"
  [ ! -e "${ROOT}/deploy" ]
}

@test "clones several, each into its own path" {
  run "${SYNC}" -- app example.test/acme/app deploy example.test/acme/deploy
  assert_success
  assert_equal "$(cat "${ROOT}/deploy/README")" "from acme/deploy"
  assert_equal "$(cat "${ROOT}/app/README")" "from acme/app"
}

@test "clones over ssh when asked" {
  SLOP_HAUS_CLONE_PROTOCOL=ssh run "${SYNC}" -- app example.test/acme/app
  assert_success
  assert_equal "$(git -C "${ROOT}/app" config --get remote.origin.url)" "git@example.test:acme/app.git"
}

@test "follows ssh when an existing manifest repo was cloned over ssh" {
  git clone -q "git@example.test:acme/deploy.git" "${ROOT}/deploy"
  run "${SYNC}" -- deploy example.test/acme/deploy app example.test/acme/app
  assert_success
  assert_equal "$(git -C "${ROOT}/app" config --get remote.origin.url)" "git@example.test:acme/app.git"
}

@test "a passed-in protocol wins over how an existing manifest repo was cloned" {
  git clone -q "git@example.test:acme/deploy.git" "${ROOT}/deploy"
  SLOP_HAUS_CLONE_PROTOCOL=https run "${SYNC}" -- deploy example.test/acme/deploy app example.test/acme/app
  assert_success
  assert_equal "$(git -C "${ROOT}/app" config --get remote.origin.url)" "https://example.test/acme/app.git"
}

@test "an unknown passed-in protocol is refused" {
  SLOP_HAUS_CLONE_PROTOCOL=ftp run "${SYNC}" -- app example.test/acme/app
  assert_failure 2
  [ ! -e "${ROOT}/app" ]
}

@test "an existing directory with the wrong origin is left byte for byte as it was" {
  git init -q "${ROOT}/app"
  git -C "${ROOT}/app" remote add origin https://example.test/someone/else.git
  echo "uncommitted work" > "${ROOT}/app/notes.txt"
  before=$(snapshot "${ROOT}")

  run "${SYNC}" -- app example.test/acme/app
  assert_success
  assert_output --partial "app already exists -- left alone"
  assert_equal "$(snapshot "${ROOT}")" "${before}"
  assert_equal "$(git -C "${ROOT}/app" config --get remote.origin.url)" "https://example.test/someone/else.git"
}

@test "an existing plain directory, file and dangling link are left alone" {
  mkdir "${ROOT}/app"
  echo keep > "${ROOT}/app/data"
  echo keep > "${ROOT}/deploy"
  ln -s "${BATS_TEST_TMPDIR}/nowhere" "${ROOT}/third"
  before=$(snapshot "${ROOT}")

  run "${SYNC}" -- app example.test/acme/app deploy example.test/acme/deploy third example.test/acme/app
  assert_success
  assert_equal "$(snapshot "${ROOT}")" "${before}"
}

@test "an empty directory is not cloned into either" {
  mkdir "${ROOT}/app"
  run "${SYNC}" -- app example.test/acme/app
  assert_success
  [ -z "$(ls -A "${ROOT}/app")" ]
}

@test "SLOP_HAUS_CLONE=no clones nothing and records the choice for later runs" {
  SLOP_HAUS_CLONE=no run "${SYNC}" -- app example.test/acme/app
  assert_success
  [ ! -e "${ROOT}/app" ]
  [ -e "${ROOT}/.slop-haus/no-clone" ]

  run "${SYNC}" -- app example.test/acme/app
  assert_success
  assert_output --partial "still reported and validated"
  [ ! -e "${ROOT}/app" ]
}

@test "deleting the opt-out marker turns cloning back on" {
  : > "${ROOT}/.slop-haus/no-clone"
  run "${SYNC}" -- app example.test/acme/app
  [ ! -e "${ROOT}/app" ]
  rm "${ROOT}/.slop-haus/no-clone"
  run "${SYNC}" -- app example.test/acme/app
  assert_success
  [ -d "${ROOT}/app/.git" ]
}

@test "a root that is not a git repo works: clones land, nothing tries git there" {
  run "${SYNC}" -- app example.test/acme/app deploy example.test/acme/deploy
  assert_success
  [ -d "${ROOT}/app/.git" ]
  [ -d "${ROOT}/deploy/.git" ]
  [ ! -e "${ROOT}/.git" ]
  [ ! -e "${ROOT}/.gitignore" ]
}

@test "hostile paths are skipped, and nothing is created outside the root" {
  canary="${BATS_TEST_TMPDIR}/canary"
  run "${SYNC}" -- \
    ../escaped example.test/acme/app \
    /tmp/slop-haus-abs example.test/acme/app \
    a/b example.test/acme/app \
    -x example.test/acme/app \
    .git example.test/acme/app \
    '$(touch '"${canary}"')' example.test/acme/app \
    '' example.test/acme/app \
    . example.test/acme/app
  assert_failure 1
  [ ! -e "${canary}" ]
  [ ! -e "${BATS_TEST_TMPDIR}/escaped" ]
  [ ! -e /tmp/slop-haus-abs ]
  [ ! -e "${ROOT}/a" ]
  [ ! -e "${ROOT}/-x" ]
  [ ! -e "${ROOT}/.git" ]
  assert_equal "$(ls -A "${ROOT}")" ".slop-haus"
}

@test "hostile remotes are skipped, and no option reaches git or ssh" {
  canary="${BATS_TEST_TMPDIR}/canary"
  for remote in \
    '-oProxyCommand=touch '"${canary}"'/a/b' \
    '--upload-pack=touch '"${canary}" \
    'example.test/--upload-pack=x/y' \
    'example.test/../../etc' \
    'example.test/a/b;touch '"${canary}" \
    'example.test/$(touch '"${canary}"')/b' \
    'ext::sh -c touch '"${canary}"'/a/b' \
    'example.test' \
    ''; do
    SLOP_HAUS_CLONE_PROTOCOL=ssh run "${SYNC}" -- app "${remote}"
    assert_failure 1
    # Refused by the script's own check, not merely failed by git.
    assert_output --partial "skipping app: its remote is not a host/owner/repo name"
    [ ! -e "${ROOT}/app" ]
    [ ! -e "${canary}" ]
  done
}

@test "a failing clone does not stop the rest, and says which failed" {
  run "${SYNC}" -- missing example.test/acme/nope app example.test/acme/app
  assert_failure 1
  assert_output --partial "could not clone missing"
  [ ! -e "${ROOT}/missing" ]
  [ -z "$(find "${ROOT}/.slop-haus" -name 'clone.*')" ]
  [ -d "${ROOT}/app/.git" ]
}

@test "an invalid entry does not stop the valid ones after it" {
  run "${SYNC}" -- ../x example.test/acme/app app example.test/acme/app
  assert_failure 1
  [ -d "${ROOT}/app/.git" ]
}

@test "never prompts for credentials" {
  # git would block on a prompt here; with prompting off it fails fast.
  mkdir -p "${BATS_TEST_TMPDIR}/bin"
  cat > "${BATS_TEST_TMPDIR}/bin/git" << STUB
#!/usr/bin/env bash
if [ "\$1" = clone ]; then
  echo "prompt=\${GIT_TERMINAL_PROMPT:-unset}" > "${BATS_TEST_TMPDIR}/clone.env"
  exit 1
fi
exec $(command -v git) "\$@"
STUB
  chmod +x "${BATS_TEST_TMPDIR}/bin/git"
  PATH="${BATS_TEST_TMPDIR}/bin:${PATH}" run "${SYNC}" -- app example.test/acme/app
  assert_equal "$(cat "${BATS_TEST_TMPDIR}/clone.env")" "prompt=0"
}

@test "does not print the origin of an existing repo" {
  git init -q "${ROOT}/app"
  git -C "${ROOT}/app" remote add origin https://user:s3cret@example.test/acme/app.git
  run "${SYNC}" -- app example.test/acme/app deploy example.test/acme/deploy
  assert_success
  refute_output --partial s3cret
}

@test "an odd number of manifest arguments is refused before anything happens" {
  run "${SYNC}" -- app
  assert_failure 2
  [ ! -e "${ROOT}/app" ]
}

@test "no manifest is not an error" {
  run "${SYNC}" --
  assert_success
  assert_output ""
}

@test "a clone killed halfway leaves nothing at the real path, so the next run retries" {
  # A git whose clone writes a partial repo where it is told to, then dies.
  mkdir -p "${BATS_TEST_TMPDIR}/bin"
  cat > "${BATS_TEST_TMPDIR}/bin/git" << STUB
#!/usr/bin/env bash
if [ "\$1" = clone ]; then
  mkdir -p "\${@: -1}/.git"
  kill -9 \$\$
fi
exec $(command -v git) "\$@"
STUB
  chmod +x "${BATS_TEST_TMPDIR}/bin/git"
  PATH="${BATS_TEST_TMPDIR}/bin:${PATH}" run "${SYNC}" -- app example.test/acme/app
  assert_failure
  [ ! -e "${ROOT}/app" ]

  run "${SYNC}" -- app example.test/acme/app
  assert_success
  [ -d "${ROOT}/app/.git" ]
}

@test "a clone is never put over something that appeared while it ran" {
  mkdir -p "${BATS_TEST_TMPDIR}/bin"
  cat > "${BATS_TEST_TMPDIR}/bin/git" << STUB
#!/usr/bin/env bash
if [ "\$1" = clone ]; then
  $(command -v git) "\$@" && mkdir "${ROOT}/app" && echo mine > "${ROOT}/app/data"
  exit \$?
fi
exec $(command -v git) "\$@"
STUB
  chmod +x "${BATS_TEST_TMPDIR}/bin/git"
  PATH="${BATS_TEST_TMPDIR}/bin:${PATH}" run "${SYNC}" -- app example.test/acme/app
  assert_failure
  assert_equal "$(ls -A "${ROOT}/app")" "data"
}
