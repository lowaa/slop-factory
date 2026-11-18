import { describe, expect, it } from "vitest";
import { positiveIntParam, uuidParam } from "./params";

describe("positiveIntParam", () => {
  const { parse, stringify } = positiveIntParam("ticketId");

  it("parses a positive integer into a number", () => {
    expect(parse({ ticketId: "42" })).toEqual({ ticketId: 42 });
    expect(parse({ ticketId: "999999999999999" })).toEqual({ ticketId: 999999999999999 });
  });

  it.each(["", "0", "-1", "007", "1.5", "1e3", " 4", "4 ", "0x10", "abc", "1000000000000000"])(
    "refuses %j",
    (raw) => {
      expect(parse({ ticketId: raw })).toBe(false);
    },
  );

  it("keeps the route's other params as they are", () => {
    const withOrg = positiveIntParam<"itemId", "orgId">("itemId");
    expect(withOrg.parse({ orgId: "acme", itemId: "42" })).toEqual({ orgId: "acme", itemId: 42 });
    expect(withOrg.stringify({ orgId: "acme", itemId: 42 })).toEqual({ orgId: "acme", itemId: "42" });
  });

  it("stringifies back to the same digits", () => {
    expect(stringify({ ticketId: 42 })).toEqual({ ticketId: "42" });
  });
});

describe("uuidParam", () => {
  const { parse, stringify } = uuidParam("id");
  const canonical = "0f8fad5b-d9cb-469f-a165-70867728950e";

  it("accepts a canonical UUID, as it is", () => {
    expect(parse({ id: canonical })).toEqual({ id: canonical });
    expect(parse({ id: canonical.toUpperCase() })).toEqual({ id: canonical.toUpperCase() });
  });

  it.each([
    ["empty", ""],
    ["too short", canonical.slice(0, -1)],
    ["too long", `${canonical}0`],
    ["a non-hex character", canonical.replace("d9cb", "d9cg")],
    ["no hyphens", canonical.replaceAll("-", "")],
    ["a hyphen missing", canonical.replace("-d9cb", "d9cb")],
    ["an extra hyphen", canonical.replace("d9cb", "d9-cb")],
    ["a hyphen out of place", "0f8fad5bd-9cb-469f-a165-70867728950e"],
    ["braces", `{${canonical}}`],
    ["surrounding space", ` ${canonical}`],
    ["a path in it", `${canonical}/edit`],
  ])("refuses %s", (_, raw) => {
    expect(parse({ id: raw })).toBe(false);
  });

  it("keeps the route's other params as they are", () => {
    const withOrg = uuidParam<"id", "orgId">("id");
    expect(withOrg.parse({ orgId: "acme", id: canonical })).toEqual({ orgId: "acme", id: canonical });
    expect(withOrg.stringify({ orgId: "acme", id: canonical })).toEqual({ orgId: "acme", id: canonical });
  });

  it("stringifies back to the path unchanged", () => {
    const upper = canonical.toUpperCase();
    expect(stringify({ id: canonical })).toEqual({ id: canonical });
    const parsed = parse({ id: upper });
    expect(parsed && stringify(parsed)).toEqual({ id: upper });
  });
});
