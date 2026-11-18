import type { APIRequestContext, Page, PlaywrightWorkerArgs } from "@playwright/test";
import { test as base } from "@playwright/test";

import { useWithCssCoverage } from "@e2e-support/css_coverage";
import { waitUntilReachable } from "@e2e-support/reachable";

import * as db from "@/support/db";
import { rejectAllConsent, type ConsentStart } from "@/support/consent";
import * as mailhog from "@/support/mailhog";
import { API_BASE_URL, BOARD_ORIGIN, DEV_USER_EMAIL, DEV_USER_PASSWORD, FRONTEND_BASE_URL } from "@/support/env";

// CSRF_HEADER is authn.CSRFHeader (server/internal/authn/csrf.go).
export const CSRF_HEADER = "X-CSRF-Token";

// signedInContext is a request context at baseURL signed in as email,
// sending what the board's own page sends: the board's Origin on every
// request, and the session's CSRF token (server/internal/authn/csrf.go).
//
// Every board and agent route needs a signed-in user, so a context that's
// going to call them has to sign in first. POST /api/auth/login sets the
// session as cookies and answers with the session's CSRF token; a request
// context's headers are fixed when it is made, so the signed-in context is
// a second one, made from the first one's cookies with the token added.
// The token lasts the session (a refresh keeps it), so it is good for the
// whole worker.
async function signedInContext(
  playwright: PlaywrightWorkerArgs["playwright"],
  baseURL: string,
  email: string,
  password: string,
): Promise<APIRequestContext> {
  const signingIn = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Origin: BOARD_ORIGIN } });
  await waitUntilReachable(signingIn, baseURL === API_BASE_URL ? "/healthz" : "/");
  const resp = await signingIn.post("/api/auth/login", { data: { email, password } });
  if (!resp.ok()) {
    throw new Error(`failed to sign in as ${email}: ${resp.status()} ${await resp.text()}`);
  }
  const { csrf_token: csrfToken } = (await resp.json()) as { csrf_token: string };
  const storageState = await signingIn.storageState();
  await signingIn.dispose();
  return playwright.request.newContext({
    baseURL,
    storageState,
    // No pooled sockets: the server closes an idle keep-alive connection
    // after 120s (go-web/server), and a POST that reuses one just as it
    // closes gets a reset ("socket hang up") Playwright does not retry.
    extraHTTPHeaders: { Origin: BOARD_ORIGIN, [CSRF_HEADER]: csrfToken, Connection: "close" },
  });
}

// signedInApiContext is exported for a spec that needs a *second*
// identity's API context of its own -- tenancy.spec.ts's second tenant,
// activated via `anonPage` but then driven at the API level rather than
// through the browser. `api`/`frontendApi` below are this same shape, just
// worker-scoped and always the seeded dev user.
export async function signedInApiContext(
  playwright: PlaywrightWorkerArgs["playwright"],
  email: string,
  password: string,
): Promise<APIRequestContext> {
  return signedInContext(playwright, API_BASE_URL, email, password);
}

// boardWriteHeaders is what a write from `request` -- a page's own request
// context, which shares the page's cookies but sends no Origin and knows no
// CSRF token -- needs to be made as the board's page would make it: the
// board's Origin, and the session's token, read from /api/auth/me the way
// the page reads it. Signed out, just the Origin.
export async function boardWriteHeaders(request: APIRequestContext): Promise<Record<string, string>> {
  const me = await request.get("/api/auth/me");
  if (!me.ok()) return { Origin: BOARD_ORIGIN };
  const { csrf_token: csrfToken } = (await me.json()) as { csrf_token: string };
  return { Origin: BOARD_ORIGIN, [CSRF_HEADER]: csrfToken };
}

interface WorkerFixtures {
  api: APIRequestContext;
  frontendApi: APIRequestContext;
}

interface TestFixtures {
  cleanDatabase: void;
  anonPage: Page;
  consentStart: ConsentStart;
}

// `workers: 1` in playwright.config.ts keeps this suite serial --
// cleanDatabase's TRUNCATE below is global, so tests running in parallel
// workers would stomp on each other's tickets if that config value ever
// changed.
//
// `api`/`frontendApi`/`page` are all signed in as the seeded dev user (see
// ../../docker-compose-dev.yaml's `seed` service) and log in exactly once
// per worker, never once per test: POST /api/auth/login is rate-limited to
// 10 attempts per 15 minutes per address (server/cmd/api/main.go's
// LoginLimit), and this suite runs far more than 10 tests against that one
// address. A spec that specifically needs a signed-*out* browser -- the
// register/activate/login flow itself, or a 401/redirect check -- takes
// `anonPage` instead.
export const test = base.extend<TestFixtures, WorkerFixtures>({
  // See support/consent.ts. A spec about the banner itself sets
  // test.use({ consentStart: "none" }).
  consentStart: ["reject", { option: true }],

  // The built-in context, with the visitor's consent already decided before
  // anything is signed in or opened: the cookie is the anonymous one, which
  // the server merges into the signed-in user's record on the first read.
  context: async ({ context, consentStart }, use) => {
    if (consentStart === "reject") {
      await waitUntilReachable(context.request, "/");
      await rejectAllConsent(context);
    }
    await use(context);
  },

  api: [
    async ({ playwright }, use) => {
      const ctx = await signedInContext(playwright, API_BASE_URL, DEV_USER_EMAIL, DEV_USER_PASSWORD);
      await use(ctx);
      await ctx.dispose();
    },
    { scope: "worker" },
  ],

  // An API request context through the *frontend's* origin (its nginx,
  // proxying /api/* to the api service -- see
  // ../../../frontend/nginx.conf.template) rather than straight to the api
  // service the way `api` does. one_origin.spec.ts uses this to prove that
  // routing; `page`'s fixture below also borrows its session cookies, since
  // both share the frontend's origin and this signs in exactly once.
  frontendApi: [
    async ({ playwright }, use) => {
      const ctx = await signedInContext(playwright, FRONTEND_BASE_URL, DEV_USER_EMAIL, DEV_USER_PASSWORD);
      await use(ctx);
      await ctx.dispose();
    },
    { scope: "worker" },
  ],

  // A signed-in browser tab per test -- see tickets.spec.ts. Depends on the
  // built-in `context` fixture (not the built-in `page`) so cookies can be
  // added before the first navigation, and so Playwright's own tracing --
  // wired up at the context level, see playwright.config.ts's
  // `trace: "retain-on-failure"` -- still covers it.
  //
  // The cookies come from `frontendApi`'s already-signed-in session
  // (storageState() reads its cookie jar) rather than a fresh login: see
  // this file's own doc comment on why a login per test would run into the
  // rate limit long before an 11-spec suite finishes.
  page: async ({ context, frontendApi }, use, testInfo) => {
    const state = await frontendApi.storageState();
    await context.addCookies(state.cookies);
    const page = await context.newPage();
    await waitUntilReachable(page.request, "/");
    await useWithCssCoverage(page, testInfo, use);
  },

  // A signed-*out* browser tab, for the one kind of spec `page` cannot
  // serve: register/activate/login itself, and anything asserting what an
  // anonymous visitor sees. Do not use `page` and `anonPage` in the same
  // test -- they both resolve to the one `context` fixture Playwright
  // creates per test, so `page`'s addCookies above would leave `anonPage`
  // signed in too.
  anonPage: async ({ context }, use, testInfo) => {
    const page = await context.newPage();
    await waitUntilReachable(page.request, "/");
    await useWithCssCoverage(page, testInfo, use);
  },

  // Truncates every tenant-scoped table before each test so tests never
  // observe tickets, agents or chat left behind by a previous test --
  // *including* the ones cleanDatabase itself creates by signing `api`/
  // `frontendApi` in, since agents/tickets/etc. are the seeded dev user's
  // tenant's data, not global. Table list mirrors
  // server/internal/schema/migrations/000172_baseline.sql's non-`global_`
  // tables (everything with a tenant_id and RLS), plus global_oauth_grants
  // and global_oauth_auth_requests -- global by name (a token/request
  // arrives before any tenant is known, see 000009_oauth.sql's doc
  // comment) but still one tenant's agent's grant (or a request headed for
  // one), so they have to go too. CASCADE reaches global_oauth_codes,
  // global_oauth_device_codes and global_oauth_tokens on its own via their
  // FK to grant_id -- see 000009_oauth.sql -- so they're not listed
  // separately. global_oauth_clients is left out the same way global_users
  // is: it holds the seeded "slop-haus-workspace" client every test run
  // needs to still exist (000009_oauth.sql's own INSERT), not per-test
  // data.
  //
  // 000009_oauth.sql dropped global_agent_credentials outright (static
  // agent bearer tokens are gone, replaced by the OAuth grants above) --
  // this table list is what changed to match, since a stale TRUNCATE of a
  // table that no longer exists fails outright rather than just missing
  // something.
  //
  // What is *not* truncated: global_users, global_tenants and
  // global_tenant_memberships, because the seeded dev user (and its
  // tenant) has to survive every test -- signing `api`/`frontendApi`/`page`
  // in again next test depends on it still existing. Instead, any *other*
  // user a registration/tenancy spec created is deleted by email, which
  // cascades to that user's own memberships and auth tokens; a tenant left
  // with no members after that (i.e. not the dev user's) is deleted too, so
  // tenancy.spec.ts's "both tenants' first ticket is #1" sees a fresh
  // ticket_sequences row for a brand new tenant every time, never a reused
  // one from an earlier test.
  //
  // `tenant_settings` is named explicitly because it is a table CASCADE does
  // not reach: it has no foreign key to agents or tickets. Left out,
  // a single test that pauses ticket processing would leave the fleet
  // paused for every test after it, and those would fail somewhere far
  // from the cause. harness_config's defaults row and the revision
  // counter are the same: neither hangs off an agent.
  cleanDatabase: [
    async ({ api }, use) => {
      await api.get("/healthz");
      await db.execute(
        "TRUNCATE TABLE " +
          "global_oauth_grants, global_oauth_auth_requests, ticket_sequences, agents, " +
          "ticket_comments, ticket_deps, ticket_paths, agent_contexts, tickets, " +
          "locks, leases, sessions, tenant_settings, " +
          "harness_config, harness_config_revisions, " +
          "fleet_instruction_defaults, fleet_instruction_versions, " +
          "intake_sources, intake_events, intake_deliveries, codehost_credentials " +
          "RESTART IDENTITY CASCADE",
      );
      // The truncate took every tenant's seeded sensitive word list with it
      // (the global_tenants_seed_sensitive_words trigger seeds one at tenant creation, not at use).
      await db.execute("SELECT seed_sensitive_words(id) FROM global_tenants");
      // Intake sources' credentials are global too; their sources, events and
      // deliveries are truncated above.
      await db.execute("DELETE FROM global_intake_credentials");
      // So is the dev user's linked GitHub identity.
      await db.execute("DELETE FROM global_user_identities WHERE provider = 'github'");
      // The consent record refuses deletes (and its users' deletes) by design,
      // so a test run clears it the one way the database allows: as the
      // owner, with the append-only triggers off for the length of one
      // transaction, so a failure leaves them on.
      await db.transaction([
        ["ALTER TABLE global_consent_events DISABLE TRIGGER USER"],
        ["DELETE FROM global_consent_events"],
        ["DELETE FROM global_consent_merge_tokens"],
        ["ALTER TABLE global_consent_events ENABLE TRIGGER USER"],
      ]);
      await db.execute("DELETE FROM global_users WHERE email <> $1", [DEV_USER_EMAIL]);
      await db.execute(
        "DELETE FROM global_tenants WHERE id NOT IN (SELECT tenant FROM global_tenant_memberships)",
      );
      await mailhog.clear();
      await use();
    },
    { auto: true },
  ],
});

export { expect } from "@playwright/test";
export { DATABASE_URL } from "@/support/env";
