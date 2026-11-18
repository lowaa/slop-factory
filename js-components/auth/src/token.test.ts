import { afterEach, describe, expect, it } from "vitest";
import { readAndClearToken } from "./token";

afterEach(() => {
  window.history.replaceState({}, "", "/");
});

describe("readAndClearToken", () => {
  it("reads the token from the URL fragment", () => {
    window.history.replaceState({}, "", "/activate#token=abc123");

    expect(readAndClearToken()).toBe("abc123");
  });

  it("scrubs the fragment from the address bar", () => {
    window.history.replaceState({}, "", "/activate#token=abc123");

    readAndClearToken();

    expect(window.location.hash).toBe("");
    expect(window.location.search).toBe("");
    expect(window.location.pathname).toBe("/activate");
  });

  it("preserves the query string while scrubbing the fragment", () => {
    window.history.replaceState({}, "", "/activate?next=/dashboard#token=abc123");

    expect(readAndClearToken()).toBe("abc123");
    expect(window.location.hash).toBe("");
    expect(window.location.search).toBe("?next=/dashboard");
  });

  it("does not read a query-string token", () => {
    window.history.replaceState({}, "", "/activate?token=abc123");

    expect(readAndClearToken()).toBeNull();
  });

  it("returns null when there is no token", () => {
    window.history.replaceState({}, "", "/activate");

    expect(readAndClearToken()).toBeNull();
  });
});
