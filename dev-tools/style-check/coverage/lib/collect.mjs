// Opens each URL in every colour scheme and viewport with a CssRecorder
// attached, so a rule only a phone or a dark-scheme render applies still
// counts as used.
import { availableParallelism } from "node:os";
import { chromium } from "playwright-core";
import { CssRecorder } from "./recorder.mjs";

// Pages are independent and each one is mostly waiting on the dev server.
const DEFAULT_WORKERS = Math.max(2, Math.min(6, availableParallelism()));

// `ready(page)` resolves when the page has finished rendering what it is
// going to show. A URL that fails is counted and reported, not fatal: the
// run is a survey, and one broken story must not hide the rest.
export async function collectPages({ urls, schemes, viewports, ready, initScript, workers = DEFAULT_WORKERS, log = console.error }) {
  const recorder = new CssRecorder();
  const failures = [];
  const browser = await chromium.launch({ args: ["--disable-dev-shm-usage", "--hide-scrollbars"] });
  try {
    for (const scheme of schemes) {
      for (const [name, viewport] of Object.entries(viewports)) {
        const contextOptions = { viewport, colorScheme: scheme, reducedMotion: "reduce", locale: "en-US", timezoneId: "UTC" };
        const queue = [...urls];
        const worker = async () => {
          for (let url = queue.shift(); url !== undefined; url = queue.shift()) {
            // A context per page: pages of one context share localStorage and
            // cookies, so what a story writes would change what the stories
            // rendered beside it show, and with it which rules look used.
            const context = await browser.newContext(contextOptions);
            let page = null;
            // Set once the page is done: requests still in flight then fail
            // from their handlers for want of a context, which is not news.
            let closing = false;
            try {
              // Chromium fails every in-flight request with ERR_NETWORK_CHANGED
              // when any interface on the host changes, and a Docker network
              // or container starting elsewhere does that, even for loopback.
              // A story whose module request is lost never renders. Fetched
              // from Node, the requests do not see the notification. maxRetries
              // covers the one reset a reused connection can still take.
              await context.route(/^https?:/, async (route) => {
                try {
                  await route.fulfill({ response: await route.fetch({ maxRetries: 3 }) });
                } catch (e) {
                  // The failure surfaces as a failed page load, so say why.
                  if (!closing) log(`coverage: ${url}: fetching ${route.request().url()}: ${String(e.message).split("\n")[0]}`);
                  await route.abort().catch(() => {});
                }
              });
              page = await context.newPage();
              if (initScript) await page.addInitScript(initScript);
              await recorder.attach(page);
              await page.goto(url, { waitUntil: "load" });
              await ready(page);
            } catch (e) {
              failures.push({ url, scheme, viewport: name, error: String(e.message).split("\n")[0] });
              log(`coverage: ${scheme}-${name} ${url}: ${String(e.message).split("\n")[0]}`);
            } finally {
              // A page that failed to load still reports the sheets it got.
              if (page) await recorder.detach(page).catch((e) => log(`coverage: detach ${url}: ${e.message}`));
              closing = true;
              await context.unrouteAll({ behavior: "ignoreErrors" }).catch(() => {});
              await context.close().catch(() => {});
            }
          }
        };
        await Promise.all(Array.from({ length: workers }, worker));
      }
    }
  } finally {
    await browser.close();
  }
  return { recorder, failures };
}
