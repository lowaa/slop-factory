// Records which of a page's style rules the browser applied, over any
// number of pages, as the capture format report.mjs reads:
//
//   { "format": "css-coverage-capture/1",
//     "sheets": [ { "text": "<the sheet>", "rules": [ { "start": <offset>, "used": bool, "reachable": bool } ] } ] }
//
// "used" is Chrome's own answer (CSS.startRuleUsageTracking: the rule
// matched an element at some point). Page.coverage is not used: it ignores
// the <style> elements a dev server injects. "reachable" is set for a rule
// that was not used but whose selector, with the states a person causes
// (:hover, :focus, ::before...) taken off, does match something on the page.
import { createHash } from "node:crypto";
import { parseRules, staticSelector } from "./rules.mjs";

export const CAPTURE_FORMAT = "css-coverage-capture/1";

export class CssRecorder {
  constructor() {
    this.sheets = new Map();
    this.sessions = new Map();
  }

  async attach(page) {
    const cdp = await page.context().newCDPSession(page);
    const headers = new Map();
    // The text is read when the sheet appears: a page can remove a <style>
    // (a story unmounting) before we stop, and its usage would be lost.
    cdp.on("CSS.styleSheetAdded", ({ header }) => {
      const text = cdp.send("CSS.getStyleSheetText", { styleSheetId: header.styleSheetId }).then((r) => r.text, () => null);
      headers.set(header.styleSheetId, text);
    });
    await cdp.send("DOM.enable");
    await cdp.send("CSS.enable");
    await cdp.send("CSS.startRuleUsageTracking");
    this.sessions.set(page, { cdp, headers });
  }

  // Stops tracking on the page and folds what it saw into the recorder.
  async detach(page) {
    const { cdp, headers } = this.sessions.get(page);
    this.sessions.delete(page);
    try {
      const { ruleUsage } = await cdp.send("CSS.stopRuleUsageTracking");
      // Chrome lists the rules that were used; a rule it does not list was
      // not, and a sheet it lists nothing of is all unused.
      const used = new Map();
      for (const u of ruleUsage) {
        if (!used.has(u.styleSheetId)) used.set(u.styleSheetId, new Set());
        if (u.used) used.get(u.styleSheetId).add(u.startOffset);
      }
      for (const id of headers.keys()) {
        const text = await headers.get(id);
        if (text === null) continue;
        const sheet = this.#sheet(text);
        const pending = [];
        for (const start of sheet.parsed.keys()) {
          const seen = sheet.rules.get(start) ?? { used: false, reachable: false };
          seen.used ||= used.get(id)?.has(start) ?? false;
          sheet.rules.set(start, seen);
          if (!seen.used && !seen.reachable) pending.push(start);
        }
        await this.#markReachable(page, sheet, pending);
      }
    } finally {
      await cdp.detach().catch(() => {});
    }
  }

  async #markReachable(page, sheet, starts) {
    const wanted = [];
    for (const start of starts) {
      const rule = sheet.parsed.get(start);
      if (!rule) continue;
      for (const selector of rule.selectors) {
        const stripped = staticSelector(selector);
        if (stripped !== null) wanted.push({ start, stripped });
      }
    }
    if (wanted.length === 0) return;
    const matched = await page.evaluate((list) => {
      const hits = [];
      for (const { start, stripped } of list) {
        try {
          if (document.querySelector(stripped)) hits.push(start);
        } catch {
          // A selector this browser cannot parse is not reachable.
        }
      }
      return hits;
    }, wanted);
    for (const start of matched) sheet.rules.get(start).reachable = true;
  }

  #sheet(text) {
    const id = createHash("sha1").update(text).digest("hex");
    let sheet = this.sheets.get(id);
    if (!sheet) {
      sheet = { text, rules: new Map(), parsed: new Map() };
      try {
        for (const r of parseRules(text)) sheet.parsed.set(r.start, r);
      } catch {
        // A sheet postcss cannot read has no rules to report.
      }
      this.sheets.set(id, sheet);
    }
    return sheet;
  }

  toJSON() {
    return {
      format: CAPTURE_FORMAT,
      sheets: [...this.sheets.values()].map((s) => ({
        text: s.text,
        // Only the rules that were used or reached; the report reads the rest
        // from the sheet's own text.
        rules: [...s.rules]
          .filter(([, r]) => r.used || r.reachable)
          .map(([start, r]) => ({ start, used: r.used, reachable: r.reachable }))
          .sort((a, b) => a.start - b.start),
      })),
    };
  }
}
