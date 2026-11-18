import { defineConfig } from "vitest/config";
import { storybookTest } from "@storybook/addon-vitest/vitest-plugin";
import { playwright } from "@vitest/browser-playwright";

// See js-components/ui/vitest.storybook.config.ts's identical comment --
// separate from vitest.config.ts's jsdom + RTL config; addon-vitest needs
// a real browser to run *.stories.tsx files (play functions + addon-a11y
// checks included) via `npm run test:storybook`.
export default defineConfig({
  plugins: [storybookTest({ configDir: ".storybook" })],
  // Same workaround as js-components/ui's config: addon-vitest's injected
  // setup file lives in node_modules, outside Vite's dependency-optimizer
  // scan root, so its CJS-only transitive deps never get pre-bundled and
  // fail to load in the browser without being hand-listed here.
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
