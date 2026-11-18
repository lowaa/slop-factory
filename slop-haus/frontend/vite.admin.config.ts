// A second, separate Vite build for the admin UI (src/admin/) -- the
// requirement it exists to satisfy is that a non-admin's browser must never
// download the admin pages' JavaScript at all, and a single bundle shared
// via lazy-loaded routes can't promise that (the chunk still ships to
// anyone who loads the app; only *rendering* it would be gated). Two
// independent `vite build` runs, each with its own entry and output
// directory, means the admin code is physically absent from the main
// build's /assets/ -- see
// nginx.conf.template's auth_request gate, which is what actually enforces
// who gets served this build's output.
//
// `root` points at src/admin so that directory is its own tiny project
// (its index.html is the implicit entry) -- outDir is given as an absolute
// path so the build lands at <repo>/dist/admin regardless, nested inside
// the main build's own dist/ (see package.json's "build" script, which
// runs this *after* the main `vite build` so the main build's
// emptyOutDir doesn't wipe it back out).
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  root: fileURLToPath(new URL("./src/admin", import.meta.url)),
  base: "/admin/",
  plugins: [react()],
  resolve: {
    // Mirrors vite.config.ts's own alias -- admin_app.tsx imports shared
    // code (the api client, AuthProvider, AuthLayout) the same way the
    // main app does.
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: fileURLToPath(new URL("./dist/admin", import.meta.url)),
    emptyOutDir: true,
  },
});
