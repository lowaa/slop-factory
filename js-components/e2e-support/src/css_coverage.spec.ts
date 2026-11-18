import { mkdtempSync, readdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { expect, test, type Page } from "@playwright/test";

import { CAPTURE_FORMAT, CssCoverageRecorder, useWithCssCoverage } from "./css_coverage";

interface Call {
  method: string;
  params?: Record<string, unknown>;
}

// A page whose CDP session has three stylesheets: "1" with one used and one
// unused rule, "2" with none used (Chrome lists nothing for it), and "3",
// which the page has removed again by the time tracking stops.
function fakePage(): { page: Page; calls: Call[]; detached: () => boolean } {
  let handler: (e: { header: { styleSheetId: string } }) => void = () => {};
  let detached = false;
  const calls: Call[] = [];
  const texts: Record<string, string> = { "1": ".a{x:y}\n.b{x:y}", "2": ".c{x:y}" };
  const cdp = {
    on: (_event: string, h: typeof handler) => {
      handler = h;
    },
    send: async (method: string, params?: Record<string, unknown>) => {
      calls.push({ method, params });
      if (method === "CSS.enable") for (const id of ["1", "2", "3"]) handler({ header: { styleSheetId: id } });
      if (method === "CSS.getStyleSheetText") {
        const text = texts[String(params?.styleSheetId)];
        if (text === undefined) throw new Error("No style sheet with given id found");
        return { text };
      }
      if (method === "CSS.stopRuleUsageTracking") {
        return {
          ruleUsage: [
            { styleSheetId: "1", startOffset: 0, endOffset: 7, used: true },
            { styleSheetId: "1", startOffset: 8, endOffset: 15, used: false },
            { styleSheetId: "elsewhere", startOffset: 0, endOffset: 9, used: true },
          ],
        };
      }
      return {};
    },
    detach: async () => {
      detached = true;
    },
  };
  const page = { context: () => ({ newCDPSession: async () => cdp }) } as unknown as Page;
  return { page, calls, detached: () => detached };
}

test("stop writes each stylesheet's used rules as a capture file, keeping one with none used", async () => {
  const dir = mkdtempSync(join(tmpdir(), "css-coverage-"));
  const recorder = new CssCoverageRecorder(dir);
  const { page, calls, detached } = fakePage();

  await recorder.start(page);
  const file = await recorder.stop(page, "tickets list/opens");

  expect(file).toMatch(/tickets_list_opens-0\.json$/);
  expect(JSON.parse(readFileSync(file!, "utf8"))).toEqual({
    format: CAPTURE_FORMAT,
    sheets: [
      { text: ".a{x:y}\n.b{x:y}", rules: [{ start: 0, used: true, reachable: false }] },
      { text: ".c{x:y}", rules: [] },
    ],
  });
  expect(detached()).toBe(true);
  // Tracking starts after the domains are enabled, and ends on stop; each
  // sheet's text is asked for by its own id.
  const methods = calls.map((c) => c.method);
  expect(methods.indexOf("DOM.enable")).toBeLessThan(methods.indexOf("CSS.enable"));
  expect(methods.indexOf("CSS.enable")).toBeLessThan(methods.indexOf("CSS.startRuleUsageTracking"));
  expect(methods.indexOf("CSS.startRuleUsageTracking")).toBeLessThan(methods.indexOf("CSS.stopRuleUsageTracking"));
  expect(calls.filter((c) => c.method === "CSS.getStyleSheetText").map((c) => c.params)).toEqual([
    { styleSheetId: "1" },
    { styleSheetId: "2" },
    { styleSheetId: "3" },
  ]);
});

test("stop on a page that was never started writes nothing", async () => {
  const dir = mkdtempSync(join(tmpdir(), "css-coverage-"));
  expect(await new CssCoverageRecorder(dir).stop(fakePage().page, "x")).toBeNull();
  expect(readdirSync(dir)).toEqual([]);
});

test("useWithCssCoverage writes a capture after the test's use when given a directory", async () => {
  const dir = mkdtempSync(join(tmpdir(), "css-coverage-"));
  const { page } = fakePage();
  let filesDuringUse: string[] | null = null;

  await useWithCssCoverage(page, { title: "opens a ticket", workerIndex: 3 }, async () => {
    filesDuringUse = readdirSync(dir);
  }, dir);

  expect(filesDuringUse).toEqual([]);
  expect(readdirSync(dir)).toEqual(["w3-opens_a_ticket-0.json"]);
});

test("useWithCssCoverage without a directory just runs the test and touches nothing", async () => {
  const { page, calls } = fakePage();
  let used = false;

  await useWithCssCoverage(page, { title: "x", workerIndex: 0 }, async () => {
    used = true;
  }, "");

  expect(used).toBe(true);
  expect(calls).toEqual([]);
});

test("useWithCssCoverage records the capture even when the test fails, and keeps the failure", async () => {
  const dir = mkdtempSync(join(tmpdir(), "css-coverage-"));
  const { page } = fakePage();

  await expect(
    useWithCssCoverage(page, { title: "fails", workerIndex: 0 }, async () => {
      throw new Error("boom");
    }, dir),
  ).rejects.toThrow("boom");

  expect(readdirSync(dir)).toHaveLength(1);
});

test("useWithCssCoverage takes its directory from CSS_COVERAGE_DIR by default, and numbers captures within a worker", async () => {
  const dir = mkdtempSync(join(tmpdir(), "css-coverage-"));
  const before = process.env.CSS_COVERAGE_DIR;
  process.env.CSS_COVERAGE_DIR = dir;
  try {
    for (let i = 0; i < 2; i++) {
      await useWithCssCoverage(fakePage().page, { title: "same title", workerIndex: 1 }, async () => {});
    }
  } finally {
    if (before === undefined) delete process.env.CSS_COVERAGE_DIR;
    else process.env.CSS_COVERAGE_DIR = before;
  }

  expect(readdirSync(dir).sort()).toEqual(["w1-same_title-0.json", "w1-same_title-1.json"]);
});
