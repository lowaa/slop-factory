import { describe, expect, it } from "vitest";
import { signInReturnPath } from "./auth_routes";

describe("signInReturnPath", () => {
  it("keeps a local path, search and all", () => {
    expect(signInReturnPath("/tickets/7?from=blocked&tenant=t")).toBe("/tickets/7?from=blocked&tenant=t");
  });

  it.each([
    ["missing", undefined],
    ["another site", "//evil.example"],
    ["another site, behind a dot segment", "/.//evil.example"],
    ["an absolute URL", "https://evil.example/"],
    ["the login page", "/login?redirect=%2Fagents"],
    ["the register page", "/register"],
    ["the forgot-password page", "/forgot-password#x"],
  ])("is the board for %s", (_name, raw) => {
    expect(signInReturnPath(raw)).toBe("/");
  });

  // The server refuses a provider sign-in whose next is longer outright.
  it("is the board for a path longer than the server takes as next", () => {
    const at = (length: number) => `/tickets/7?q=${"a".repeat(length - "/tickets/7?q=".length)}`;
    expect(signInReturnPath(at(2048))).toBe(at(2048));
    expect(signInReturnPath(at(2049))).toBe("/");
  });
});
