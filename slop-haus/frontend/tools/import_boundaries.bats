# Tests for import_boundaries.mjs against a scratch src tree: one negative
# run per rule (a violating import must fail the check), plus the ratchet.
#
#   bats/run.sh slop-haus/frontend/tools   # from the repo root

CHECK="${BATS_TEST_DIRNAME}/import_boundaries.mjs"

setup() {
  bats_load_library repo_test
  bats_load_library bats-support
  bats_load_library bats-assert
  require_command node
  export IMPORT_BOUNDARIES_SRC="${BATS_TEST_TMPDIR}/src"
  export IMPORT_BOUNDARIES_BASELINE="${BATS_TEST_TMPDIR}/baseline.json"
  mkdir -p "${IMPORT_BOUNDARIES_SRC}"
}

src() { # src <path under src/> <content>
  mkdir -p "$(dirname "${IMPORT_BOUNDARIES_SRC}/$1")"
  printf '%s\n' "$2" > "${IMPORT_BOUNDARIES_SRC}/$1"
}

check() { node "${CHECK}" "$@"; }

@test "a clean tree passes with no baseline" {
  src components/a/index.ts 'export const a = 1;'
  src components/b/b.ts 'import { a } from "@/components/a";'
  run check
  assert_success
}

@test "lib importing a component fails" {
  src components/a/a.ts 'export const a = 1;'
  src components/a/index.ts 'export * from "./a";'
  src lib/x.ts 'import { a } from "@/components/a";'
  run check
  assert_failure
  assert_output --partial "[lib-no-components] lib/x.ts -> components/a/index.ts"
}

@test "context, api, test, shared and domain importing a component fail" {
  src components/a/index.ts 'export const a = 1;'
  src context/c.ts 'import { a } from "../components/a";'
  src api/d.ts 'import { a } from "@/components/a";'
  src test/e.ts 'import { a } from "@/components/a";'
  src shared/f.ts 'import { a } from "@/components/a";'
  src domain/g.ts 'import { a } from "@/components/a";'
  run check
  assert_failure
  assert_output --partial "[lib-no-components] context/c.ts"
  assert_output --partial "[lib-no-components] api/d.ts"
  assert_output --partial "[lib-no-components] test/e.ts"
  assert_output --partial "[lib-no-components] shared/f.ts"
  assert_output --partial "[lib-no-components] domain/g.ts"
}

@test "a deep import into another component folder fails" {
  src components/a/inner.tsx 'export const a = 1;'
  src components/b/b.tsx 'import { a } from "../a/inner";'
  run check
  assert_failure
  assert_output --partial "[component-public-api] components/b/b.tsx -> components/a/inner.tsx"
}

@test "imports within a folder, and through index.ts, pass" {
  src components/a/inner.ts 'export const a = 1;'
  src components/a/a.ts 'import { a } from "./inner";'
  src components/a/index.ts 'export { a } from "./inner";'
  src components/b/b.ts 'import { a } from "@/components/a/index";'
  run check
  assert_success
}

@test "a cycle between component folders fails" {
  src components/a/index.ts 'import "@/components/b";'
  src components/b/index.ts 'import "@/components/a";'
  run check
  assert_failure
  assert_output --partial "[no-cycle]"
  assert_output --partial "a <-> b"
}

@test "a file import cycle fails" {
  src lib/x.ts 'import "./y";'
  src lib/y.ts 'import "./x";'
  run check
  assert_failure
  assert_output --partial "[no-cycle] lib/x.ts -> lib/x.ts, lib/y.ts"
}

@test "a three-file cycle fails as one group" {
  src lib/x.tsx 'import "./y";'
  src lib/y.tsx 'import "./z";'
  src lib/z.tsx 'import "./x";'
  run check
  assert_failure
  assert_output --partial "[no-cycle] lib/x.tsx -> lib/x.tsx, lib/y.tsx, lib/z.tsx"
}

@test "a file importing itself fails" {
  src lib/x.ts 'import "./x";'
  run check
  assert_failure
  assert_output --partial "[no-cycle] lib/x.ts -> lib/x.ts"
}

@test "a baselined violation passes, a new one fails" {
  src components/a/inner.ts 'export const a = 1;'
  src components/b/b.ts 'import { a } from "../a/inner";'
  check --update
  run check
  assert_success
  src components/c/c.ts 'import { a } from "../a/inner";'
  run check
  assert_failure
  assert_output --partial "components/c/c.ts"
}

@test "a fixed violation still in the baseline fails" {
  src components/a/inner.ts 'export const a = 1;'
  src components/b/b.ts 'import { a } from "../a/inner";'
  check --update
  src components/b/b.ts 'export const b = 1;'
  run check
  assert_failure
  assert_output --partial "Baseline lists violations that are fixed"
}

@test "an inline ApiError message ternary outside lib fails" {
  src components/a/a.ts 'export const m = (err: unknown) => (err instanceof ApiError ? err.message : "Failed.");'
  run check
  assert_failure
  assert_output --partial "[use-error-message] components/a/a.ts -> line 1"
}

@test "an ApiError check that is not the message idiom passes, as does lib" {
  src components/a/a.ts 'export const m = (err: unknown) => (err instanceof ApiError ? err.status : 0);'
  src lib/use_async_action.ts 'export const m = (err: unknown) => (err instanceof ApiError ? err.message : "x");'
  run check
  assert_success
}
