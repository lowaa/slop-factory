import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    // Components import their own .scss; compiling it adds nothing to a
    // jsdom assertion and would make sass a hard test dependency.
    css: false,
  },
});
