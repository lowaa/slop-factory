import { expect, it } from "vitest";

// Node 25+ (the Playwright test image runs 26) defines its own global
// localStorage, undefined without --localstorage-file. Vitest 5's jsdom
// environment replaces it with jsdom's; if that ever stops, tests that spy on
// Storage.prototype (usage/no_storage.test.ts) pass without checking anything.
it("tests see jsdom's web storage, not Node's", () => {
  expect(globalThis.localStorage).toBe(window.localStorage);
  expect(localStorage).toBeInstanceOf(window.Storage);
  expect(sessionStorage).toBeInstanceOf(window.Storage);
  localStorage.setItem("jsdom_storage_probe", "v");
  expect(localStorage.getItem("jsdom_storage_probe")).toBe("v");
  localStorage.removeItem("jsdom_storage_probe");
});
