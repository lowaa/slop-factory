import { expect, type Page } from "@playwright/test";

// Dex's static users in an e2e stack (the suite's dex/config.yaml): public
// test credentials. Nothing ties them to an account until a test does, and
// the suites reset their accounts before each test, so every test starts with
// both unattached.
export interface DexUser {
  email: string;
  password: string;
}

export const dex = {
  alice: { email: "alice@example.com", password: "alice-e2e-password" },
  bob: { email: "bob@example.com", password: "bob-e2e-password" },
} satisfies Record<string, DexUser>;

// signInAtDex fills in Dex's own login page, which the page has just been
// sent to, as u.
export async function signInAtDex(page: Page, u: DexUser): Promise<void> {
  await expect(page).toHaveURL(/\/dex\/auth\//);
  await page.locator("#login").fill(u.email);
  await page.locator("#password").fill(u.password);
  await page.locator("#submit-login").click();
}
