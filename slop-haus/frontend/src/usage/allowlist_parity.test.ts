import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { ALLOWLIST_PATH } from "./allowlist_path";
import { CATALOG } from "./catalog";
import { allowlistJson } from "./allowlist_json";

// The server enforces internal/usagemetrics/ui_allowlist.json on what it
// counts, so it must say what the catalog says.
describe("the server's allowlist", () => {
  it("is the catalog's, as checked in (npm run generate:usage_allowlist)", () => {
    expect(readFileSync(ALLOWLIST_PATH, "utf8")).toBe(allowlistJson());
  });

  it("holds every event and attribute of the catalog and nothing more", () => {
    const checkedIn = JSON.parse(readFileSync(ALLOWLIST_PATH, "utf8")) as { events: Record<string, Record<string, unknown>> };
    expect(Object.keys(checkedIn.events).sort()).toEqual(Object.keys(CATALOG).sort());
    for (const [name, attrs] of Object.entries(CATALOG)) {
      expect(Object.keys(checkedIn.events[name]).sort()).toEqual(Object.keys(attrs).sort());
    }
  });
});
