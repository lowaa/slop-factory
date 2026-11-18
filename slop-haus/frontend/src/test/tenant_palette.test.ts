import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { describe, expect, it } from "vitest";
import { compile } from "sass";
import { uiStylesImporter } from "@slop-factory/ui/styles/scss_breakpoints";
import { TENANT_COLOURS } from "@/lib/tenant_colour";
import { contrastRatio, tokensOf } from "./contrast";

const srcDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const css = compile(join(srcDir, "index.scss"), {
  style: "compressed",
  importers: [
    { findFileUrl: (url) => (url.startsWith("@/") ? pathToFileURL(join(srcDir, url.slice(2))) : null) },
    uiStylesImporter,
  ],
}).css;

// What the stripe has to stand out from: the header's background. The app
// has one dark theme, so the light value is the white a light theme's raised
// surface would be, not a token the app defines.
const THEMES = [
  { name: "dark", selector: ":root", header: tokensOf(css, ":root")["--color-bg-raised"] },
  { name: "light", selector: ':root[data-theme=light]', header: "#ffffff" },
] as const;

// WCAG AA for text, and for a non-text graphic (the stripe).
const TEXT_AA = 4.5;
const GRAPHIC_AA = 3;

describe("the tenant palette", () => {
  for (const theme of THEMES) {
    describe(`in the ${theme.name} theme`, () => {
      const tokens = { ...tokensOf(css, ":root"), ...tokensOf(css, theme.selector) };
      for (const key of TENANT_COLOURS) {
        it(`${key}: initials are readable on it, and the stripe stands out from the header`, () => {
          const bg = tokens[`--tenant-${key}-bg`];
          const fg = tokens[`--tenant-${key}-fg`];
          expect(bg, `--tenant-${key}-bg is not defined`).toBeDefined();
          expect(fg, `--tenant-${key}-fg is not defined`).toBeDefined();
          expect(contrastRatio(fg, bg), "badge text on its colour").toBeGreaterThanOrEqual(TEXT_AA);
          expect(contrastRatio(bg, theme.header), "stripe against the header").toBeGreaterThanOrEqual(GRAPHIC_AA);
        });
      }
    });
  }

  it("defines no colour the picker does not offer", () => {
    const defined = Object.keys(tokensOf(css, ":root"))
      .map((name) => /^--tenant-(\w+)-bg$/.exec(name)?.[1])
      .filter((key) => key !== undefined);
    expect(defined.sort()).toEqual([...TENANT_COLOURS].sort());
  });
});
