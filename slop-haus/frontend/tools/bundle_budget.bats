# Tests for bundle_budget.mjs against a scratch manifest: the budget counts the
# entry's static imports, not its dynamic ones.
#
#   bats/run.sh slop-haus/frontend/tools   # from the repo root

CHECK="${BATS_TEST_DIRNAME}/bundle_budget.mjs"

setup() {
  bats_load_library repo_test
  bats_load_library bats-support
  bats_load_library bats-assert
  require_command node
  DIST="${BATS_TEST_TMPDIR}/dist"
  mkdir -p "${DIST}/.vite" "${DIST}/assets"
}

# chunk <name> <bytes of incompressible data>
chunk() {
  head -c "$2" /dev/urandom > "${DIST}/assets/$1.js"
}

@test "a lazy chunk is not part of the initial load" {
  chunk entry 1000
  chunk page 400000
  cat > "${DIST}/.vite/manifest.json" << 'JSON'
{"src/main.tsx":{"file":"assets/entry.js","isEntry":true,"dynamicImports":["src/page.tsx"]},
 "src/page.tsx":{"file":"assets/page.js","isDynamicEntry":true}}
JSON
  run node "${CHECK}" "${DIST}"
  assert_success
}

@test "a chunk the entry imports statically counts, even if it is not the entry file" {
  chunk entry 1000
  chunk page 400000
  cat > "${DIST}/.vite/manifest.json" << 'JSON'
{"src/main.tsx":{"file":"assets/entry.js","isEntry":true,"imports":["src/page.tsx"]},
 "src/page.tsx":{"file":"assets/page.js"}}
JSON
  run node "${CHECK}" "${DIST}"
  assert_failure
  assert_output --partial "FAIL"
}

@test "the entry file itself counts" {
  chunk entry 400000
  cat > "${DIST}/.vite/manifest.json" << 'JSON'
{"src/main.tsx":{"file":"assets/entry.js","isEntry":true}}
JSON
  run node "${CHECK}" "${DIST}"
  assert_failure
}

@test "a chunk reached through a static import of a static import counts" {
  chunk entry 1000
  chunk shared 1000
  chunk page 400000
  cat > "${DIST}/.vite/manifest.json" << 'JSON'
{"src/main.tsx":{"file":"assets/entry.js","isEntry":true,"imports":["src/shared.ts"]},
 "src/shared.ts":{"file":"assets/shared.js","imports":["src/page.tsx"]},
 "src/page.tsx":{"file":"assets/page.js"}}
JSON
  run node "${CHECK}" "${DIST}"
  assert_failure
}
