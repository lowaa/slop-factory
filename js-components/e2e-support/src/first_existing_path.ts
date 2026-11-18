import { existsSync } from "node:fs";

// The first candidate that exists. Files an e2e image bakes in beside the
// tests sit at a different place from where a run from the checkout finds
// them, and an env var can override both; undefined candidates are skipped.
export function firstExistingPath(what: string, candidates: readonly (string | undefined)[]): string {
  const found = candidates.find((p): p is string => p !== undefined && existsSync(p));
  if (!found) {
    throw new Error(`${what} not found (tried ${candidates.filter((p) => p !== undefined).join(", ")})`);
  }
  return found;
}
