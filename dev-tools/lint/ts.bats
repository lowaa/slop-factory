# Tests for the TypeScript side: oxlint.sh against a scratch area with the
# shared config, and findings.mjs's adapters for knip and jscpd reports.
#
#   bats/run.sh dev-tools/lint/ts.bats   # from the repo root

LINT_DIR="${BATS_TEST_DIRNAME}"

setup() {
  bats_load_library repo_test
  require_command node
  AREA="${BATS_TEST_TMPDIR}/area"
  mkdir -p "${AREA}/pkg/src"
  printf '{ "extends": ["%s/oxlintrc.base.json"] }\n' "${LINT_DIR}" > "${AREA}/pkg/.oxlintrc.json"
  printf '{ "compilerOptions": { "strict": true, "target": "ES2022", "module": "ESNext", "moduleResolution": "bundler" }, "include": ["src"] }\n' \
    > "${AREA}/pkg/tsconfig.json"
  ts pkg/src/a.ts 'export async function load(): Promise<number> {
  return 1;
}'
}

ts() { # ts <path under the area> <content>
  mkdir -p "$(dirname "${AREA}/$1")"
  printf '%s\n' "$2" > "${AREA}/$1"
}

findings() { printf '%s' "$2" | node "${LINT_DIR}/findings.mjs" "$1"; }

@test "oxlint: a clean area passes, and a type-aware finding fails" {
  run "${LINT_DIR}/oxlint.sh" "${AREA}"
  assert_success
  ts pkg/src/b.ts 'import { load } from "./a";

export function start(): void {
  load();
}'
  run "${LINT_DIR}/oxlint.sh" "${AREA}"
  assert_failure
  assert_output --partial "  pkg/src/b.ts typescript(no-floating-promises)"
}

@test "oxlint: a baselined finding passes, and is stale once fixed" {
  ts pkg/src/b.ts 'import { load } from "./a";

export function start(): void {
  load();
}'
  run "${LINT_DIR}/oxlint.sh" --rebaseline "${AREA}"
  assert_success
  run "${LINT_DIR}/oxlint.sh" "${AREA}"
  assert_success
  ts pkg/src/b.ts 'import { load } from "./a";

export function start(): void {
  void load();
}'
  run "${LINT_DIR}/oxlint.sh" "${AREA}"
  assert_failure
  assert_output --partial "no longer found"
}

@test "oxlint: the complexity rules are on" {
  ts pkg/src/c.ts 'export function deep(a: number, b: number, c: number, d: number, e: number): number {
  if (a) { if (b) { if (c) { if (d) { if (e) { return 1; } } } } }
  return 0;
}'
  run "${LINT_DIR}/oxlint.sh" "${AREA}"
  assert_failure
  assert_output --partial "pkg/src/c.ts eslint(max-depth)"
  assert_output --partial "pkg/src/c.ts eslint(max-params)"
}

@test "oxlint: complexity, function length and misused promises are on; tests skip the length rule" {
  local body="" i
  for i in $(seq 82); do body+="  n += ${i};"$'\n'; done
  ts pkg/src/d.ts "export function long(n: number): number {
${body}  return n;
}"
  ts pkg/src/d.test.ts "export function longInTest(n: number): number {
${body}  return n;
}"
  ts pkg/src/e.ts 'import { load } from "./a";

export function branchy(a: number): number {
  let n = 0;
  if (a === 1) n = 1; else if (a === 2) n = 2; else if (a === 3) n = 3; else if (a === 4) n = 4;
  else if (a === 5) n = 5; else if (a === 6) n = 6; else if (a === 7) n = 7; else if (a === 8) n = 8;
  else if (a === 9) n = 9; else if (a === 10) n = 10; else if (a === 11) n = 11; else if (a === 12) n = 12;
  else if (a === 13) n = 13; else if (a === 14) n = 14; else if (a === 15) n = 15;
  return n;
}

export function misused(): void {
  if (load()) return;
}'
  run "${LINT_DIR}/oxlint.sh" --rebaseline "${AREA}"
  assert_success
  run cat "${AREA}/lint_baseline/oxlint.txt"
  assert_line "pkg/src/d.ts eslint(max-lines-per-function)"
  refute_line "pkg/src/d.test.ts eslint(max-lines-per-function)"
  assert_line "pkg/src/e.ts eslint(complexity)"
  assert_line "pkg/src/e.ts typescript(no-misused-promises)"
}

@test "oxlint: a broken config fails as a broken run" {
  printf '{ "rules": { "no-such-rule": "error" } }\n' > "${AREA}/pkg/.oxlintrc.json"
  run "${LINT_DIR}/oxlint.sh" --rebaseline "${AREA}"
  assert_failure
  assert_output --partial "did not produce a JSON report"
  [ ! -e "${AREA}/lint_baseline/oxlint.txt" ]
}

@test "findings: knip issues become one line per name" {
  run findings knip '{"issues":[{"file":"a/b.ts","exports":[{"name":"x","line":1},{"name":"y"}],"types":[],"dependencies":[{"name":"left pad"}],"duplicates":[[{"name":"A"},{"name":"B"}]]}]}'
  assert_success
  assert_output $'a/b.ts exports x\na/b.ts exports y\na/b.ts dependencies left_pad\na/b.ts duplicates A,B'
}

@test "findings: a jscpd clone is its file pair, sorted" {
  run findings jscpd '{"duplicates":[{"firstFile":{"name":"z.go"},"secondFile":{"name":"a.go"}},{"firstFile":{"name":"a.go"},"secondFile":{"name":"a.go"}}]}'
  assert_success
  assert_output $'a.go clone z.go\na.go clone a.go'
}

@test "findings: a report of the wrong shape, or not JSON, fails" {
  run findings knip '{"duplicates":[]}'
  assert_failure
  run findings jscpd 'Error: something broke'
  assert_failure
  assert_output --partial "did not produce a JSON report"
  run findings oxlint '{"diagnostics":[{"code":"x"}]}'
  assert_failure
  run findings bogus '{}'
  assert_failure 2
}
