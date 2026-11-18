import { describe, expect, it } from "vitest";
import { errorType, isBenignError, MESSAGE_WITHHELD, safeErrorMessage, safeErrorStack, safeStack } from "./safe_error";

// A health value and a name, as a thrown message or a parse error would
// carry them.
const SECRET = "Hemoglobin 13.5 g/dL for Jane Doe";

describe("safeErrorMessage", () => {
  it("withholds a message it does not recognise", () => {
    expect(safeErrorMessage(new Error(`bad value: ${SECRET}`))).toBe(MESSAGE_WITHHELD);
    expect(safeErrorMessage(new TypeError(SECRET))).toBe(MESSAGE_WITHHELD);
  });

  it("withholds the input a JSON parse error quotes", () => {
    const err = new SyntaxError(`Unexpected token 'H', "${SECRET}" is not valid JSON`);
    expect(safeErrorMessage(err)).toBe(MESSAGE_WITHHELD);
  });

  it("withholds a thrown non-error whatever it holds", () => {
    expect(safeErrorMessage(SECRET)).toBe(MESSAGE_WITHHELD);
    expect(safeErrorMessage({ message: SECRET })).toBe(MESSAGE_WITHHELD);
  });

  it.each([
    ["Cannot read properties of undefined (reading 'Hemoglobin')", "Cannot read properties of undefined"],
    ["Cannot set properties of null (setting 'x')", "Cannot set properties of null"],
    ["rows.map is not a function", "is not a function"],
    ["secretVariable is not defined", "reference to an undefined variable"],
    ["Failed to fetch", "Failed to fetch"],
  ])("keeps the engine's %j as a fixed string without its variable part", (message, expected) => {
    const out = safeErrorMessage(new TypeError(message));
    expect(out).toBe(expected);
    expect(out).not.toContain("Hemoglobin");
    expect(out).not.toContain("secretVariable");
    expect(out).not.toContain("rows");
  });
});

describe("errorType", () => {
  it("names an error by its class, and a non-error only by what it is", () => {
    expect(errorType(new RangeError("x"))).toBe("RangeError");
    expect(errorType(SECRET)).toBe("NonError:string");
    expect(errorType(null)).toBe("NonError:null");
  });

  it("does not trust a name that isn't a plain identifier", () => {
    const err = new Error("x");
    err.name = SECRET;
    expect(errorType(err)).toBe("Error");
  });
});

describe("isBenignError", () => {
  it("recognises ResizeObserver noise", () => {
    expect(isBenignError(new Error("ResizeObserver loop completed with undelivered notifications."))).toBe(true);
    expect(isBenignError(new Error("boom"))).toBe(false);
  });
});

describe("safeStack", () => {
  const v8 = [
    `Error: ${SECRET}`,
    "    at parse (https://app.example/assets/index-abc123.js:1:2345)",
    "    at https://app.example/assets/index-abc123.js:1:99",
    "    at <anonymous>",
  ].join("\n");

  it("keeps the frames of a V8 stack and drops its message header", () => {
    const out = safeStack(v8)!;
    expect(out).toContain("at parse (https://app.example/assets/index-abc123.js:1:2345)");
    expect(out.split("\n")).toHaveLength(3);
    expect(out).not.toContain("Hemoglobin");
  });

  it("keeps the frames of a Firefox stack", () => {
    const out = safeStack(`parse@https://app.example/assets/index-abc123.js:1:2345\n@https://app.example/assets/x.js:3:4`);
    expect(out!.split("\n")).toHaveLength(2);
  });

  it("drops the continuation lines of a multi-line message", () => {
    const out = safeStack(`Error: line one\n${SECRET}\nsecond line\n    at f (https://a/b.js:1:1)`);
    expect(out).toBe("    at f (https://a/b.js:1:1)");
  });

  it("drops a frame URL's query and fragment, keeping its line and column", () => {
    const out = safeStack(
      "    at f (https://a/assets/i.js?tenant=acme&token=s3cret:10:20)\n    at g (https://a/assets/i.js#frag:3:4)\ng@https://a/x.js?q=Jane:5:6",
    );
    expect(out).toBe("    at f (https://a/assets/i.js:10:20)\n    at g (https://a/assets/i.js:3:4)\ng@https://a/x.js:5:6");
  });

  it("caps the frames and their length", () => {
    const frames = Array.from({ length: 100 }, (_, i) => `    at f${i} (https://a/${"x".repeat(500)}.js:1:1)`);
    const lines = safeStack(frames.join("\n"))!.split("\n");
    expect(lines).toHaveLength(30);
    expect(Math.max(...lines.map((l) => l.length))).toBeLessThanOrEqual(201);
  });

  it("is undefined when there are no frames", () => {
    expect(safeStack(`Error: ${SECRET}`)).toBeUndefined();
    expect(safeStack(undefined)).toBeUndefined();
  });
});

describe("safeErrorStack", () => {
  function errorWith(message: string, frames: string[]): Error {
    const err = new Error(message);
    err.stack = [`Error: ${message}`, ...frames].join("\n");
    return err;
  }

  it("keeps only the frames after the message, so a message can't pose as frames", () => {
    const err = errorWith(`${SECRET}\n    at leak (https://app/assets/${SECRET}:1:2)`, [
      "    at real (https://app/assets/index-abc123.js:7:8)",
    ]);
    expect(safeErrorStack(err)).toBe("    at real (https://app/assets/index-abc123.js:7:8)");
  });

  it("keeps a header-less stack whole", () => {
    const err = new Error(SECRET);
    err.stack = "real@https://app/assets/index-abc123.js:7:8";
    expect(safeErrorStack(err)).toBe("real@https://app/assets/index-abc123.js:7:8");
  });

  it("is undefined for an error with no stack", () => {
    const err = new Error("x");
    err.stack = undefined;
    expect(safeErrorStack(err)).toBeUndefined();
  });
});
