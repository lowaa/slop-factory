import { describe, expect, it } from "vitest";
import { loginPathReturningTo, returnToSearch } from "./return_to";

describe("loginPathReturningTo", () => {
  it("carries a local path, search included, as ?redirect=", () => {
    const path = loginPathReturningTo("/login", "/tickets/42?tenant=t-1&from=blocked");
    expect(path).toBe("/login?redirect=%2Ftickets%2F42%3Ftenant%3Dt-1%26from%3Dblocked");
    expect(new URL(path, "http://x").searchParams.get("redirect")).toBe("/tickets/42?tenant=t-1&from=blocked");
  });

  it("keeps a search the login path already has", () => {
    const path = new URL(loginPathReturningTo("/login?via=invite", "/tickets/42"), "http://x");
    expect(path.pathname).toBe("/login");
    expect(Object.fromEntries(path.searchParams)).toEqual({ via: "invite", redirect: "/tickets/42" });
  });

  it.each([
    ["no return path", undefined],
    ["the site root", "/"],
    ["a protocol-relative path", "//evil.example"],
    ["a path that normalises to protocol-relative", "/.//evil.example"],
    ["an absolute URL", "https://evil.example/"],
    ["a backslash host", "/\\evil.example"],
    ["a malformed escape", "/tickets/%zz"],
  ])("leaves the login page plain for %s", (_name, returnTo) => {
    expect(loginPathReturningTo("/login", returnTo)).toBe("/login");
  });
});

describe("returnToSearch", () => {
  it("reads ?redirect= as it came, for the caller to check", () => {
    expect(returnToSearch({ redirect: "//evil.example" })).toEqual({ redirect: "//evil.example" });
  });

  it.each([[{}], [{ redirect: "" }], [{ redirect: 42 }]])("is empty for %j", (raw) => {
    expect(returnToSearch(raw)).toEqual({ redirect: undefined });
  });
});
