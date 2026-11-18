# Tests for the capture and the report, against pages whose styles are SCSS
# compiled with the repo's Sass and an embedded source map.
#
#   bats/run.sh dev-tools/style-check/coverage   # from the repo root

ROOT="$(cd -- "${BATS_TEST_DIRNAME}/../../.." && pwd)"
COLLECT="${BATS_TEST_DIRNAME}/collect_pages.mjs"
REPORT="${BATS_TEST_DIRNAME}/report.mjs"

setup() {
  bats_load_library repo_test
  require_command node
  SITE="${BATS_TEST_TMPDIR}/site"
  mkdir -p "${SITE}"
  cat > "${SITE}/styles.scss" << 'SCSS'
.used { color: red; }
.never { color: blue; }
.link:hover { color: green; }
.used {
  .child { margin: 0; }
}
@media (max-width: 500px) {
  .used { color: pink; }
}
SCSS
  "${ROOT}/node_modules/.bin/sass" --embed-source-map --source-map-urls=absolute "${SITE}/styles.scss" "${SITE}/styles.css"
  page "$(< "${SITE}/styles.css")" '<div class="used"><a class="link">x</a></div>' "${SITE}/page.html"
  cat > "${SITE}/viewports.json" << 'JSON'
{"viewports": {"desktop": {"width": 1280, "height": 800}}}
JSON
  cat > "${SITE}/viewports_phone.json" << 'JSON'
{"viewports": {"desktop": {"width": 1280, "height": 800}, "phone": {"width": 390, "height": 844}}}
JSON
}

# page <css> <body> <file>
page() { printf '<!doctype html><style>%s</style><body>%s</body>\n' "$1" "$2" > "$3"; }

capture() { # capture <viewports file> <out> <url>...
  local vp="$1" out="$2"
  shift 2
  node "${COLLECT}" --out "${out}" --viewports "${vp}" "$@"
}

# unused <js expression over u, the styles.scss unused list>
unused() { node -p "const u = require(process.argv[1]).files['styles.scss'].unused; JSON.stringify($1)" "${BATS_TEST_TMPDIR}/out/report.json"; }

report() { node "${REPORT}" --root "${SITE}" --out "${BATS_TEST_TMPDIR}/out" "$@"; }

@test "lists the rules no render applied, at their SCSS lines, and not used or hover-only ones" {
  capture "${SITE}/viewports.json" "${SITE}/c.json" "file://${SITE}/page.html"
  run report "${SITE}/c.json"
  [ "${status}" -eq 0 ]
  run unused 'u'
  [ "${output}" = '[{"line":2,"selectors":[".never"]},{"line":5,"selectors":[".used .child"]},{"line":8,"context":"@media (max-width:500px)","selectors":[".used"]}]' ]
}

@test "a rule only a phone applies is used once a phone is rendered" {
  capture "${SITE}/viewports_phone.json" "${SITE}/c.json" "file://${SITE}/page.html"
  report "${SITE}/c.json"
  run unused 'u.map((r) => r.line)'
  [ "${output}" = '[2,5]' ]
}

@test "a rule a capture of the built CSS used, by selector, is not listed" {
  capture "${SITE}/viewports_phone.json" "${SITE}/c.json" "file://${SITE}/page.html"
  page '.never{color:blue}' '<p class="never">x</p>' "${SITE}/built.html"
  capture "${SITE}/viewports.json" "${SITE}/built.json" "file://${SITE}/built.html"
  report "${SITE}/c.json" "${SITE}/built.json"
  run unused 'u.map((r) => r.line)'
  [ "${output}" = '[5]' ]
}

# One worker, so the writer's page has finished before the reader's opens: a
# context shared between pages would hand the reader the writer's item and
# make its `.seen` rule look used.
@test "a page does not see what an earlier page of the same capture stored" {
  printf '.alone { color: blue; }\n.seen { color: red; }\n' > "${SITE}/stored.scss"
  "${ROOT}/node_modules/.bin/sass" --embed-source-map --source-map-urls=absolute "${SITE}/stored.scss" "${SITE}/stored.css"
  page '' '<script>localStorage.setItem("seen", "1")</script>' "${SITE}/writer.html"
  page "$(< "${SITE}/stored.css")" '<p id="p" class="alone">x</p><script>if (localStorage.getItem("seen")) p.className = "seen"</script>' "${SITE}/reader.html"
  capture "${SITE}/viewports.json" "${SITE}/c.json" --workers 1 "file://${SITE}/writer.html" "file://${SITE}/reader.html"
  report "${SITE}/c.json"
  run node -p "JSON.stringify(require(process.argv[1]).files['stored.scss'].unused.map((r) => r.selectors))" "${BATS_TEST_TMPDIR}/out/report.json"
  [ "${output}" = '[[".seen"]]' ]
}

@test "a --workers that is not a whole number of at least 1 is refused, not an empty capture" {
  for bad in 0 -1 1.5 many; do
    run capture "${SITE}/viewports.json" "${SITE}/c.json" "--workers=${bad}" "file://${SITE}/page.html"
    [ "${status}" -eq 2 ]
  done
  [ ! -e "${SITE}/c.json" ]
}

@test "--include limits the report to source files under a prefix" {
  capture "${SITE}/viewports.json" "${SITE}/c.json" "file://${SITE}/page.html"
  report --include elsewhere/ "${SITE}/c.json"
  run node -p "JSON.stringify(require(process.argv[1]).files)" "${BATS_TEST_TMPDIR}/out/report.json"
  [ "${output}" = '{}' ]
}

@test "the markdown report names the file, the line and the selector" {
  capture "${SITE}/viewports.json" "${SITE}/c.json" "file://${SITE}/page.html"
  report "${SITE}/c.json"
  run grep -F -- '- line 2: `.never`' "${BATS_TEST_TMPDIR}/out/report.md"
  [ "${status}" -eq 0 ]
}

@test "a file that is not a capture is refused" {
  echo '{"sheets": []}' > "${SITE}/bad.json"
  run report "${SITE}/bad.json"
  [ "${status}" -ne 0 ]
  [[ ${output} == *"css-coverage-capture/1"* ]]
}

# coverage.sh itself, on the smallest Storybook there is (js-components/privacy,
# 4 stories): the dev server, the story enumeration and the source maps.
SCRIPT="${BATS_TEST_DIRNAME}/coverage.sh"

@test "coverage.sh renders a site's stories and reports its SCSS files with their rule counts" {
  run "${SCRIPT}" --site js-components/privacy --out "${BATS_TEST_TMPDIR}/cov" --include js-components/privacy/
  [ "${status}" -eq 0 ]
  run node -p "const f = require(process.argv[1]).files; JSON.stringify(Object.keys(f).sort()) + ' ' + (f['js-components/privacy/src/consent/consent_banner.scss'].rules > 0)" "${BATS_TEST_TMPDIR}/cov/report.json"
  [[ ${output} == *'consent_banner.scss'* && ${output} == *' true' ]]
  [ -f "${BATS_TEST_TMPDIR}/cov/captures/stories-js-components-privacy.json" ]
  # The scratch Storybook config is gone.
  run find "${ROOT}/js-components/privacy" -maxdepth 1 -name '.storybook-css-coverage-*'
  [ -z "${output}" ]
}

@test "coverage.sh takes a directory of e2e captures" {
  mkdir -p "${BATS_TEST_TMPDIR}/e2e"
  page '.consent-banner{color:red}' '<div class="consent-banner">x</div>' "${SITE}/built.html"
  capture "${SITE}/viewports.json" "${BATS_TEST_TMPDIR}/e2e/built.json" "file://${SITE}/built.html"
  run "${SCRIPT}" --site js-components/privacy --e2e "${BATS_TEST_TMPDIR}/e2e" --out "${BATS_TEST_TMPDIR}/cov"
  [ "${status}" -eq 0 ]
}

@test "coverage.sh refuses an unknown site, a missing e2e directory and a bad option" {
  run "${SCRIPT}" --site no/such/site --out "${BATS_TEST_TMPDIR}/cov"
  [ "${status}" -eq 2 ]
  run "${SCRIPT}" --site js-components/privacy --e2e "${BATS_TEST_TMPDIR}/nowhere" --out "${BATS_TEST_TMPDIR}/cov"
  [ "${status}" -eq 2 ]
  run "${SCRIPT}" --bogus
  [ "${status}" -eq 2 ]
}
