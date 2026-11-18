# CSS coverage

`coverage.sh` lists the style rules nothing uses, per SCSS file, from what a
browser actually applied. A grep of class names cannot say that: it misses
names built at runtime (`range-bar--${state}`) and cannot tell which rules
apply. It removes nothing; deleting is a decision per rule (below).

```
dev-tools/style-check/coverage/coverage.sh [--out <dir>] [--site <dir>]... [--e2e <dir>]... [--include <path-prefix>]...
```

Needs `npm ci` at the repo root and Playwright's Chromium (the same one the
visual diff uses). No Docker: pixels do not matter here, so it runs on the
host. A full run takes several minutes. It writes `<out>/report.md` and
`report.json` (default out: `./css-coverage`) and keeps the raw captures in
`<out>/captures/`, so a report can be remade with `report.mjs` without
rendering again.

## How it works

1. **Stories.** For each site in `config.json`, `collect_stories.mjs`
   starts the site's Storybook *dev server* and renders every story in each of
   the site's colour schemes at the desktop and phone viewports, so a rule that
   only a dark scheme or a phone applies counts. The dev server, because only
   its styles carry source maps (`css.devSourcemap`, turned on by a scratch
   config dir `.storybook-css-coverage/` next to the site's own, removed at the
   end); a production build's CSS has none.
2. **Usage.** Chromium's `CSS.startRuleUsageTracking` says which rules matched
   an element (`lib/recorder.mjs`). Playwright's `page.coverage` is not used:
   it skips the `<style>` elements the dev server injects.
3. **State-dependent rules.** A rule such as `.button:hover` never matches
   while a story is only rendered. When a rule was not used, its selector with
   the user-caused parts taken off (`:hover`, `:focus`, `::before`...) is tried
   with `querySelector`; if that matches, the rule is *reachable*, not unused.
4. **e2e.** `js-components/e2e-support/src/css_coverage.ts` records the same
   thing from a Playwright test's page and writes a capture file. The e2e
   suites run the built app, which has no source map, so those rules are
   matched to the SCSS ones by at-rule context and selector. A selector the
   build rewrote does not match, which leaves a rule listed, never hides one.
   Pass the directory of capture files as `--e2e`.
5. **Report.** Each dev-server rule is mapped back to its SCSS line through the
   source map. A rule is listed only if no story used it, no story reached it,
   and no e2e capture used a rule with the same context and selector.

## Recording the e2e suites

Captures are local files and are not committed, so each run records its own.
The suites run in containers: mount a host directory and point
`CSS_COVERAGE_DIR` at it through `E2E_RUN_ARGS` (the suite's `test.sh` passes
it to `compose run`). Unset, nothing is recorded.

```
COV=$PWD/css-e2e   # an absolute path
mkdir -p "$COV/slop-haus"
E2E_RUN_ARGS="-v $COV/slop-haus:/css-cov -e CSS_COVERAGE_DIR=/css-cov" slop-haus/e2e_playwright_ts/test.sh
dev-tools/style-check/coverage/coverage.sh --e2e "$COV/slop-haus"
```

The containers run as root, so the files are root's; `chown` them if
`coverage.sh` cannot read them. A run is slow: the whole e2e suite, then
every story. `--include slop-haus/` (or `js-components/`) reports
one package.

Recorded: the `page` fixture and `anonPage`, one
capture file per test. Not recorded: pages a spec makes itself. In slop-haus
that is `independentPage` in `tests/support/auth_flow.ts` (15 calls in 6
specs: admin, auth, invites, sign_in_methods, tenant_settings, ticket_links)
and one `browser.newContext()` in `conversation_chat.spec.ts` (the
touch-device test). A rule only those pages apply shows as unused,
which the triage below catches.

## Reading the report

Per file: rules, used, reachable, used through the same selector elsewhere, unused. Rules
that several places emit from one source line (a mixin, an `@each`) count once,
as used if any of them was.

Not covered: `@keyframes`, `@font-face`, custom properties, and rules in a
stylesheet no story or e2e page loads (those do not appear at all).

## Triage

Unused in the report is not removable. For each rule:

- **delete** it if no code can produce its class: read the components,
  including class names built at runtime (`grep` the prefix, not only the full
  name);
- **add a story** that shows the state (error, empty, loading) if a component
  can produce it; that also fixes a story gap;
- **keep** it, with a one-line reason in the PR.

A removal PR shows the visual diff (`../visual_diff.sh`: no change), the CSS
diff (`../css_diff.sh`: only removals) and the coverage report before and
after.

## Tests

`bats/run.sh dev-tools/style-check/coverage`: SCSS compiled with the repo's
Sass and an embedded source map, rendered in Chromium, reported. It runs in the
`dev-tools-style-visual` pipeline, which has the browser image.
