import type { Browser, BrowserContext, Page } from "@playwright/test";
import { expect } from "@playwright/test";

import { rejectAllConsent } from "@/support/consent";
import * as mailhog from "@/support/mailhog";

// SESSION_COOKIE is authn.SessionCookie (server/internal/authn/user.go) --
// the board's access-token cookie. Tests that need to force a session to
// look expired (rather than waiting out its real TTL) clear it directly
// rather than going through a UI sign-out, which would also drop the
// refresh cookie and end the session for good.
export const SESSION_COOKIE = "slop_haus_session";

// independentPage is a signed-out browser context of its own, for a spec
// that needs a *second* real identity alongside `page` (always the seeded
// dev user) -- support/fixtures.ts's own `anonPage` shares `page`'s browser
// context, so using both in the same test would leave the "signed-out" page
// carrying `page`'s cookies too. A plain `browser.newContext()` avoids that
// entirely.
export async function independentPage(browser: Browser): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext();
  await rejectAllConsent(context);
  return { context, page: await context.newPage() };
}

// A fresh, never-registered address per call -- register/login are
// rate-limited per address (server/cmd/api/main.go's LoginLimit/MailLimit),
// so reusing one address across tests would make a full suite run flaky
// depending on execution order.
export function uniqueEmail(prefix: string): string {
  return `${prefix}-${crypto.randomUUID()}@example.com`;
}

export interface NewAccount {
  email: string;
  password: string;
  displayName: string;
}

export function newAccount(prefix: string): NewAccount {
  return { email: uniqueEmail(prefix), password: "a-strong-password", displayName: `${prefix} person` };
}

// registerViaUI drives the real /register form and waits for the "check
// your email" success screen -- the account is not yet activated.
export async function registerViaUI(page: Page, account: NewAccount): Promise<void> {
  await page.goto("/register");
  await page.getByLabel("Display name").fill(account.displayName);
  await page.getByLabel("Email").fill(account.email);
  await page.getByLabel("Password").fill(account.password);
  // The heading only appears once the server has hashed the password and
  // handed the activation mail over, which under CI load can exceed the
  // default 5s assertion timeout. Waiting on the response itself
  // separates "the server was slow" from "the server refused" (a rate
  // limit, say): the latter fails here with its status and body, not as a
  // missing heading.
  const registered = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/api/auth/register"),
    { timeout: 30_000 },
  );
  await page.getByRole("button", { name: "Create account" }).click();
  const resp = await registered;
  // The body is read only for a refusal: once the page has moved on to the
  // success screen the browser no longer holds a 2xx body to fetch.
  if (!resp.ok()) {
    throw new Error(`register refused: ${resp.status()} ${await resp.text()}`);
  }
  await expect(page.getByRole("heading", { name: "Check your email" })).toBeVisible();
}

// activateViaEmail reads the activation link MailHog just captured for
// `email` and follows it (activate_page.tsx auto-submits on mount), leaving
// `page` on the activated confirmation with a "Sign in" link.
export async function activateViaEmail(page: Page, email: string): Promise<void> {
  const body = await mailhog.waitForEmail(email, "Activate your slop-haus account");
  // The page auto-submits on mount and the success text only appears once
  // the server answers, which under CI load can exceed the default 5s
  // assertion timeout. Waiting on the response (registered before the
  // navigation so it can't be missed) makes a refusal fail with its status
  // and body rather than as a missing text.
  const activated = page.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith("/api/auth/activate"),
    { timeout: 30_000 },
  );
  await page.goto(mailhog.linkPath(body));
  await expect(page.getByRole("heading", { name: "Activate your account" })).toBeVisible();
  const resp = await activated;
  if (!resp.ok()) {
    throw new Error(`activate refused: ${resp.status()} ${await resp.text()}`);
  }
  await expect(page.getByText("Your account is active. You can now sign in.")).toBeVisible();
}

// registerAndActivate is the two steps above together, the common case for
// a spec that just needs a second, real, signed-up-able account.
export async function registerAndActivate(page: Page, account: NewAccount): Promise<void> {
  await registerViaUI(page, account);
  await activateViaEmail(page, account.email);
}

// fillLoginForm drives the real /login form. It does not navigate there
// itself -- a caller either just followed a link to it (the activation
// page's "Sign in" link) or goes directly with page.goto("/login").
export async function fillLoginForm(page: Page, email: string, password: string): Promise<void> {
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
}

// signInViaUI is fillLoginForm for a page that isn't already on /login.
export async function signInViaUI(page: Page, email: string, password: string): Promise<void> {
  await page.goto("/login");
  await fillLoginForm(page, email, password);
}
