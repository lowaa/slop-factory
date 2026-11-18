import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { parseStatus, parseStatusList } from "./status";

describe("parseStatus", () => {
  it.each(["preparing", "parked", "open", "in_progress", "blocked", "discussion", "review", "done", "cancelled"])("accepts %s", (s) => {
    expect(parseStatus(s)).toBe(s);
  });
  it.each(["", "Open", "nope", "toString", "__proto__", null, undefined])("rejects %j", (v) => {
    expect(parseStatus(v)).toBeUndefined();
  });
});

describe("parseStatusList", () => {
  it("wraps a single value", () => {
    expect(parseStatusList("open")).toEqual(["open"]);
  });
  it("drops unknown entries and keeps order", () => {
    expect(parseStatusList(["done", "bogus", "open"])).toEqual(["done", "open"]);
  });
  it("is empty for nothing or only invalid input", () => {
    expect(parseStatusList(undefined)).toEqual([]);
    expect(parseStatusList("bogus")).toEqual([]);
  });
});

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (name === "api_generated") return [];
    if (statSync(p).isDirectory()) return sources(p);
    return /\.(ts|tsx)$/.test(name) && !/\.(test|stories)\./.test(name) ? [p] : [];
  });
}

describe("status casts", () => {
  it("leaves no `as Status` in production code", () => {
    // parseStatus is the one place the cast is earned, by the membership check.
    const parser = join(__dirname, "status.ts");
    const offenders = sources(join(__dirname, "..")).filter((f) => f !== parser).filter((f) => /\bas Status(\[\])?\b/.test(readFileSync(f, "utf8")));
    expect(offenders).toEqual([]);
  });
});
