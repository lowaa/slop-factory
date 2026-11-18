// Invitations end to end, through the real UI, a real MailHog inbox and the
// real tenant-switching session cookies -- auth.spec.ts covers
// register/activate/login/reset, tenancy.spec.ts covers isolation between
// two already-existing tenants; this file covers how someone gets from an
// invite link into (or between) tenants at all.
//
// Every test here needs `page` (the signed-in dev user, driving the
// inviter's side of things) *and* an independent, signed-out identity for
// the invitee -- support/fixtures.ts's own `anonPage` shares its browser
// context with `page`, so using both in the same test would leave the
// "signed-out" page carrying the dev user's cookies too (see that file's
// doc comment on the pair). A plain `browser.newContext()` avoids that
// entirely, the same way tenancy.spec.ts avoids it by pairing `api`
// (a separate request context, not a page) with `anonPage` instead.
import { boardWriteHeaders, expect, test } from "./support/fixtures";
import { newTicket } from "./support/api_helpers";
import * as mailhog from "./support/mailhog";
import { fillLoginForm, independentPage, newAccount, registerAndActivate, uniqueEmail } from "./support/auth_flow";
import { accountMenuButton, chooseFromDropdown, openHeaderTab } from "./support/widgets";

// `page` is always the seeded dev user (tenant A -- see support/fixtures.ts).
test("a member invites a new person, who creates an account from the link and lands in the inviter's tenant", async ({
  page,
  browser,
  api,
}) => {
  await newTicket(api, "Only the inviter's tenant should show this");

  await page.goto("/");
  await openHeaderTab(page, "Members");
  const email = uniqueEmail("member-invite");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(email)).toBeVisible();

  const body = await mailhog.waitForEmail(email, "invited you to slop-haus");
  const { context, page: invitee } = await independentPage(browser);
  await invitee.goto(mailhog.linkPath(body));
  await expect(invitee.getByRole("heading", { name: "You're invited" })).toBeVisible();
  // The invited address is shown, not asked for -- the invite already
  // names it (invite_page.tsx's subtitle).
  await expect(invitee.getByText(email)).toBeVisible();

  await invitee.getByLabel("Display name").fill("Invited Person");
  await invitee.getByLabel("Password").fill("a-strong-password");
  await invitee.getByRole("button", { name: "Create account" }).click();

  await expect(invitee.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(accountMenuButton(invitee, "Invited Person")).toBeVisible();
  await expect(invitee.getByText("Only the inviter's tenant should show this")).toBeVisible();

  // The invite is single-use: the same link a second time is dead, not a
  // second acceptance.
  await invitee.goto("/login");
  await invitee.goto(mailhog.linkPath(body));
  await expect(invitee.getByRole("alert")).toBeVisible();
  await context.close();
});

test("a member invites an existing user, who accepts while signed in and then switches back with the tenant switcher", async ({
  page,
  browser,
}) => {
  const account = newAccount("invite-existing");
  const { context, page: invitee } = await independentPage(browser);
  await registerAndActivate(invitee, account);

  await page.goto("/");
  await openHeaderTab(page, "Members");
  await page.getByLabel("Email").fill(account.email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(account.email)).toBeVisible();

  const body = await mailhog.waitForEmail(account.email, "invited you to slop-haus");
  const invitePath = mailhog.linkPath(body);

  // Sign in as the invitee (their own, pre-existing tenant) before ever
  // touching the invite link.
  await invitee.goto("/login");
  await fillLoginForm(invitee, account.email, account.password);
  await expect(invitee.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  await invitee.goto(invitePath);
  await expect(invitee.getByRole("heading", { name: "You're invited" })).toBeVisible();
  await invitee.getByRole("button", { name: "Accept" }).click();

  // Moved into the inviter's (dev user's) tenant.
  await expect(invitee.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(invitee.getByRole("combobox", { name: "Tenant" })).toBeVisible();

  // Switch back to their own tenant -- named after their own display name,
  // the way self-registration provisions one (server/internal/user/
  // store.go's provisionTenant).
  await chooseFromDropdown(invitee, "Tenant", account.displayName);
  await expect(invitee.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(invitee.getByRole("combobox", { name: "Tenant" })).toHaveText(account.displayName);
  await context.close();
});

// The invite page sends a signed-out invitee who already has an account off
// to /login, and the invite has to still be waiting when they come back --
// by then the URL is plain "/login" and no longer names it, and its token
// left the address bar on the first visit.
test("a signed-out invitee with an account goes through /login and comes back to the invite to accept it", async ({
  page,
  browser,
}) => {
  const account = newAccount("invite-round-trip");
  const { context, page: invitee } = await independentPage(browser);
  await registerAndActivate(invitee, account);

  await page.goto("/");
  await openHeaderTab(page, "Members");
  await page.getByLabel("Email").fill(account.email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(account.email)).toBeVisible();

  const body = await mailhog.waitForEmail(account.email, "invited you to slop-haus");
  await invitee.goto(mailhog.linkPath(body));
  await expect(invitee.getByText(/sign in to accept/)).toBeVisible();
  await invitee.getByRole("link", { name: "Sign in" }).click();

  await expect(invitee.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(invitee).toHaveURL(/\/login$/);
  await fillLoginForm(invitee, account.email, account.password);

  await expect(invitee.getByRole("heading", { name: "You're invited" })).toBeVisible();
  await expect(invitee).toHaveURL(/\/invite$/);
  await invitee.getByRole("button", { name: "Accept" }).click();

  // In the inviter's tenant now, with their own still there to switch to.
  await expect(invitee.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(invitee).toHaveURL(/\/$/);
  await expect(invitee.getByRole("combobox", { name: "Tenant" })).toBeVisible();
  await context.close();
});

// The invite is for its address specifically: signed in as someone else
// entirely, the page has to say so rather than silently offering to accept.
// The invited address needs its own *existing* account for this (an
// account-less invite always shows the create-account form, regardless of
// who else is signed in -- see invite_page.tsx) -- so this uses two
// separate real accounts, neither of them the invite's target.
test("signed in as a different address, the invite page says so and offers to sign out", async ({ page, browser }) => {
  const { context: victimCtx, page: victimPage } = await independentPage(browser);
  const victim = newAccount("invite-victim");
  await registerAndActivate(victimPage, victim);
  await victimCtx.close();

  const { context: outsiderCtx, page: outsiderPage } = await independentPage(browser);
  const outsider = newAccount("invite-outsider");
  await registerAndActivate(outsiderPage, outsider);

  await page.goto("/");
  await openHeaderTab(page, "Members");
  await page.getByLabel("Email").fill(victim.email);
  await page.getByRole("button", { name: "Send invite" }).click();

  const body = await mailhog.waitForEmail(victim.email, "invited you to slop-haus");

  // outsider signs in as themself, then opens an invite meant for victim.
  await outsiderPage.goto("/login");
  await fillLoginForm(outsiderPage, outsider.email, outsider.password);
  // Wait for the sign-in itself to land before navigating away -- a goto()
  // straight after clicking submit can outrace the login response (and
  // the session cookie it sets), which is exactly the flake this fixed.
  await expect(outsiderPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await outsiderPage.goto(mailhog.linkPath(body));

  await expect(outsiderPage.getByText(new RegExp(`signed in as ${outsider.email}`))).toBeVisible();
  await expect(outsiderPage.getByRole("button", { name: "Sign out" })).toBeVisible();
  await outsiderCtx.close();
});

// A member's own Members panel offers the same "Revoke" admin.spec.ts's
// admin-invite test exercises for the admin bundle -- this is the ordinary
// member's version of it (DELETE /api/invites/{id}, not /api/admin/invites),
// and the invite is still a live link right up until that click.
test("a member revokes their own pending invite, and the emailed link stops working", async ({ page, browser }) => {
  await page.goto("/");
  await openHeaderTab(page, "Members");
  const email = uniqueEmail("member-revoke");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(email)).toBeVisible();

  const body = await mailhog.waitForEmail(email, "invited you to slop-haus");
  const path = mailhog.linkPath(body);

  // Scoped to this invite's own row -- an earlier test in this file can
  // leave another outstanding invite (and "Revoke" button) on the panel,
  // and a bare page-wide locator would be ambiguous between them.
  await page.locator(".members-panel__row", { hasText: email }).getByRole("button", { name: "Revoke" }).click();
  await expect(page.getByText(email)).not.toBeVisible();

  const { context, page: invitee } = await independentPage(browser);
  await invitee.goto(path);
  await expect(invitee.getByRole("alert")).toBeVisible();
  await expect(invitee.getByRole("button", { name: "Create account" })).not.toBeVisible();

  // Not just the page declining to offer acceptance -- the revoked token
  // itself is refused server-side too, the same call CreateAccountForm
  // would have made had the invite still been live.
  const acceptResp = await invitee.request.post("/api/auth/invite/accept", {
    headers: await boardWriteHeaders(invitee.request),
    data: { token: mailhog.tokenFrom(path), display_name: "Too Late", password: "a-strong-password" },
  });
  // 400 is ErrInvalidToken (server/internal/user/handler.go), the answer
  // for an unknown, spent or revoked token alike.
  expect(acceptResp.status(), await acceptResp.text()).toBe(400);
  await context.close();
});
