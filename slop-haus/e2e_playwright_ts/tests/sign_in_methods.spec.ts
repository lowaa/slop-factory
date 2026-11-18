// Sign-in methods and login providers through the real UI: the account
// page's shared panel, and a full round trip through the stack's Dex (see
// ../docker-compose-dev.e2e.yaml) -- the provider button, accepting an
// invite with it, and linking it to a password account.
//
// Every test works on an account of its own, never the dev user's: a
// password change ends every other session of the account, and `page`/`api`
// share one dev-user session across the whole worker.
import type { Page } from "@playwright/test";
import { expect, test } from "./support/fixtures";
import * as mailhog from "./support/mailhog";
import { independentPage, newAccount, registerAndActivate, signInViaUI, uniqueEmail } from "./support/auth_flow";
import { dex, signInAtDex } from "@e2e-support/dex";
import { accountMenuButton, openAccountPage, openHeaderTab, signOutViaHeader } from "./support/widgets";

const BOARD = { name: "File a ticket" };

async function openAccount(page: Page): Promise<void> {
  await openAccountPage(page);
  await expect(page).toHaveURL(/\/account$/);
  await expect(page.getByRole("heading", { name: "Sign-in methods" })).toBeVisible();
}

// methodRow is the panel's row for one way in, by its name exactly -- an
// address in another row ("link-dex-...@example.com") can contain the name
// too.
function methodRow(page: Page, name: string) {
  return page.getByRole("list").getByRole("listitem").filter({ has: page.getByText(name, { exact: true }) });
}

test("the sign-in page offers the configured provider beside the password form", async ({ anonPage }) => {
  await anonPage.goto("/login");

  await expect(anonPage.getByRole("button", { name: "Continue with Dex" })).toBeVisible();
  await expect(anonPage.getByLabel("Password")).toBeVisible();
});

test("a password account changes its password, and can't remove its only way in", async ({ anonPage }) => {
  const account = newAccount("methods");
  await registerAndActivate(anonPage, account);
  await signInViaUI(anonPage, account.email, account.password);
  await expect(anonPage.getByRole("heading", BOARD)).toBeVisible();
  await openAccount(anonPage);
  await expect(methodRow(anonPage, "Password")).toContainText(account.email);

  await anonPage.getByRole("button", { name: "Remove password" }).click();
  await anonPage.getByRole("button", { name: "Yes, remove" }).click();
  await expect(anonPage.getByRole("alert")).toContainText("That’s the only way you can sign in");

  const changed = "a-changed-password";
  await anonPage.getByRole("button", { name: "Change password" }).click();
  await anonPage.getByLabel("New password").fill(changed);
  await anonPage.getByRole("button", { name: "Save" }).click();
  // The consent provider keeps its own (empty) status region on every page, so
  // name the one meant.
  await expect(anonPage.getByRole("status").filter({ hasText: "Password changed." })).toBeVisible();

  await signOutViaHeader(anonPage);
  // Sign-out is an async request: navigating to /login before it lands aborts
  // it, the session survives, and /login redirects to the board.
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await signInViaUI(anonPage, account.email, account.password);
  await expect(anonPage.getByRole("alert")).toContainText("invalid email or password");
  await signInViaUI(anonPage, account.email, changed);
  await expect(anonPage.getByRole("heading", BOARD)).toBeVisible();
});

test("an invitee accepts with Dex, signs in with Dex from then on, and adds a password", async ({ page, browser }) => {
  await page.goto("/");
  await openHeaderTab(page, "Members");
  const email = uniqueEmail("dex-invite");
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(email)).toBeVisible();

  const body = await mailhog.waitForEmail(email, "invited you to slop-haus");
  const { context, page: invitee } = await independentPage(browser);
  await invitee.goto(mailhog.linkPath(body));
  await invitee.getByRole("button", { name: "Continue with Dex" }).click();
  await signInAtDex(invitee, dex.alice);

  // Signed in, in the inviter's tenant, at the invite's address -- the
  // link proved it, whatever Dex says the address is.
  await expect(invitee.getByRole("heading", BOARD)).toBeVisible();
  await openAccount(invitee);
  await expect(methodRow(invitee, "Dex")).toContainText(dex.alice.email);
  await expect(methodRow(invitee, "Password")).toContainText("Not set");

  await invitee.getByRole("button", { name: "Add a password" }).click();
  await invitee.getByLabel("New password").fill("a-strong-password");
  await invitee.getByRole("button", { name: "Save" }).click();
  await expect(invitee.getByRole("status").filter({ hasText: "Password added." })).toBeVisible();
  await expect(methodRow(invitee, "Password")).toContainText(email);

  await signOutViaHeader(invitee);
  await invitee.getByRole("button", { name: "Continue with Dex" }).click();
  await signInAtDex(invitee, dex.alice);
  await expect(invitee.getByRole("heading", BOARD)).toBeVisible();
  await context.close();
});

test("a password account links Dex from the account page, and signs in with it", async ({ anonPage }) => {
  const account = newAccount("link-dex");
  await registerAndActivate(anonPage, account);
  await signInViaUI(anonPage, account.email, account.password);
  await expect(anonPage.getByRole("heading", BOARD)).toBeVisible();
  await openAccount(anonPage);

  await anonPage.getByRole("button", { name: "Link Dex" }).click();
  await signInAtDex(anonPage, dex.bob);

  await expect(anonPage).toHaveURL(/\/account$/);
  await expect(methodRow(anonPage, "Dex")).toContainText(dex.bob.email);

  await signOutViaHeader(anonPage);
  await anonPage.getByRole("button", { name: "Continue with Dex" }).click();
  await signInAtDex(anonPage, dex.bob);
  await expect(anonPage.getByRole("heading", BOARD)).toBeVisible();
  await expect(accountMenuButton(anonPage, account.displayName)).toBeVisible();
});

test("a Dex sign-in nobody has linked comes back to the sign-in page saying so", async ({ anonPage }) => {
  await anonPage.goto("/login");
  await anonPage.getByRole("button", { name: "Continue with Dex" }).click();
  await signInAtDex(anonPage, dex.bob);

  await expect(anonPage).toHaveURL(/\/login$/);
  await expect(anonPage.getByRole("alert")).toHaveText(
    "There’s no account here for that Dex sign-in. If you already have an account, sign in another way and link Dex from your account page. Otherwise, ask for an invite.",
  );
});
