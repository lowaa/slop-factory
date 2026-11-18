// WCAG 2 contrast, for tests that hold a colour pair to a threshold.

function channel(value: number): number {
  const c = value / 255;
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance(hex: string): number {
  const m = /^#([0-9a-f]{6}|[0-9a-f]{3})$/i.exec(hex);
  if (!m) throw new Error(`not a #rrggbb or #rgb colour: ${hex}`);
  const full = m[1].length === 3 ? [...m[1]].map((c) => c + c).join("") : m[1];
  const n = parseInt(full, 16);
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255);
}

export function contrastRatio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// The custom properties of every rule of compressed CSS with exactly this
// selector (a stylesheet may have several :root rules), later ones winning.
export function tokensOf(css: string, selector: string): Record<string, string> {
  const tokens: Record<string, string> = {};
  let found = false;
  for (let at = css.indexOf(`${selector}{`); at >= 0; at = css.indexOf(`${selector}{`, at + 1)) {
    if (at > 0 && !"}{;".includes(css[at - 1])) continue;
    found = true;
    const body = css.slice(at + selector.length + 1, css.indexOf("}", at));
    for (const decl of body.split(";")) {
      const [name, value] = decl.split(/:(.*)/s);
      if (name?.startsWith("--") && value) tokens[name] = value.trim();
    }
  }
  if (!found) throw new Error(`no ${selector} rule`);
  return tokens;
}
