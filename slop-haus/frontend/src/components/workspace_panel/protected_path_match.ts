// Mirrors the server's matcher for path rules (ticket.PathRuleMatches): a rule
// ending in "/" covers everything under that directory, otherwise it is a Go
// path.Match pattern over a file, where * and ? never cross a /. The server
// decides what is protected; this only lets the owner preview a rule.

function patternToRegExp(pattern: string): RegExp | null {
  let out = "";
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i];
    if (c === "*") out += "[^/]*";
    else if (c === "?") out += "[^/]";
    else if (c === "\\") {
      i++;
      if (i >= pattern.length) return null;
      out += pattern[i].replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    } else if (c === "[") {
      const end = pattern.indexOf("]", i + 2);
      if (end < 0) return null;
      let cls = pattern.slice(i + 1, end);
      if (cls.startsWith("^")) cls = "^" + cls.slice(1).replace(/[\]\\^]/g, "\\$&");
      else cls = cls.replace(/[\]\\^]/g, "\\$&");
      out += `[${cls}]`;
      i = end;
    } else out += c.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }
  try {
    return new RegExp(`^${out}$`);
  } catch {
    return null;
  }
}

export function pathRuleMatches(rule: string, rel: string): boolean {
  if (rule.endsWith("/")) return rel.startsWith(rule);
  if (rel.endsWith("/")) return false;
  return patternToRegExp(rule)?.test(rel) ?? false;
}

export interface PathRule {
  path: string;
}

// firstMatchingRule is the first rule covering the path, as the server reads
// the list, or undefined. Blank rules (an Add not yet filled) never match.
export function firstMatchingRule<T extends PathRule>(rules: T[], rel: string): T | undefined {
  const p = rel.trim().replace(/^\.?\//, "");
  if (p === "") return undefined;
  return rules.find((r) => r.path.trim() !== "" && pathRuleMatches(r.path.trim(), p));
}
