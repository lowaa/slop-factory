import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import type { CDPSession, Page } from "@playwright/test";

// Records which style rules a test's page applied, as a capture file that
// dev-tools/style-check/coverage/report.mjs reads (its recorder.mjs defines
// the format; this is the part of it an e2e run needs). The e2e suites run
// the built app, whose CSS has no source map, so the report matches these
// rules to the SCSS ones by selector.
//
// Chrome's rule-usage tracking, not Playwright's page.coverage: the latter
// reports only stylesheets that have a URL.
export const CAPTURE_FORMAT = "css-coverage-capture/1";

interface RuleUsage {
  styleSheetId: string;
  startOffset: number;
  endOffset: number;
  used: boolean;
}

interface Capture {
  format: typeof CAPTURE_FORMAT;
  sheets: { text: string; rules: { start: number; used: boolean; reachable: boolean }[] }[];
}

export class CssCoverageRecorder {
  // Each sheet's text is read when it appears: a page can remove a <style>
  // before stop(), and its usage would be lost with it.
  private readonly sessions = new Map<Page, { cdp: CDPSession; sheets: Map<string, Promise<string | null>> }>();
  private count = 0;

  constructor(private readonly dir: string) {}

  async start(page: Page): Promise<void> {
    const cdp = await page.context().newCDPSession(page);
    const sheets = new Map<string, Promise<string | null>>();
    cdp.on("CSS.styleSheetAdded", ({ header }) => {
      const id = header.styleSheetId;
      sheets.set(
        id,
        (cdp.send("CSS.getStyleSheetText", { styleSheetId: id }) as Promise<{ text: string }>).then(
          (r) => r.text,
          () => null,
        ),
      );
    });
    await cdp.send("DOM.enable");
    await cdp.send("CSS.enable");
    await cdp.send("CSS.startRuleUsageTracking");
    this.sessions.set(page, { cdp, sheets });
  }

  // Stops tracking and writes <dir>/<name>-<n>.json. Returns the file's
  // path, or null if start() was never called for this page.
  async stop(page: Page, name: string): Promise<string | null> {
    const session = this.sessions.get(page);
    if (!session) return null;
    this.sessions.delete(page);
    const { cdp, sheets } = session;
    try {
      // Chrome lists the rules that were used; a sheet it lists nothing of
      // is all unused, and still belongs in the capture.
      const { ruleUsage } = (await cdp.send("CSS.stopRuleUsageTracking")) as { ruleUsage: RuleUsage[] };
      const capture: Capture = { format: CAPTURE_FORMAT, sheets: [] };
      for (const [id, pending] of sheets) {
        const text = await pending;
        if (text === null) continue;
        const rules = ruleUsage
          .filter((u) => u.styleSheetId === id && u.used)
          .map((u) => ({ start: u.startOffset, used: true, reachable: false }));
        capture.sheets.push({ text, rules });
      }
      mkdirSync(this.dir, { recursive: true });
      const file = join(this.dir, `${name.replace(/[^A-Za-z0-9._-]+/g, "_")}-${this.count++}.json`);
      writeFileSync(file, JSON.stringify(capture));
      return file;
    } finally {
      await cdp.detach().catch(() => {});
    }
  }
}

export const CSS_COVERAGE_DIR_ENV = "CSS_COVERAGE_DIR";

// One recorder per directory and process, so that the capture files a worker
// writes are numbered across its tests rather than from 0 in each.
const recorders = new Map<string, CssCoverageRecorder>();

// What a page-creating fixture wraps its `use` in: records the page's CSS
// usage when CSS_COVERAGE_DIR names a directory, and does nothing otherwise.
// `workerIndex` is part of the file name because a retry starts a new worker
// whose count begins at 0 again.
export async function useWithCssCoverage(
  page: Page,
  testInfo: { title: string; workerIndex: number },
  use: (page: Page) => Promise<void>,
  dir: string | undefined = process.env[CSS_COVERAGE_DIR_ENV],
): Promise<void> {
  if (!dir) return use(page);
  let recorder = recorders.get(dir);
  if (!recorder) {
    recorder = new CssCoverageRecorder(dir);
    recorders.set(dir, recorder);
  }
  await recorder.start(page);
  try {
    await use(page);
  } finally {
    // Coverage is a by-product: a test that closed its own page must keep its
    // own result rather than gain a coverage error.
    await recorder.stop(page, `w${testInfo.workerIndex}-${testInfo.title}`).catch((e: unknown) => {
      console.warn(`css coverage: not recorded for "${testInfo.title}": ${String(e)}`);
    });
  }
}
