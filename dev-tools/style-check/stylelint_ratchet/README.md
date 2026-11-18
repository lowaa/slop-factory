# Style guard

stylelint, with the shared `stylelint.config.mjs` at the repo root, run over
the SCSS of slop-haus (`slop-haus/frontend`) and
js-components. Each area's `test.sh` runs it (js-components through
`native_test.sh unit`, inside its image), so CI runs it where lint already runs.

## Rules

- No hex or `rgb()`/`hsl()`-family colours, except in the token files
  (`src/index.scss`, js-components `.storybook/*.scss`).
- No width/height `@media` features, except in `src/styles/_layout.scss`, which
  holds the breakpoint mixin. Use the mixin.
- `font-size` is `var(--text-…)` or `inherit`; `z-index` is `var(--z-…)`, `auto`,
  `0` or `-1`.
- No `var(--x, fallback)` in js-components.
- No `!important`, plus everything in `stylelint-config-standard-scss`.

## The baseline

Existing violations are listed per area in `stylelint-baseline.json` as
`{file, rule, count}`. `ratchet.mjs` fails when

- a file has more violations of a rule than its baseline allows (new debt), or
- the baseline allows more than there are (debt was paid and the baseline was
  not lowered).

```sh
node dev-tools/style-check/stylelint_ratchet/ratchet.mjs slop-haus/frontend           # check
node dev-tools/style-check/stylelint_ratchet/ratchet.mjs --update slop-haus/frontend  # rewrite
npx stylelint --config stylelint.config.mjs "slop-haus/frontend/src/**/*.scss"       # every violation, with lines
```

## Fixing a violation

Use the token (`var(--color-…)`, `var(--text-…)`, `var(--z-…)`) or the
breakpoint mixin instead of the literal. If you fixed some of a file's debt,
run `--update` for that area and commit the lower baseline; review the diff,
which should only show counts going down or entries disappearing.

A genuinely new exception is not a baseline bump. Add a
`// stylelint-disable-next-line <rule> -- <reason>` above the line. Never run
`--update` to make a new violation pass.

## Tests

`bats/run.sh dev-tools/style-check/stylelint_ratchet`
