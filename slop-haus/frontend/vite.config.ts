// defineConfig comes from vitest/config rather than plain vite -- it's the
// same vite config type plus a `test` key, so this stays the single source
// of truth for both `vite dev`/`vite build` and `vitest` instead of a
// second, easily-drifting config file.
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    // Mirrors tsconfig.app.json's "@/*" path mapping -- TS only checks
    // types with that, it doesn't teach Vite/Rollup how to actually
    // resolve the specifier, so the alias has to be declared here too.
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    proxy: {
      // Local `npm run dev` equivalent of the nginx /api/ proxy used in
      // docker-compose-dev.yaml's frontend service -- keeps the frontend
      // talking same-origin to /api/* in both setups, so the Go backend
      // never needs CORS support. Override for a non-default local api
      // port/host via VITE_API_PROXY_TARGET.
      "/api": {
        target: process.env.VITE_API_PROXY_TARGET ?? "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    // Must exceed setup.ts's asyncUtilTimeout (5s): a findBy* waiting on a
    // lazy route's first load otherwise hits the 5s default test timeout
    // before it can report anything, and CI load made that routine.
    testTimeout: 20_000,
    hookTimeout: 20_000,
  },
});
