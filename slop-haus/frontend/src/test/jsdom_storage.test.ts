/// <reference types="vitest/jsdom" />
import { expect, it } from "vitest";

// Node 25+ (the Playwright test image runs 26) defines its own global
// localStorage, undefined without --localstorage-file. Vitest 5's jsdom
// environment replaces it with jsdom's; if that ever stops, tests that spy on
// Storage.prototype (usage/no_storage.test.ts) pass without checking anything.
// Compared against `jsdom.window`, the JSDOM instance's own window: the test
// globals' `window` is globalThis, so it would agree with whatever is there.
it("tests see jsdom's web storage, not Node's", () => {
  expect(localStorage).toBe(jsdom.window.localStorage);
  expect(sessionStorage).toBe(jsdom.window.sessionStorage);
  expect(Storage).toBe(jsdom.window.Storage);
  localStorage.setItem("jsdom_storage_probe", "v");
  expect(localStorage.getItem("jsdom_storage_probe")).toBe("v");
  localStorage.removeItem("jsdom_storage_probe");
});
