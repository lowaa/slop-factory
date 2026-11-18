import { readFileSync } from "node:fs";
import { stripTypeScriptTypes } from "node:module";

import type { Page } from "@playwright/test";

export interface DefaultStyledControl {
  description: string;
}

// The helper (js-components/storybook-kit's default_style_check.ts, which
// Storybook runs on every story) as a script that defines its functions:
// types stripped, and `export` dropped so it needs no module loader. It is
// plain DOM code, so the same source runs here against a real page rather
// than a second copy of it.
export function helperSource(typeScript: string): string {
  return stripTypeScriptTypes(typeScript, { mode: "strip" }).replace(/^export /gm, "");
}

export class DefaultStyleChecker {
  private readonly source: string;

  constructor(helperPath: string) {
    this.source = helperSource(readFileSync(helperPath, "utf8"));
  }

  // The visible controls on the page that look like the browser's default in
  // the colour scheme the page is emulating, as `theme` says ("current", the
  // helper's own name for a page left as it is, when the caller does no
  // emulation).
  //
  // Run through page.evaluate as a string, which the page's
  // Content-Security-Policy does not apply to (an injected <script> would be
  // subject to it).
  async find(page: Page, theme = "current"): Promise<DefaultStyledControl[]> {
    return page.evaluate(
      `(() => {
        ${this.source}
        return findInCurrentTheme(document.body, ${JSON.stringify(theme)}).map((c) => ({ description: c.description }));
      })()`,
    ) as Promise<DefaultStyledControl[]>;
  }
}
