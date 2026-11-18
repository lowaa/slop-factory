import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Node, not jsdom: what is unit tested here reads files and parses source.
// The default-look check needs a real browser and is tested as stories.
// The react plugin is for Storybook, which takes its Vite config from this
// file (as in the other js-components packages).
export default defineConfig({
  plugins: [react()],
  test: { include: ["src/**/*.test.ts"] },
});
