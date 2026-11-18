// Runs before every test file (see vitest.config.ts's test.setupFiles).
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// testing-library only auto-cleans under Jest's global afterEach; vitest
// needs it explicitly or DOM from one test leaks into the next.
afterEach(() => {
  cleanup();
});
