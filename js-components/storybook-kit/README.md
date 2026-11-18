# storybook-kit

App-agnostic Storybook test tooling. The kit knows nothing about any app:
providers, exemption maps, theme switches and client objects are passed in
(`src/no_app_imports.test.ts` enforces that nothing is imported from an app).
When an app needs different behaviour, add an option here rather than forking
the code back into the app.

- `default_style_check`: fails a story whose form controls look like the
  browser's default. `themes` takes the app's theme switches
  (`rootAttributeTheme` covers an attribute on the root); with none, the page
  is checked as it is. The file imports nothing, so an e2e suite can compile
  and inject it into a page.
- `story_coverage`: `checkStoryCoverage({ srcDir, coveredByParent, mustFind })`
  from a thin `story_coverage.test.ts` holding the app's exemption map.
- `design_tokens`: shared components read `var(--token)` with no fallback, so
  a host that lacks the token leaves the property unset. `checkPackageTokens`
  (from a package's `tokens.test.ts`) fails when the package's styles read a
  token its `REQUIRED_TOKENS`/`INTERNAL_TOKENS` (`src/tokens.ts`) don't list,
  or when `preview_tokens.scss` (below) doesn't define a required one in every
  theme. `checkAppTokens` (from the app's `design_tokens.test.ts`) fails when
  the app doesn't define, in every theme of its compiled root stylesheet, a
  token a package requires or its own stylesheets read.
- `.storybook/preview_tokens.scss`: the tokens every package's Storybook canvas supplies,
  `@use`d from each package's `.storybook/preview.scss`.
- `api_guard`: `createApiGuard(client)` makes every method of a client fail a
  story unless stubbed, even if the component swallowed the error.

There is no shared `vitest.storybook` config factory on purpose: it would
import `@storybook/addon-vitest` from the kit's own `node_modules`, a second
copy of Storybook beside the app's, and stories then run against two
instances (mocks stop resetting between stories).
