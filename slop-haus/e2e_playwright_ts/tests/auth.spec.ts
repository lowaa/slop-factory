// The sign-up/sign-in surface end to end, through the real UI and a real
// MailHog inbox -- api.spec.ts and friends drive the REST API directly, but
// register/activate/reset are journeys through emailed links that only
// make sense driven the way a person actually takes them.
import { boardWriteHeaders, CSRF_HEADER, expect, test } from "./support/fixtures";
import { BOARD_ORIGIN } from "./support/env";
import * as mailhog from "./support/mailhog";
import {
  activateViaEmail,
  fillLoginForm,
  independentPage,
  newAccount,
  registerAndActivate,
  registerViaUI,
  signInViaUI,
  SESSION_COOKIE,
  uniqueEmail,
} from "./support/auth_flow";
import { accountMenuButton, signOutViaHeader } from "./support/widgets";

test("register, activate, sign in, and land on the board with the display name", async ({ anonPage }) => {
  const account = newAccount("register-flow");
  await registerViaUI(anonPage, account);
  await activateViaEmail(anonPage, account.email);

  await anonPage.getByRole("link", { name: "Sign in" }).click();
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await fillLoginForm(anonPage, account.email, account.password);

  // The board itself, with this account's own name in the header -- not a
  // generic "signed in" notice.
  await expect(anonPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(accountMenuButton(anonPage, account.displayName)).toBeVisible();
});

// The 401 body is huma's standard envelope, and the same generic detail
// whether the address doesn't exist or the password is wrong (see
// server/internal/user/service.go's Login) -- login_page.tsx renders it
// as-is, so this also pins that the UI never invents a different, more
// specific-sounding message of its own.
test("a wrong password shows the generic error and leaves the visitor signed out", async ({ anonPage }) => {
  const account = newAccount("wrong-password");
  await registerViaUI(anonPage, account);
  await activateViaEmail(anonPage, account.email);

  await signInViaUI(anonPage, account.email, "not-the-right-password");

  await expect(anonPage.getByRole("alert")).toContainText("invalid email or password");
  // Still on the login form, not the board.
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();
});

test("forgot password, reset via the emailed link, and only the new password works", async ({ anonPage }) => {
  const account = newAccount("reset-flow");
  await registerViaUI(anonPage, account);
  await activateViaEmail(anonPage, account.email);

  await anonPage.goto("/forgot-password");
  await anonPage.getByLabel("Email").fill(account.email);
  await anonPage.getByRole("button", { name: "Send reset link" }).click();
  await expect(anonPage.getByRole("heading", { name: "Check your email" })).toBeVisible();

  const body = await mailhog.waitForEmail(account.email, "Reset your slop-haus password");
  await anonPage.goto(mailhog.linkPath(body));
  await expect(anonPage.getByRole("heading", { name: "Set a new password" })).toBeVisible();

  const newPassword = "a-brand-new-password";
  await anonPage.getByLabel("New password").fill(newPassword);
  await anonPage.getByRole("button", { name: "Set new password" }).click();
  await expect(anonPage.getByText("Your password has been changed. You can now sign in.")).toBeVisible();

  await anonPage.getByRole("link", { name: "Sign in" }).click();
  await fillLoginForm(anonPage, account.email, account.password);
  await expect(anonPage.getByRole("alert")).toContainText("invalid email or password");

  await signInViaUI(anonPage, account.email, newPassword);
  await expect(anonPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();
});

// Single-use is enforced server-side (see server/internal/user/service.go's
// ResetPassword, spent atomically on use) -- reusing the very link that
// just worked has to be refused, not just a *different*, never-used token.
test("a reset link works once", async ({ anonPage }) => {
  const account = newAccount("reset-once");
  await registerViaUI(anonPage, account);
  await activateViaEmail(anonPage, account.email);

  await anonPage.goto("/forgot-password");
  await anonPage.getByLabel("Email").fill(account.email);
  await anonPage.getByRole("button", { name: "Send reset link" }).click();

  const body = await mailhog.waitForEmail(account.email, "Reset your slop-haus password");
  const path = mailhog.linkPath(body);

  await anonPage.goto(path);
  await anonPage.getByLabel("New password").fill("first-use-password");
  await anonPage.getByRole("button", { name: "Set new password" }).click();
  await expect(anonPage.getByText("Your password has been changed. You can now sign in.")).toBeVisible();

  // The exact same link, a second time -- via a different page first
  // (a real second click on the emailed link is a fresh top-level
  // navigation, the way an email client always opens a link). Without
  // this, Chromium sees the destination differs from the current URL only
  // by fragment and treats it as an in-page anchor jump rather than a real
  // navigation, so reset_password_page.tsx never remounts and never
  // re-reads the token -- a Playwright/browser quirk, not the app reusing
  // state a real second visit wouldn't have.
  await anonPage.goto("/login");
  await anonPage.goto(path);
  await anonPage.getByLabel("New password").fill("second-use-password");
  await anonPage.getByRole("button", { name: "Set new password" }).click();
  await expect(anonPage.getByRole("alert")).toContainText("invalid or expired token");
});

test("signing out returns to /login, and the board is no longer reachable", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  await signOutViaHeader(page);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/login");

  // Not just "the sign-out screen happened to land there" -- the root path
  // itself no longer resolves to the board for this browser.
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
});

// The signed-in pages are behind sign-in whichever URL a visitor arrives
// on, and the address bar ends up saying where they actually are.
// A page other than the board goes along as /login's return path (see
// sign_in_return.spec.ts).
test("an anonymous visitor to the board or /account is sent to /login", async ({ anonPage }) => {
  for (const [path, login] of [
    ["/", /\/login$/],
    ["/account", /\/login\?redirect=%2Faccount$/],
  ] as const) {
    await anonPage.goto(path);
    await expect(anonPage.getByRole("heading", { name: "Sign in" }), path).toBeVisible();
    await expect(anonPage, path).toHaveURL(login);
    await expect(anonPage.getByRole("heading", { name: "File a ticket" }), path).toHaveCount(0);
  }
});

test("a signed-in visitor to /login or /register is sent to the board", async ({ page }) => {
  for (const path of ["/login", "/register"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { name: "File a ticket" }), path).toBeVisible();
    await expect(page, path).toHaveURL(/\/$/);
  }
});

test("an unauthenticated request to a board API route gets 401", async ({ anonPage }) => {
  const resp = await anonPage.request.get("/api/tickets");
  expect(resp.status()).toBe(401);
});

// A write the session cookie authenticates needs the session's CSRF token
// (server/internal/authn/csrf.go's BrowserGuard). The real page sends it on
// every write -- every UI spec that changes something proves that -- so
// this is the other half: the browser's own cookies, from the board's own
// origin, but without the token the page would have read from
// /api/auth/me, are refused. Then the same request with it, so the refusal
// is shown to be the token's doing.
test("a signed-in browser's write without the session's CSRF token is refused", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  const headers = await boardWriteHeaders(page.request);
  const token = headers[CSRF_HEADER];
  expect(token, "/api/auth/me hands the page its token").toBeTruthy();

  const refused: { name: string; headers: Record<string, string>; code: string }[] = [
    { name: "no token", headers: { Origin: BOARD_ORIGIN }, code: "csrf" },
    { name: "a made-up token", headers: { Origin: BOARD_ORIGIN, [CSRF_HEADER]: "bm90LXRoZS10b2tlbg" }, code: "csrf" },
    { name: "another site", headers: { Origin: "https://evil.example", [CSRF_HEADER]: token }, code: "cross_origin" },
  ];
  for (const attempt of refused) {
    const resp = await page.request.post("/api/tickets", {
      headers: attempt.headers,
      data: { title: `should never be created (${attempt.name})` },
    });
    expect(resp.status(), `${attempt.name}: ${await resp.text()}`).toBe(403);
    expect((await resp.json()).code, attempt.name).toBe(attempt.code);
  }

  const allowed = await page.request.post("/api/tickets", { headers, data: { title: "created with the token" } });
  expect(allowed.status(), await allowed.text()).toBe(201);
  const titles = ((await (await page.request.get("/api/tickets")).json()).tickets as { title: string }[]).map(
    (t) => t.title,
  );
  expect(titles).toEqual(["created with the token"]);
});

// Sign-in is a public write, before any session exists for the token to
// belong to, so it is held to the board's origin instead. Without that, a
// cross-site page could POST a victim's browser into signing in as the
// attacker (login CSRF).
test("POST /api/auth/login from another origin, or none, is 403; from the board, wrong creds is a plain 401", async ({
  anonPage,
}) => {
  const email = uniqueEmail("login-csrf");
  const data = { email, password: "whatever-it-does-not-matter" };

  const fromElsewhere: Record<string, Record<string, string>> = {
    "another origin": { Origin: "https://evil.example" },
    "a Referer from another site": { Referer: "https://evil.example/page" },
    "neither Origin nor Referer": {},
  };
  for (const [name, headers] of Object.entries(fromElsewhere)) {
    const resp = await anonPage.request.post("/api/auth/login", { headers, data });
    expect(resp.status(), `${name}: ${await resp.text()}`).toBe(403);
    expect((await resp.json()).code, name).toBe("cross_origin");
  }

  // Positive control: the same request from the board is answered on its
  // merits (401, since there's no such account) -- proving the 403s above
  // really are the origin's doing, not some other block that would have
  // refused this request either way.
  const fromTheBoard = await anonPage.request.post("/api/auth/login", { headers: { Origin: BOARD_ORIGIN }, data });
  expect(fromTheBoard.status(), await fromTheBoard.text()).toBe(401);
  const viaReferer = await anonPage.request.post("/api/auth/login", {
    headers: { Referer: `${BOARD_ORIGIN}/login` },
    data,
  });
  expect(viaReferer.status(), await viaReferer.text()).toBe(401);
});

// user.Service.Login's rate limit key is address+client (server/internal/
// user/service.go's limitKey), not the client alone: 10 failed attempts
// locks out that one address from that one client, and nothing else.
test("a locked-out address is refused even with the right password, in the UI; a different address is unaffected", async ({
  anonPage,
}) => {
  const locked = newAccount("lockout");
  await registerAndActivate(anonPage, locked);

  // Driven through the API directly (still through the frontend origin,
  // same as the real form) rather than the real form ten times over --
  // this loop's own outcome isn't the point, only that it spends the
  // budget the next, UI-driven attempt then finds exhausted.
  for (let i = 0; i < 10; i++) {
    const resp = await anonPage.request.post("/api/auth/login", {
      headers: { Origin: BOARD_ORIGIN },
      data: { email: locked.email, password: "not-the-right-password" },
    });
    expect(resp.status(), await resp.text()).toBe(401);
  }

  // The 11th attempt, this time with the *correct* password and through
  // the real form -- refused all the same, and the sign-in page has to
  // say so rather than a generic "wrong password".
  await anonPage.goto("/login");
  await fillLoginForm(anonPage, locked.email, locked.password);
  // @slop-factory/auth's LoginPage explains a rate_limited refusal in its
  // own words (error_codes.ts's describeAuthCode) rather than the server's
  // raw detail text, same as every other code it knows.
  await expect(anonPage.getByRole("alert")).toContainText("Too many attempts");
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();

  // A different address from the very same browser (client, IP) is a
  // different rate-limit key and is unaffected.
  const other = newAccount("lockout-unaffected");
  await registerAndActivate(anonPage, other);
  await anonPage.goto("/login");
  await fillLoginForm(anonPage, other.email, other.password);
  await expect(anonPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();
});

// Every existing session ends the moment a password is reset (user.Service.
// ResetPassword bumps sessions_valid_after) -- a reset is often a response
// to someone else already holding the password, so leaving their session
// alive would defeat the point.
test("resetting a password ends every other signed-in session", async ({ browser }) => {
  const account = newAccount("reset-ends-sessions");
  const { context: ownerCtx, page: owner } = await independentPage(browser);
  await registerAndActivate(owner, account);

  // A second, independent browser session for the same account -- signed
  // in *before* the reset, the one this test expects to be cut off.
  const { context: otherCtx, page: other } = await independentPage(browser);
  await other.goto("/login");
  await fillLoginForm(other, account.email, account.password);
  await expect(other.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  // sessionLive (server/internal/user/service.go) compares whole seconds,
  // deliberately: "a session signed in during the same second as the
  // cut-off... survives; that is the price." Without a gap here, a fast
  // run can land the sign-in above and the reset below in the same wall
  // second and flake exactly into that documented edge case.
  await other.waitForTimeout(1_100);

  await owner.goto("/forgot-password");
  await owner.getByLabel("Email").fill(account.email);
  await owner.getByRole("button", { name: "Send reset link" }).click();
  const body = await mailhog.waitForEmail(account.email, "Reset your slop-haus password");
  await owner.goto(mailhog.linkPath(body));
  await owner.getByLabel("New password").fill("a-brand-new-password");
  await owner.getByRole("button", { name: "Set new password" }).click();
  await expect(owner.getByText("Your password has been changed. You can now sign in.")).toBeVisible();

  // The other session's refresh cookie is now stale: refreshing it
  // (server/internal/user/routes.go's RefreshPath) is refused outright.
  // Its access token is not checked here on purpose: VerifyAccess reads no
  // database, so an access token lives out its short TTL and revocation
  // takes hold at the next refresh.
  const refreshResp = await other.request.post("/api/auth/refresh", { headers: { Origin: BOARD_ORIGIN } });
  expect(refreshResp.status(), await refreshResp.text()).toBe(401);

  // And once its access token is gone -- simulated here rather than
  // waiting out its real TTL -- the app has nothing left to refresh it
  // with, and lands back on /login.
  await other.context().clearCookies({ name: SESSION_COOKIE });
  await other.reload();
  await expect(other.getByRole("heading", { name: "Sign in" })).toBeVisible();
  expect(new URL(other.url()).pathname).toBe("/login");

  await ownerCtx.close();
  await otherCtx.close();
});
