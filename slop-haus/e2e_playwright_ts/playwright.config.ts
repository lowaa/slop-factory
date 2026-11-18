import { defineConfig } from "@playwright/test";

import { FRONTEND_BASE_URL } from "@/support/env";

// The lists-at-scale render guard needs a database seeded with 5,000 tickets
// (test.sh's SCALE=1 does that and sets this), and its seed would be wiped
// by the next spec's cleanDatabase, so it is a project of its own that a
// plain `playwright test` does not know about.
const SCALE_SPEC = "lists_at_scale.spec.ts";
const scale = process.env.SCALE_TESTS === "1";

export default defineConfig({
  projects: [
    { name: "e2e", testDir: "./tests", testIgnore: SCALE_SPEC },
    ...(scale ? [{ name: "scale", testDir: "./tests", testMatch: SCALE_SPEC }] : []),
    // The shared support code's own specs (js-components/e2e-support): pure,
    // no stack, and run here so a change to it cannot merge unrun. test.sh's
    // SHARDS names both e2e projects, splitting only e2e, so a project added
    // here needs adding there too.
    { name: "e2e_support", testDir: "./e2e_support" },
  ],
  // fixtures.ts's cleanDatabase TRUNCATEs the whole database before every
  // test, which is only safe if tests never run concurrently against the
  // same database. Concurrency comes from test.sh's SHARDS instead, a stack
  // per shard.
  workers: 1,
  // Not parallelism (one worker): it lets --shard split by test rather than
  // by file, whose runtimes vary too much to balance -- by file, 6 shards
  // ran from 18s to 3.2m. Safe because every test starts from a truncated
  // database; a describe that relies on order declares mode "serial", which
  // keeps it together.
  fullyParallel: true,
  // No retries, on CI too: a retried pass hides a flake, and a flake that
  // fails the check gets filed as a ticket
  // instead of staying invisible.
  retries: 0,
  forbidOnly: !!process.env.CI,
  reporter: [["list"]],
  timeout: 60_000,
  // A just-filed or moved ticket reaches its board column only after a
  // reload round trip to the backend; on a loaded CI runner that exceeded
  // Playwright's 5s default (pipeline 11350, tickets.spec.ts).
  expect: { timeout: 15_000 },
  use: {
    baseURL: FRONTEND_BASE_URL,
    trace: "retain-on-failure",
  },
});
