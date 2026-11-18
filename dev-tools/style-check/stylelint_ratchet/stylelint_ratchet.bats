# Tests for ratchet.mjs and the repo's stylelint.config.mjs, against a scratch
# tree laid out like the real apps (STYLELINT_RATCHET_ROOT).
#
#   bats/run.sh dev-tools/style-check/stylelint_ratchet   # from the repo root

RATCHET="${BATS_TEST_DIRNAME}/ratchet.mjs"

setup() {
  bats_load_library repo_test
  bats_load_library bats-support
  bats_load_library bats-assert
  require_command node
  export STYLELINT_RATCHET_ROOT="${BATS_TEST_TMPDIR}/repo"
  AREA="${STYLELINT_RATCHET_ROOT}/app"
  mkdir -p "${AREA}/src"
  scratch_tree "${STYLELINT_RATCHET_ROOT}"
}

scratch_tree() {
  local repo_root
  repo_root="$(cd "${BATS_TEST_DIRNAME}/../../.." && pwd)"
  mkdir -p "$1"
  ln -s "${repo_root}/node_modules" "$1/node_modules"
  cp "${repo_root}/stylelint.config.mjs" "$1/"
}

scss() { # scss <path under app/> <content>
  mkdir -p "$(dirname "${AREA}/$1")"
  printf '%s\n' "$2" > "${AREA}/$1"
}

ratchet() { node "${RATCHET}" "$@"; }

@test "a clean area with no baseline passes" {
  scss src/a.scss '.a {
  color: var(--color-text);
}'
  run ratchet "${AREA}"
  assert_success
}

@test "a new violation fails and names the file and rule" {
  scss src/a.scss '.a {
  color: #fff;
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "src/a.scss  color-no-hex  baseline 0, now 1"
}

@test "violations matching the baseline pass" {
  scss src/a.scss '.a {
  color: #fff;
}'
  ratchet --update "${AREA}"
  run ratchet "${AREA}"
  assert_success
}

@test "one more violation than the baseline fails" {
  scss src/a.scss '.a {
  color: #fff;
}'
  ratchet --update "${AREA}"
  scss src/a.scss '.a {
  color: #fff;
  background: #000;
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "baseline 1, now 2"
}

@test "a fixed violation with an unlowered baseline fails" {
  scss src/a.scss '.a {
  color: #fff;
  background: #000;
}'
  ratchet --update "${AREA}"
  scss src/a.scss '.a {
  color: #fff;
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "Baseline is higher than the real count"
  assert_output --partial "baseline 2, now 1"
}

@test "a baseline entry for a file with no violations left fails" {
  scss src/a.scss '.a {
  color: #fff;
}'
  ratchet --update "${AREA}"
  scss src/a.scss '.a {
  color: var(--color-text);
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "baseline 1, now 0"
}

@test "update writes a sorted baseline with paths relative to the area" {
  scss src/b.scss '.b {
  color: #fff;
}'
  scss src/a.scss '.a {
  color: #fff;
}'
  ratchet --update "${AREA}"
  run node -e 'const b=JSON.parse(require("fs").readFileSync(process.argv[1]));console.log(b.map(e=>e.file+" "+e.rule+" "+e.count).join("\n"))' "${AREA}/stylelint-baseline.json"
  assert_output "src/a.scss color-no-hex 1
src/b.scss color-no-hex 1"
}

@test "update orders rules within a file" {
  scss src/a.scss '.a {
  z-index: 5;
  color: #fff;
}'
  ratchet --update "${AREA}"
  run node -e 'const b=JSON.parse(require("fs").readFileSync(process.argv[1]));console.log(b.map(e=>e.rule).join(" "))' "${AREA}/stylelint-baseline.json"
  assert_output "color-no-hex declaration-property-value-allowed-list"
}

@test "a file stylelint cannot parse fails even with --update" {
  scss src/a.scss '.a { color: red;'
  run ratchet --update "${AREA}"
  assert_failure
  assert_output --partial "could not parse"
}

@test "a stylelint-disable-next-line with a reason is not a violation" {
  scss src/a.scss '.a {
  // stylelint-disable-next-line color-no-hex -- brand mark, not themeable
  color: #fff;
}'
  run ratchet "${AREA}"
  assert_success
}

@test "a stylelint-disable-next-line without a reason is flagged" {
  scss src/a.scss '.a {
  // stylelint-disable-next-line color-no-hex
  color: #fff;
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "report-descriptionless-disables"
}

@test "rgb, hsl, important, bare font-size and z-index are each flagged" {
  scss src/a.scss '.a {
  color: rgb(0 0 0 / 50%);
  background: hsl(0 0% 0%);
  font-size: 14px;
  z-index: 10;
  margin: 0 !important;
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "function-disallowed-list  baseline 0, now 2"
  assert_output --partial "declaration-property-value-allowed-list  baseline 0, now 2"
  assert_output --partial "declaration-no-important"
}

@test "token values pass for font-size and z-index" {
  scss src/a.scss '.a {
  font-size: var(--text-sm);
  z-index: var(--z-modal);
}

.b {
  font-size: inherit;
  z-index: auto;
}

.c {
  z-index: -1;
}'
  run ratchet "${AREA}"
  assert_success
}

@test "a width media query fails outside the breakpoint file and passes inside it" {
  scss src/a.scss '@media (width <= 720px) {
  .a {
    color: red;
  }
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "media-feature-name-disallowed-list"
  rm "${AREA}/src/a.scss"
  scss src/styles/_layout.scss '@media (width <= 720px) {
  .a {
    color: red;
  }
}'
  run ratchet "${AREA}"
  assert_success
}

@test "colours are allowed in src/index.scss only" {
  scss src/index.scss ':root {
  --color-bg: #06080f;
}'
  run ratchet "${AREA}"
  assert_success
  scss src/other.scss ':root {
  --color-bg: #06080f;
}'
  run ratchet "${AREA}"
  assert_failure
}

@test "var() fallbacks fail under js-components and pass elsewhere" {
  scss src/a.scss '.a {
  color: var(--color-text, red);
}'
  run ratchet "${AREA}"
  assert_success

  export STYLELINT_RATCHET_ROOT="${BATS_TEST_TMPDIR}/jsc_repo"
  scratch_tree "${STYLELINT_RATCHET_ROOT}"
  mkdir -p "${STYLELINT_RATCHET_ROOT}/js-components/ui/src"
  AREA="${STYLELINT_RATCHET_ROOT}/js-components"
  scss ui/src/a.scss '.a {
  color: var(--color-text, red);
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "declaration-property-value-disallowed-list"
}

@test "height and min/max media features are flagged too" {
  scss src/a.scss '@media (min-height: 500px) {
  .a {
    color: red;
  }
}'
  run ratchet "${AREA}"
  assert_failure
  assert_output --partial "media-feature-name-disallowed-list"
}

@test "colours are allowed in a js-components storybook preview only" {
  scratch_tree "${BATS_TEST_TMPDIR}/jsc"
  export STYLELINT_RATCHET_ROOT="${BATS_TEST_TMPDIR}/jsc"
  AREA="${STYLELINT_RATCHET_ROOT}/js-components"
  scss ui/.storybook/preview.scss ':root {
  --color-bg: #06080f;
}'
  run ratchet "${AREA}"
  assert_success
  scss ui/src/a.scss ':root {
  --color-bg: #06080f;
}'
  run ratchet "${AREA}"
  assert_failure
}
