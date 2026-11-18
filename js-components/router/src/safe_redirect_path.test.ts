import { describe, expect, it } from "vitest";
import caseFile from "../../../go-auth/login/testdata/local_redirect_cases.json";
import { safeRedirectPath } from "./safe_redirect_path";

// A row of the case file go-auth/login's TestLocalRedirect_Table runs too:
// each function's result for the input, or null where it refuses it.
interface RedirectCase {
  name: string;
  input: string;
  go: string | null;
  ts: string | null;
  note?: string;
}

const cases: RedirectCase[] = caseFile.cases;

// Returned only for a refusal: no path can equal it, so "/" as a result
// still reads as an accepted "/".
const REFUSED = "\u0000refused";

// Where the app is when it follows the path. Its own path, query and
// fragment are there so a result that resolved relative to them, rather
// than to the origin, would show up.
const BASE = "https://app.example/some/page?from=base#frag";

describe("safeRedirectPath", () => {
  it.each(cases.map((c) => [c.name, c] as const))("gives the shared case file's result for %s", (_name, c) => {
    expect(safeRedirectPath(c.input, REFUSED)).toBe(c.ts ?? REFUSED);
  });

  // The promise this function makes to the server: it accepts a subset of
  // what go-auth's LocalRedirect accepts, and a path both accept leads to
  // the same page on this site either way. The Go column is what
  // LocalRedirect returns, as that function's own test checks.
  it.each(cases.map((c) => [c.name, c] as const))("only accepts what the server's LocalRedirect does, to the same place: %s", (_name, c) => {
    const ours = safeRedirectPath(c.input, REFUSED);
    if (ours === REFUSED) return;

    expect(c.go, "accepted here but refused by LocalRedirect").not.toBeNull();
    const here = new URL(ours, BASE);
    const server = new URL(c.go ?? "", BASE);
    expect(here.origin).toBe(new URL(BASE).origin);
    expect(here.href).toBe(server.href);
  });

  it.each([[undefined], [null], [42], [{ to: "/" }], [["/"]]])("rejects the non-string %j", (raw) => {
    expect(safeRedirectPath(raw)).toBe("/");
  });

  it("returns the given fallback for a rejected path", () => {
    expect(safeRedirectPath("//evil.example", "/login")).toBe("/login");
  });
});
