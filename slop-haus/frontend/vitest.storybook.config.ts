import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import { storybookTest } from "@storybook/addon-vitest/vitest-plugin";
import { playwright } from "@vitest/browser-playwright";

// Separate from vite.config.ts's `test` block (jsdom component tests, run
// via `npm test`): addon-vitest runs every story -- play functions and a11y
// checks included -- in a real browser, a different `test.browser` setup
// that shouldn't be merged into the jsdom one.
export default defineConfig({
  plugins: [storybookTest({ configDir: ".storybook" })],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  // addon-vitest's setup file lives in node_modules, where Vite's dependency
  // scanner never looks, so its CJS-only deps have to be pre-bundled by
  // name or they fail to load in the browser.
  // Without `entries` Vite discovers the stories' deps lazily, and a late
  // re-optimisation mid-run reloads the page, failing the dynamic import of
  // the setup file for whichever test files were loading.
  optimizeDeps: {
    entries: ["src/**/*.stories.tsx", ".storybook/preview.ts"],
    include: ["aria-query", "lz-string", "pretty-format"],
  },
  test: {
    name: "storybook",
    // Heavy stories (ticket_card Default, virtual_list Thousands) timed out at
    // the 15s default on an overloaded CI runner while passing locally.
    testTimeout: 60_000,
    hookTimeout: 60_000,
    browser: {
      enabled: true,
      headless: true,
      provider: playwright(),
      instances: [{ browser: "chromium" }],
    },
  },
});
