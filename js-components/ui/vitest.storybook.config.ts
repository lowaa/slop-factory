import { defineConfig } from "vitest/config";
import { storybookTest } from "@storybook/addon-vitest/vitest-plugin";
import { playwright } from "@vitest/browser-playwright";

// Separate from vitest.config.ts's `test` block (jsdom + RTL component
// tests, run via `npm test`) -- addon-vitest needs a real browser
// environment (Playwright/Chromium), which is a different `test.browser`
// setup than jsdom and shouldn't be merged into the same config. Runs
// every *.stories.tsx file (play functions + addon-a11y checks included)
// as an actual test via `npm run test:storybook`.
export default defineConfig({
  plugins: [storybookTest({ configDir: ".storybook" })],
  // addon-vitest's injected setup file lives inside node_modules rather
  // than project source, so Vite's dependency-optimizer scanner (which
  // only crawls from source entry points) never discovers its CJS-only
  // transitive deps to pre-bundle/ESM-interop them, and they fail to load
  // in the browser with "does not provide an export named ...". Hand-listing
  // them here is the workaround; a future dependency bump may surface a new
  // one the same way.
  optimizeDeps: {
    include: ["aria-query", "lz-string", "pretty-format"],
  },
  test: {
    name: "storybook",
    browser: {
      enabled: true,
      headless: true,
      provider: playwright(),
      instances: [{ browser: "chromium" }],
    },
  },
});
