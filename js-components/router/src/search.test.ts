import { describe, expect, it } from "vitest";
import { noSearch, optionalOneOf, optionalString, parseSearch, stringifySearch } from "./search";

describe("parseSearch", () => {
  it("keeps every value a string, however much it looks like JSON", () => {
    expect(parseSearch("?n=42&flag=true&nothing=null&list=%5B1%5D&code=007")).toEqual({
      n: "42",
      flag: "true",
      nothing: "null",
      list: "[1]",
      code: "007",
    });
  });

  it("decodes values and takes the first of a repeated key", () => {
    expect(parseSearch("?agent=Ada%20L&agent=other")).toEqual({ agent: "Ada L" });
  });

  it("keeps keys that are also Object.prototype's names", () => {
    const parsed = parseSearch("?constructor=a&toString=b&__proto__=c");
    expect(Object.entries(parsed)).toEqual([
      ["constructor", "a"],
      ["toString", "b"],
      ["__proto__", "c"],
    ]);
  });

  it("parses an empty search as no params", () => {
    expect(parseSearch("")).toEqual({});
    expect(parseSearch("?")).toEqual({});
  });
});

describe("stringifySearch", () => {
  it("writes values as they are, without JSON quoting", () => {
    expect(stringifySearch({ n: "42", request: "abc-123" })).toBe("?n=42&request=abc-123");
  });

  it("leaves out undefined and null values, and writes nothing for no params", () => {
    expect(stringifySearch({ a: undefined, b: null })).toBe("");
  });

  it("round-trips through parseSearch", () => {
    const search = "?code=ABCD-1234&agent=a%20b%26c";
    expect(stringifySearch(parseSearch(search))).toBe("?code=ABCD-1234&agent=a+b%26c");
    expect(parseSearch(stringifySearch(parseSearch(search)))).toEqual(parseSearch(search));
  });
});

describe("optionalString", () => {
  it("returns a non-empty string value", () => {
    expect(optionalString({ request: "r-1" }, "request")).toBe("r-1");
  });

  it("returns undefined for a missing, empty or non-string value", () => {
    expect(optionalString({}, "request")).toBeUndefined();
    expect(optionalString({ request: "" }, "request")).toBeUndefined();
    expect(optionalString({ request: 42 }, "request")).toBeUndefined();
  });
});

describe("noSearch", () => {
  it("drops every param", () => {
    const validate: (raw: Record<string, unknown>) => unknown = noSearch;
    expect(validate({ request: "r-1", extra: "x" })).toEqual({});
  });
});

describe("optionalOneOf", () => {
  const TABS = ["board", "blocked"] as const;

  it("returns a value that is one of the allowed ones", () => {
    expect(optionalOneOf({ from: "blocked" }, "from", TABS)).toBe("blocked");
  });

  it("returns undefined for a missing, unknown or non-string value", () => {
    expect(optionalOneOf({}, "from", TABS)).toBeUndefined();
    expect(optionalOneOf({ from: "Blocked" }, "from", TABS)).toBeUndefined();
    expect(optionalOneOf({ from: "" }, "from", TABS)).toBeUndefined();
    expect(optionalOneOf({ from: ["board"] }, "from", TABS)).toBeUndefined();
  });
});
