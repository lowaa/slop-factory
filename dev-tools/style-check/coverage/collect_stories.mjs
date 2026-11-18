// Captures CSS rule usage over every story of one Storybook site, by
// running its dev server (a dev server because only its styles carry the
// source maps that point a rule back at its SCSS line) and rendering each
// story in the config's colour schemes and viewports.
//
//   collect_stories.mjs <visual-config.json> <site-dir> <capture.json>
//
// The site dir is repo-relative, as in the config's `sites`.
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { basename, join, resolve } from "node:path";
import { collectPages } from "./lib/collect.mjs";

const [configPath, site, outPath] = process.argv.slice(2);
if (!outPath) {
  console.error("usage: collect_stories.mjs <visual-config.json> <site-dir> <capture.json>");
  process.exit(2);
}
const config = JSON.parse(readFileSync(configPath, "utf8"));
const siteConfig = config.sites[site];
if (!siteConfig) {
  console.error(`collect_stories: ${site} is not in the config's sites`);
  process.exit(2);
}
const top = resolve(import.meta.dirname, "../../..");
const siteDir = join(top, site);
const baseConfigDir = join(siteDir, ".storybook");
if (!existsSync(baseConfigDir)) {
  console.error(`collect_stories: ${site} has no .storybook`);
  process.exit(2);
}

// The site's own config, with source maps for CSS turned on and nothing
// else changed, so the stories render as they do under `npm run storybook`.
// It lives in the site (not a scratch dir) because Storybook loads it with
// the site's module resolution. Removed when the run ends; one per run, so
// two runs of a site do not delete each other's.
const wrapperDir = mkdtempSync(join(siteDir, ".storybook-css-coverage-"));
const wrapperName = basename(wrapperDir);
let server = null;
function cleanUp() {
  server?.kill("SIGTERM");
  rmSync(wrapperDir, { recursive: true, force: true });
}
// Also on an uncaught exception, which skips the finally below: the scratch
// config would stay in the site and fail the next run's cleanliness check.
process.on("exit", cleanUp);
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    cleanUp();
    process.exit(130);
  });
}
const previewFile = readdirSync(baseConfigDir).find((f) => /^preview\.(ts|tsx|js|jsx)$/.test(f));
function writeWrapper() {
  const base = JSON.stringify("../.storybook/main");
  const preview = previewFile ? JSON.stringify(`../.storybook/${previewFile}`) : "null";
  writeFileSync(
    join(wrapperDir, "main.ts"),
    `import base from ${base};

const preview = ${preview};

export default {
  ...base,
  async previewAnnotations(entries: string[], options: unknown) {
    const inherited = base.previewAnnotations ? await base.previewAnnotations(entries, options) : entries;
    return preview ? [...inherited, new URL(preview, import.meta.url).pathname] : inherited;
  },
  async viteFinal(viteConfig: any, options: unknown) {
    const inherited = base.viteFinal ? await base.viteFinal(viteConfig, options) : viteConfig;
    return { ...inherited, css: { ...inherited.css, devSourcemap: true } };
  },
};
`,
  );
}

function freePort() {
  return new Promise((ok, fail) => {
    const s = createServer();
    s.once("error", fail);
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => ok(port));
    });
  });
}

async function waitForIndex(origin, server, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (server.exitCode !== null) throw new Error(`storybook exited with ${server.exitCode}`);
    try {
      const r = await fetch(`${origin}/index.json`);
      if (r.ok) return await r.json();
    } catch {
      // Not listening yet.
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error("storybook did not serve index.json in time");
}

// A story is rendered when Storybook says so; a story that throws says so
// too. Then the DOM gets a few quiet frames, for what a play function or a
// stubbed fetch updates after.
const HOOK = `(() => {
  window.__storyDone = false;
  const hook = setInterval(() => {
    const channel = window.__STORYBOOK_ADDONS_CHANNEL__;
    if (!channel) return;
    clearInterval(hook);
    for (const ev of ["storyRendered", "storyErrored", "storyThrewException", "playFunctionThrewException", "storyMissing"]) {
      channel.on(ev, () => { window.__storyDone = true; });
    }
  }, 2);
})();`;

async function ready(page) {
  await page.waitForFunction(() => window.__storyDone === true, null, { timeout: 30000 });
  await page.evaluate(
    () =>
      new Promise((done) => {
        let quiet = 0;
        const observer = new MutationObserver(() => (quiet = 0));
        observer.observe(document.documentElement, { subtree: true, childList: true, attributes: true, characterData: true });
        const started = performance.now();
        const tick = () => {
          quiet++;
          if (quiet >= 10 || performance.now() - started > 3000) {
            observer.disconnect();
            done();
          } else requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      }),
  );
}

writeWrapper();
const port = await freePort();
const origin = `http://127.0.0.1:${port}`;
const bin = [join(siteDir, "node_modules/.bin/storybook"), join(top, "node_modules/.bin/storybook")].find(existsSync);
server = spawn(bin, ["dev", "--port", String(port), "--host", "127.0.0.1", "--no-open", "--ci", "--disable-telemetry", "-c", wrapperName], {
  cwd: siteDir,
  stdio: ["ignore", "ignore", "inherit"],
});
let status = 0;
try {
  const index = await waitForIndex(origin, server, 180000);
  const ids = Object.values(index.entries ?? {})
    .filter((e) => e.type === "story")
    .map((e) => e.id)
    .sort();
  const urls = ids.map((id) => `${origin}/iframe.html?id=${encodeURIComponent(id)}&viewMode=story`);
  const { recorder, failures } = await collectPages({
    urls,
    schemes: siteConfig.schemes,
    viewports: config.viewports,
    ready,
    initScript: HOOK,
  });
  writeFileSync(outPath, JSON.stringify(recorder));
  console.error(`coverage: ${site}: ${ids.length} stories, ${failures.length} failed renders`);
  // A render that failed leaves its rules looking unused, so one is enough
  // to make the run fail; the capture is still written, for looking into.
  if (failures.length > 0) status = 1;
} catch (e) {
  console.error(`coverage: ${site}: ${e.message}`);
  status = 1;
} finally {
  cleanUp();
}
process.exit(status);
