import { compileString, type FileImporter } from "sass";
import { basename, dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

const UI_STYLES_PREFIX = "@slop-factory/ui/styles/";

// Resolves the package's public SCSS specifiers the way an app's bundler
// does, without needing node_modules links where the test runs.
export const uiStylesImporter: FileImporter<"sync"> = {
  findFileUrl(url) {
    if (!url.startsWith(UI_STYLES_PREFIX)) return null;
    const name = url.slice(UI_STYLES_PREFIX.length);
    return pathToFileURL(join(import.meta.dirname, `_${name}.scss`));
  },
};

// readScssBreakpoints compiles an app's layout stylesheet (the file that
// configures `@use "@slop-factory/ui/styles/layout" with ($breakpoints:
// ...)`) and returns the named widths in px, for a test to compare with the
// app's createBreakpoints map. Sass cannot be imported into TS and TS
// cannot feed Sass without a build step, so the two are kept honest by this
// comparison instead.
//
// The probe loads ui's layout by the same specifier the app does, so it gets
// the app's configured instance rather than an empty second one.
export function readScssBreakpoints(appLayoutScssPath: string): Record<string, number> {
  const { css } = compileString(
    `@use "./${basename(appLayoutScssPath)}"; @use "${UI_STYLES_PREFIX}layout" as ui; @include ui.export-breakpoints;`,
    {
      url: pathToFileURL(join(dirname(appLayoutScssPath), "probe.scss")),
      importers: [uiStylesImporter],
    },
  );
  const widths: Record<string, number> = {};
  for (const [, name, px] of css.matchAll(/--([\w-]+):\s*(\d+(?:\.\d+)?)px;/g)) {
    widths[name] = Number(px);
  }
  return widths;
}
