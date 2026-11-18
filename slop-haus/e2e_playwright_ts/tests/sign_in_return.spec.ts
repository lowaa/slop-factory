// A link into the board survives signing in: an anonymous visitor is sent
// to /login?redirect=<the page>, and lands back on it once signed in --
// with a password, or through a login provider (where the server carries
// it through the round trip as next). A return path to anywhere but this
// site lands on the board instead.
//
// Each test signs in with an account of its own: login is rate-limited
// per address, and the dev user's is shared with every other spec.
import type { Page } from "@playwright/test";
import { boardWriteHeaders, expect, test } from "./support/fixtures";
import { fillLoginForm, newAccount, registerAndActivate, signInViaUI, type NewAccount } from "./support/auth_flow";
import { BOARD_ORIGIN } from "./support/env";
import { dex, signInAtDex } from "@e2e-support/dex";
import { openAccountPage, signOutViaHeader } from "./support/widgets";

const signInHeading = (page: Page) => page.getByRole("heading", { name: "Sign in" });
const board = (page: Page) => page.getByRole("heading", { name: "File a ticket" });

const pathAndSearch = (page: Page) => {
  const url = new URL(page.url());
  return url.pathname + url.search;
};

// signedInWithTicket signs account in on page and files a ticket in its
// tenant, answering the ticket's link.
async function signedInWithTicket(
  page: Page,
  account: NewAccount,
  title: string,
): Promise<{ id: number; link: string }> {
  await registerAndActivate(page, account);
  await signInViaUI(page, account.email, account.password);
  await expect(board(page)).toBeVisible();
  const request = page.request;
  const me = await request.get("/api/auth/me");
  const tenant = (await me.json()).tenant_id;
  const resp = await request.post("/api/tickets", { data: { title }, headers: await boardWriteHeaders(request) });
  expect(resp.status(), await resp.text()).toBe(201);
  const { id } = await resp.json();
  return { id, link: `/tickets/${id}?from=blocked&tenant=${tenant}` };
}

// A deliberate sign-out forgets the page (the next person to sign in on
// this browser starts from the board), so each test signs out from the
// board and then follows the link as a fresh anonymous visitor would.
async function signOut(page: Page): Promise<void> {
  await signOutViaHeader(page);
  await expect(signInHeading(page)).toBeVisible();
}

test("a ticket link opened signed out lands on that ticket after a password sign-in", async ({ anonPage }) => {
  const account = newAccount("return-password");
  const title = `return-password-${Date.now()}`;
  const ticket = await signedInWithTicket(anonPage, account, title);

  await anonPage.goto(ticket.link);
  await expect(anonPage.getByRole("heading", { name: `#${ticket.id} ${title}` })).toBeVisible();
  await signOut(anonPage);
  // Signed out on purpose from the ticket: /login does not carry it.
  expect(pathAndSearch(anonPage)).toBe("/login");

  await anonPage.goto(ticket.link);
  await expect(signInHeading(anonPage)).toBeVisible();
  expect(pathAndSearch(anonPage)).toBe(`/login?${new URLSearchParams({ redirect: ticket.link })}`);

  await fillLoginForm(anonPage, account.email, account.password);
  await expect(anonPage.getByRole("heading", { name: `#${ticket.id} ${title}` })).toBeVisible();
  expect(pathAndSearch(anonPage)).toBe(ticket.link);
});

test("a return path to another site lands on the board instead", async ({ anonPage }) => {
  const account = newAccount("return-offsite");
  await registerAndActivate(anonPage, account);

  for (const redirect of ["//evil.example", "/.//evil.example", "https://evil.example/"]) {
    await anonPage.goto(`/login?${new URLSearchParams({ redirect })}`);
    await fillLoginForm(anonPage, account.email, account.password);
    await expect(board(anonPage), redirect).toBeVisible();
    expect(new URL(anonPage.url()).origin, redirect).toBe(BOARD_ORIGIN);
    expect(pathAndSearch(anonPage), redirect).toBe("/");
    await signOut(anonPage);
  }
});

test("a ticket link opened signed out lands on that ticket after a Dex sign-in", async ({ anonPage }) => {
  const account = newAccount("return-dex");
  const title = `return-dex-${Date.now()}`;
  const ticket = await signedInWithTicket(anonPage, account, title);
  await openAccountPage(anonPage);
  await anonPage.getByRole("button", { name: "Link Dex" }).click();
  await signInAtDex(anonPage, dex.bob);
  await expect(anonPage).toHaveURL(/\/account$/);
  await signOut(anonPage);

  await anonPage.goto(ticket.link);
  await expect(signInHeading(anonPage)).toBeVisible();
  await anonPage.getByRole("button", { name: "Continue with Dex" }).click();
  await signInAtDex(anonPage, dex.bob);

  await expect(anonPage.getByRole("heading", { name: `#${ticket.id} ${title}` })).toBeVisible();
  expect(pathAndSearch(anonPage)).toBe(ticket.link);
});
