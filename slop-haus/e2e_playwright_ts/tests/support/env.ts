import { readFileSync } from "node:fs";
import path from "node:path";

// slop-haus/server has no HTTP UI itself, so api.spec.ts drives it with
// Playwright's API request context; tickets.spec.ts drives
// slop-haus/frontend with a real browser page instead.
// The env vars are what test.sh's dockerized run sets (compose DNS names).
// These fallbacks are only for driving the suite from the host against a
// ./dev_env.sh stack, so they track that stack's published ports -- see
// ../../docker-compose-dev.yaml for why those are a block nothing else in
// the repo uses.
export const API_BASE_URL = process.env.API_BASE_URL ?? "http://localhost:8190";
export const FRONTEND_BASE_URL = process.env.FRONTEND_BASE_URL ?? "http://localhost:5273";
// The api's GitHub webhook listener, a port of its own (WEBHOOK_LISTEN_ADDR
// in ../../docker-compose-dev.e2e.yaml). The interactive dev stack runs
// without one.
export const WEBHOOK_BASE_URL = process.env.WEBHOOK_BASE_URL ?? "http://localhost:8191";
// What the api was told its webhook door is publicly (WEBHOOK_PUBLIC_BASE_URL),
// which it gives GitHub for the App's webhook.
export const WEBHOOK_PUBLIC_BASE_URL = process.env.WEBHOOK_PUBLIC_BASE_URL ?? "http://api:8081";
// The api's intake listener (INTAKE_LISTEN_ADDR), likewise a port of its own.
export const INTAKE_BASE_URL = process.env.INTAKE_BASE_URL ?? "http://localhost:8192";

// The fake GitHub API the api is pointed at (GITHUB_API_BASE_URL), which the
// suite scripts and reads back; see support/fake_github_server.mjs. The
// interactive dev stack runs without one.
export const FAKE_GITHUB_URL = process.env.FAKE_GITHUB_URL ?? "http://localhost:8193";

// BOARD_ORIGIN is the origin the api accepts browser writes from:
// PUBLIC_BASE_URL's, which both stacks point at the frontend (see
// ../../docker-compose-dev.e2e.yaml). The real browser sends it as Origin by
// itself; a request context sends nothing of the kind, so the contexts that
// write as a browser would (support/fixtures.ts) name it -- see
// server/internal/authn/csrf.go's BrowserGuard.
export const BOARD_ORIGIN = new URL(FRONTEND_BASE_URL).origin;

// The owner role's DSN, same one the api service itself is given (see
// ../../../docker-compose-dev.yaml) -- it runs migrations and creates a
// separate, least-privileged app role at startup that every request is
// then served as. RLS binds that app role, not this one, so a TRUNCATE
// here (see fixtures.ts's cleanDatabase) sees every tenant's rows rather
// than being scoped away by the policies the app role runs under.
export const DATABASE_URL =
  process.env.DATABASE_URL ?? "postgres://slophaus:devpassword@localhost:5433/slophaus?sslmode=disable";

// MailHog's HTTP API (see support/mailhog.ts), for reading the
// activation/password-reset links auth.spec.ts and tenancy.spec.ts trigger.
// The host fallback is MailHog's UI port from ../../docker-compose-dev.yaml
// (8126 on the host, mapped to its own 8025 inside the container) -- the
// UI and the HTTP API this actually calls share that one port.
export const MAILHOG_URL = process.env.MAILHOG_URL ?? "http://localhost:8126";

// The seeded dev account (see ../../docker-compose-dev.yaml's `seed`
// service): already active, so fixtures.ts can sign `api`/`page` in with it
// directly instead of every test running its own register/activate dance.
// Defaults match `seed`'s own DEV_USER_EMAIL/DEV_USER_PASSWORD defaults.
export const DEV_USER_EMAIL = process.env.DEV_USER_EMAIL ?? "dev@slop-haus.local";
export const DEV_USER_PASSWORD = process.env.DEV_USER_PASSWORD ?? "slop-haus-dev-password";

// newSessionID mints the id a test presents as its Claude run.
//
// One per test, reused by that test's SessionStart *and* its Stop calls: a
// real agent's Stop hook carries the id its SessionStart bound, and a
// mismatch is what the server reads as "this run has been superseded" --
// so it lets the caller stop rather than putting it back to work. Tests
// that invented a second id here silently stopped exercising the decision
// table at all.
export function newSessionID(): string {
  return `claude-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

// What a real Claude Code sent a hook (server/internal/hookserver/testdata/claude, captured by
// harness_e2e/test.sh --capture-hook-payloads), with this test's own session.
export function claudeHookPayload(event: string, sessionID: string): Record<string, unknown> {
  const dir = process.env.HOOK_PAYLOADS_DIR ?? new URL("../../../server/internal/hookserver/testdata/claude", import.meta.url).pathname;
  const payload = JSON.parse(readFileSync(path.join(dir, `${event}.json`), "utf8"));
  return { ...payload, session_id: sessionID };
}
