bats_load_library repo_test

SCRIPT="${BATS_TEST_DIRNAME}/own_tests.sh"

setup() {
  DIR=$(mktemp -d)
  cat > "${DIR}/manifest.json" << 'J'
[{"path":"a/x_test.go","pkg":"m/a","tests":["TestX"]},
 {"path":"b/y_test.go","pkg":"m/b","tests":["TestY"]},
 {"path":"c/z_test.go","pkg":"m/c","tests":["TestZ"]},
 {"path":"d/w_test.go","pkg":"m/d","tests":["TestW"]}]
J
}

teardown() { rm -rf "${DIR}"; }

ev() { printf '{"Package":"%s","Test":"%s","Action":"%s"}\n' "$1" "$2" "$3"; }

result() { jq -r --arg p "$1" --arg k "$2" '.[] | select(.Path == $p) | .[$k]' <<< "${output}"; }

@test "fails on base, passes on head" {
  {
    ev m/a TestX pass
    ev m/b TestY pass
  } > "${DIR}/head"
  {
    ev m/a TestX fail
    echo 'build chatter'
    ev m/b TestY pass
  } > "${DIR}/base"
  run "${SCRIPT}" summarize "${DIR}/manifest.json" "${DIR}/head" "${DIR}/base"
  [ "$status" -eq 0 ]
  [ "$(result a/x_test.go Base)" = fail ]
  [ "$(result a/x_test.go Head)" = pass ]
  [ "$(result b/y_test.go Base)" = pass ]
}

@test "a build failure on the base is compile-fail" {
  ev m/a TestX pass > "${DIR}/head"
  printf '{"Package":"m/a","Action":"output","Output":"FAIL\\tm/a [build failed]\\n"}\n{"Package":"m/a","Action":"fail"}\n' > "${DIR}/base"
  run "${SCRIPT}" summarize "${DIR}/manifest.json" "${DIR}/head" "${DIR}/base"
  [ "$(result a/x_test.go Base)" = compile-fail ]
}

@test "a package missing from the events is unknown, never a guess" {
  ev m/a TestX pass > "${DIR}/head"
  : > "${DIR}/base"
  run "${SCRIPT}" summarize "${DIR}/manifest.json" "${DIR}/head" "${DIR}/base"
  [ "$(result a/x_test.go Base)" = unknown ]
  [ "$(result b/y_test.go Head)" = unknown ]
}

@test "skipped tests did not run; subtests and other tests are ignored" {
  {
    ev m/c TestZ skip
    ev m/d TestW/sub pass
    ev m/d TestOther pass
    printf "{\"Package\":\"m/d\",\"Action\":\"pass\"}\n"
  } > "${DIR}/head"
  cp "${DIR}/head" "${DIR}/base"
  run "${SCRIPT}" summarize "${DIR}/manifest.json" "${DIR}/head" "${DIR}/base"
  [ "$(result c/z_test.go Head)" = did-not-run ]
  [ "$(result d/w_test.go Head)" = did-not-run ]
}

@test "manifest lists the top-level tests a file declares" {
  mkdir -p "${DIR}/slop-haus/server/p"
  printf 'package p\nfunc TestA(t *testing.T) {}\nfunc helper() {}\nfunc BenchmarkB(b *testing.B) {}\n' > "${DIR}/slop-haus/server/p/a_test.go"
  cd "${DIR}"
  run "${SCRIPT}" manifest mod slop-haus/server/p/a_test.go
  [ "$(jq -c '.[0].tests' <<< "${output}")" = '["TestA"]' ]
  [ "$(jq -r '.[0].pkg' <<< "${output}")" = mod/p ]
}

@test "no changed test files prints an empty block" {
  cd "${DIR}"
  git init -q . && git -c user.email=a@b -c user.name=n commit -q --allow-empty -m base
  mkdir -p slop-haus/server && echo 'module mod' > slop-haus/server/go.mod
  run "${SCRIPT}" run HEAD
  [ "$status" -eq 0 ]
  [[ "$output" == *OWNTESTS_JSON_BEGIN*'[]'*OWNTESTS_JSON_END* ]]
}

@test "a file with several tests fails if any fails; setup failures are compile-fail" {
  printf 'package a\nfunc TestP(t *testing.T) {}\nfunc TestQ(t *testing.T) {}\n' > "${DIR}/q_test.go"
  jq -c '.[0].tests = ["TestP","TestQ"]' "${DIR}/manifest.json" > "${DIR}/m2.json"
  {
    ev m/a TestP pass
    ev m/a TestQ fail
    ev m/a "" fail
  } | sed 's/"Test":"",//' > "${DIR}/head"
  printf '{"Package":"m/a","Action":"output","Output":"FAIL m/a [setup failed]\\n"}\n' > "${DIR}/base"
  run "${SCRIPT}" summarize "${DIR}/m2.json" "${DIR}/head" "${DIR}/base"
  [ "$(result a/x_test.go Head)" = fail ]
  [ "$(result a/x_test.go Base)" = compile-fail ]
}

@test "an interrupted run (tests unreported, no package result) is unknown" {
  jq -c '.[0].tests = ["TestX","TestX2"]' "${DIR}/manifest.json" > "${DIR}/m2.json"
  ev m/a TestX pass > "${DIR}/head"
  cp "${DIR}/head" "${DIR}/base"
  run "${SCRIPT}" summarize "${DIR}/m2.json" "${DIR}/head" "${DIR}/base"
  [ "$(result a/x_test.go Head)" = unknown ]
}

@test "a package-level fail with no test results is fail" {
  {
    ev m/a TestX run
    printf '{"Package":"m/a","Action":"fail"}\n'
  } | sed 's/"Test":"[^"]*",//;' > "${DIR}/head"
  cp "${DIR}/head" "${DIR}/base"
  run "${SCRIPT}" summarize "${DIR}/manifest.json" "${DIR}/head" "${DIR}/base"
  [ "$(result a/x_test.go Head)" = fail ]
}

@test "run: head passes, the base (merge-base plus the test file) fails" {
  cd "${DIR}"
  git init -q -b main . && git config user.email a@b && git config user.name n
  mkdir -p slop-haus/server/p
  echo 'module mod' > slop-haus/server/go.mod
  echo old > slop-haus/server/p/state
  # Stand-in for test.sh: the suite passes only where state says "new".
  cat > slop-haus/server/test.sh << 'S'
#!/usr/bin/env bash
if grep -q new slop-haus/server/p/state; then a=pass; else a=fail; fi
echo '{"Package":"mod/p","Test":"TestA","Action":"'"$a"'"}'
echo '{"Package":"mod/p","Action":"'"$a"'"}'
[[ $a == pass ]]
S
  chmod +x slop-haus/server/test.sh
  git add -A && git commit -qm base
  git switch -qc attempt
  echo new > slop-haus/server/p/state
  printf 'package p\nfunc TestA(t *testing.T) {}\n' > slop-haus/server/p/a_test.go
  git add -A && git commit -qm head
  run "${SCRIPT}" run main
  [ "$status" -eq 0 ]
  json=$(sed -n '/BEGIN/,/END/p' <<< "${output}" | sed '1d;$d')
  [ "$(jq -r '.[0].Path' <<< "${json}")" = slop-haus/server/p/a_test.go ]
  [ "$(jq -r '.[0].Head' <<< "${json}")" = pass ]
  [ "$(jq -r '.[0].Base' <<< "${json}")" = fail ]
}
