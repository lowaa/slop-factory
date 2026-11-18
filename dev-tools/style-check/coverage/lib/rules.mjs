// Reads a stylesheet's style rules with their offsets, and turns a rule's
// selectors into the keys the report matches them by.
import postcss from "postcss";
import selectorParser from "postcss-selector-parser";

// Pseudo-classes that need a user to do something (or the browser to be in
// some state) which a story does not show. A rule using one is reachable
// when the rest of its selector matches something, even if Chrome never saw
// it apply.
const DYNAMIC_PSEUDO_CLASSES = new Set([
  ":hover",
  ":active",
  ":focus",
  ":focus-visible",
  ":focus-within",
  ":visited",
  ":link",
  ":target",
  ":autofill",
  ":-webkit-autofill",
  ":user-invalid",
  ":user-valid",
  ":fullscreen",
  ":popover-open",
  ":open",
]);
// The single-colon spellings of pseudo-elements.
const LEGACY_PSEUDO_ELEMENTS = new Set([":before", ":after", ":first-line", ":first-letter"]);

// The at-rules a rule sits in, outermost first, as one string:
// "@media (max-width:600px)". Whitespace is squashed so the same query
// written two ways in the dev and built CSS compares equal.
function contextOf(node) {
  const parts = [];
  for (let p = node.parent; p && p.type !== "root"; p = p.parent) {
    if (p.type === "atrule") parts.unshift(`@${p.name} ${squash(p.params)}`.trim());
  }
  return parts.join(" > ");
}

// Whitespace that no CSS reads differently, so the dev and built spellings
// of one query or selector give one key.
const squash = (s) => s.replace(/\s*([:,()])\s*/g, "$1").replace(/\s+/g, " ").trim();
export const normaliseSelector = (s) => s.replace(/\s*([>+~,])\s*/g, "$1").replace(/\s+/g, " ").trim();

// Every style rule of the sheet: { start, line, column, context, selectors }.
// Rules inside @keyframes are not style rules in the browser's sense and
// are skipped; so are @font-face and the like, which have no selector.
export function parseRules(text) {
  const rules = [];
  postcss.parse(text).walkRules((rule) => {
    for (let p = rule.parent; p; p = p.parent) {
      if (p.type === "atrule" && /keyframes$/i.test(p.name)) return;
    }
    rules.push({
      start: rule.source.start.offset,
      line: rule.source.start.line,
      column: rule.source.start.column,
      context: contextOf(rule),
      selectors: rule.selectors.map((sel) => sel.trim()),
    });
  });
  return rules;
}

function transform(selector, fn) {
  let out = selector;
  selectorParser((root) => {
    fn(root);
    out = root.toString();
  }).processSync(selector);
  return out;
}

// The selector with the dynamic parts taken off: what an element has to
// match for the rule to apply once the state it needs is reached.
// Returns null when nothing dynamic was removed (nothing new to test).
export function staticSelector(selector) {
  let removed = false;
  const result = transform(selector, (root) => {
    root.walkPseudos((p) => {
      const value = p.value.toLowerCase();
      const dynamic = value.startsWith("::") || LEGACY_PSEUDO_ELEMENTS.has(value) || DYNAMIC_PSEUDO_CLASSES.has(value);
      // Inside :not() or :is() the state is part of what is asked, not a
      // state to be reached.
      if (!dynamic || p.parent?.parent?.type !== "root") return;
      removed = true;
      p.remove();
    });
    // A compound left empty (".a > :hover") needs a universal selector.
    root.walk((n) => {
      if (n.type === "selector" && n.nodes.length > 0 && n.last.type === "combinator") n.append(selectorParser.universal());
      if (n.type === "selector" && n.nodes.length === 0) n.append(selectorParser.universal());
    });
  });
  return removed ? result.trim() : null;
}

export const ruleKey = (context, selector) => `${context}\u0000${normaliseSelector(selector)}`;
