# e2e-support

App-agnostic Playwright support code shared by `slop-haus/e2e_playwright_ts`
and other apps' Playwright suites. It knows nothing about any app: URLs,
paths and credentials are passed in, and `src/no_app_imports.spec.ts`
enforces that nothing is imported from an app.

Each app keeps what encodes its own shape: `env.ts`, `playwright.config.ts`,
`fixtures.ts` (its auth model), consent helpers and its Dockerfile, plus
thin modules that bind the shared classes to its URLs (e.g. `support/mailhog.ts`).
When an app needs different behaviour, add a parameter here rather than
forking the code back into the app.

This is a plain directory, not an npm workspace package. Each e2e image gets
`src/` through a named build context (`e2e_support` in the suite's
`docker-compose-dev.e2e.yaml`) at `/srv/e2e_support`, so `pg`, `yaml` and
`@playwright/test` resolve from the suite's own `node_modules`. Apps import it
as `@e2e-support/<module>`. The `*.spec.ts` files here are pure (no browser,
no stack) and run as the `e2e_support` Playwright project in each suite.
From a checkout, each suite's `npm run sync-support` (run by `pretest` and
`pretypecheck`) copies it to the same place, gitignored. Both e2e pipelines
list this directory in their path includes.
