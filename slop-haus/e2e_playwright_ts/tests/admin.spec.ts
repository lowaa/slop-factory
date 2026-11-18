// The separately-served admin UI (frontend/src/admin, built as its own
// Vite bundle -- see frontend/vite.admin.config.ts) and the nginx gate in
// front of it (frontend/nginx.conf.template's auth_request against
// GET /api/auth/admin-check). `page` is always the seeded dev user, who is
// an admin (../docker-compose-dev.yaml's `seed` service runs `add_user
// --admin`).
//
// Every test that needs `page` (the admin) alongside a second, independent
// identity uses `browser.newContext()` for that second identity rather than
// support/fixtures.ts's own `anonPage` -- see invites.spec.ts's doc comment
// on why that pairing (both derive from the same test-scoped `context`
// fixture) would leave the "signed-out" page carrying the admin's cookies.
import { boardWriteHeaders, expect, test } from "./support/fixtures";
import { newTicket } from "./support/api_helpers";
import * as mailhog from "./support/mailhog";
import { fillLoginForm, independentPage, newAccount, registerAndActivate, uniqueEmail } from "./support/auth_flow";

test("an admin invites a new person via the admin UI, and they get their own empty tenant", async ({
  page,
  browser,
  api,
}) => {
  await newTicket(api, "The dev tenant's own ticket");

  await page.goto("/admin/");
  await expect(page.getByRole("heading", { name: "slop-haus admin" })).toBeVisible();

  const email = `admin-invite-${crypto.randomUUID()}@example.com`;
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(email)).toBeVisible();

  const body = await mailhog.waitForEmail(email, "invited you to slop-haus");
  const { context, page: invitee } = await independentPage(browser);
  await invitee.goto(mailhog.linkPath(body));
  await expect(invitee.getByRole("heading", { name: "You're invited" })).toBeVisible();

  await invitee.getByLabel("Display name").fill("New Owner");
  await invitee.getByLabel("Password").fill("a-strong-password");
  await invitee.getByRole("button", { name: "Create account" }).click();

  await expect(invitee.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  // A tenant of their own, not the dev tenant's -- no tenant switcher (only
  // ever a member of the one), and none of the dev tenant's tickets.
  await expect(invitee.getByRole("combobox", { name: "Tenant" })).not.toBeVisible();
  await expect(invitee.getByText("The dev tenant's own ticket")).not.toBeVisible();
  await expect(invitee.getByText("No tickets")).toHaveCount(await invitee.locator(".board__column").count());
  await context.close();
});

test("revoking an admin invite kills the link", async ({ page, browser }) => {
  await page.goto("/admin/");
  const email = `admin-invite-revoked-${crypto.randomUUID()}@example.com`;
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(email)).toBeVisible();

  const body = await mailhog.waitForEmail(email, "invited you to slop-haus");

  await page.getByRole("button", { name: "Revoke" }).click();
  await expect(page.getByText(email)).not.toBeVisible();

  const { context, page: invitee } = await independentPage(browser);
  await invitee.goto(mailhog.linkPath(body));
  await expect(invitee.getByRole("alert")).toBeVisible();
  await context.close();
});

test("a signed-out visitor is redirected away from the admin gate, never served its JS", async ({ anonPage }) => {
  const pageResp = await anonPage.request.get("/admin/", { maxRedirects: 0 });
  expect(pageResp.status()).toBe(302);
  expect(pageResp.headers()["location"]).toContain("/login");
});

test("a non-admin gets 403 from the admin gate, on the page and on its own asset URL", async ({ page, browser }) => {
  // A real admin asset URL, discovered the way a browser would -- from the
  // admin page's own script tag, fetched as the admin.
  await page.goto("/admin/");
  const assetSrc = await page.locator('script[type="module"]').getAttribute("src");
  expect(assetSrc, "the admin page should reference its own bundled script").toBeTruthy();
  // Served to the admin as cacheable by their browser only: a shared cache
  // that kept it would hand it to anyone who asked next.
  const adminAsset = await page.request.get(assetSrc!);
  expect(adminAsset.status()).toBe(200);
  expect(adminAsset.headers()["cache-control"]).toContain("private");
  expect(adminAsset.headers()["cache-control"]).not.toContain("public");

  const { context, page: nonAdminPage } = await independentPage(browser);
  const nonAdmin = newAccount("admin-gate-non-admin");
  await registerAndActivate(nonAdminPage, nonAdmin);
  await nonAdminPage.goto("/login");
  await fillLoginForm(nonAdminPage, nonAdmin.email, nonAdmin.password);
  await expect(nonAdminPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(nonAdminPage.getByRole("link", { name: "Admin" })).not.toBeVisible();

  const pageResp = await nonAdminPage.request.get("/admin/", { maxRedirects: 0 });
  expect(pageResp.status()).toBe(403);

  const assetResp = await nonAdminPage.request.get(assetSrc!, { maxRedirects: 0 });
  expect(assetResp.status(), "the non-admin must never receive the admin bundle's JS").toBe(403);
  expect(await assetResp.text()).not.toContain("createAdminInvite");
  await context.close();
});

// The gate above keeps a non-admin's browser off the admin bundle; this is
// the same door checked directly -- authn.RequireAdmin on the route table
// itself (server/internal/user/routes.go), not just nginx's auth_request in
// front of the JS. Both GET and POST, since a non-admin must not be able to
// either read who else was invited or mint a new invite of their own.
test("a non-admin calling /api/admin/invites directly gets 403, listing and creating alike", async ({ browser }) => {
  const { context, page: nonAdminPage } = await independentPage(browser);
  const nonAdmin = newAccount("admin-api-non-admin");
  await registerAndActivate(nonAdminPage, nonAdmin);
  await nonAdminPage.goto("/login");
  await fillLoginForm(nonAdminPage, nonAdmin.email, nonAdmin.password);
  await expect(nonAdminPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  const listResp = await nonAdminPage.request.get("/api/admin/invites");
  expect(listResp.status(), await listResp.text()).toBe(403);

  // With the board's Origin and the session's CSRF token, so the 403 is
  // the admin check's and not the CSRF check's.
  const createResp = await nonAdminPage.request.post("/api/admin/invites", {
    headers: await boardWriteHeaders(nonAdminPage.request),
    data: { email: uniqueEmail("admin-api-post-blocked") },
  });
  expect(createResp.status(), await createResp.text()).toBe(403);
  expect((await createResp.json()).detail).toBe("forbidden");
  await context.close();
});
