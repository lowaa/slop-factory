#!/usr/bin/env bash
# Runs the tests of every js-components/* package, directly on this
# machine. test.sh runs this same script inside a container; use this one
# to skip the image build while iterating. It walks the packages rather
# than a list, so a new package is picked up without a change here.
#
#   native_test.sh unit        lint (oxlint), typecheck (tsc) and test
#                              (vitest), which every package must define,
#                              then the stylelint and oxlint ratchets over
#                              all of them (dev-tools/lint/README.md)
#   native_test.sh storybook   test:storybook: the stories in a real
#                              browser, which every package with a
#                              *.stories.tsx must define
#   native_test.sh             both
#
# A package missing a script it should have fails the run, rather than
# quietly dropping out of it.
#
# Runnable from any cwd -- it cds to the mono repo root itself, since
# `npm run -w` needs to run from wherever the root workspaces package.json
# lives.
#
# `test:storybook` drives actual Storybook stories in a real browser via
# Playwright (see e.g. js-components/ui/vitest.storybook.config.ts), so
# running natively needs Playwright's Chromium installed once beforehand:
#   npx playwright install chromium
set -euo pipefail

unit=0
storybook=0
case "${1:-}" in
  unit) unit=1 ;;
  storybook) storybook=1 ;;
  '') unit=1 storybook=1 ;;
  *)
    echo "usage: $0 [unit|storybook]" >&2
    exit 2
    ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

has_stories() {
  [[ -n $(find "$1/src" -name '*.stories.tsx' -print -quit 2> /dev/null) ]]
}

packages=0
failed=()
for pkg_dir in js-components/*/; do
  pkg_dir="${pkg_dir%/}"
  pkg_json="$pkg_dir/package.json"
  [[ -f $pkg_json ]] || continue
  packages=$((packages + 1))

  scripts=()
  ((unit)) && scripts+=(lint typecheck test)
  ((storybook)) && has_stories "$pkg_dir" && scripts+=(test:storybook)

  for script in "${scripts[@]}"; do
    if ! node -e "process.exit(require('./$pkg_json').scripts?.['$script'] ? 0 : 1)"; then
      failed+=("$pkg_dir: no \"$script\" script in package.json")
      continue
    fi
    echo "== $pkg_dir: npm run $script =="
    # Carry on past a failure, so one run reports every broken package
    # rather than only the first.
    npm run "$script" --workspace "$pkg_dir" || failed+=("$pkg_dir: npm run $script")
  done
done

if ((unit)); then
  echo "== stylelint ratchet =="
  node dev-tools/style-check/stylelint_ratchet/ratchet.mjs js-components || failed+=("js-components: stylelint ratchet")
  echo "== oxlint ratchet =="
  dev-tools/lint/oxlint.sh js-components || failed+=("js-components: oxlint ratchet")
fi

# A move of the packages would otherwise pass by running nothing.
((packages)) || failed+=("no js-components/*/package.json found")

if ((${#failed[@]})); then
  printf 'native_test.sh: failed: %s\n' "${failed[@]}" >&2
  exit 1
fi
