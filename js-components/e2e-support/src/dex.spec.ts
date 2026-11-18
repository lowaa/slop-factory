import { expect, test, type Page } from "@playwright/test";

import { dex, signInAtDex } from "./dex";

// A page served at `url` with the form fields Dex's login page has, which
// records what is submitted instead of posting it.
async function dexLoginAt(page: Page, url: string): Promise<void> {
  await page.route(url, (route) =>
    route.fulfill({
      contentType: "text/html",
      body: `<form onsubmit="event.preventDefault(); window.submitted = [login.value, password.value]">
        <input id="login"><input id="password" type="password"><button id="submit-login">Login</button>
      </form>`,
    }),
  );
  await page.goto(url);
}

test("fills Dex's login form with the user's credentials and submits it", async ({ page }) => {
  await dexLoginAt(page, "http://dex.test/dex/auth/local/login?x=1");
  await signInAtDex(page, dex.alice);
  expect(await page.evaluate(() => (window as unknown as { submitted: string[] }).submitted)).toEqual([
    "alice@example.com",
    "alice-e2e-password",
  ]);
});

test("refuses a page that is not Dex's login, without typing credentials into it", async ({ page }) => {
  await dexLoginAt(page, "http://frontend.test/login");
  await expect(signInAtDex(page, dex.bob)).rejects.toThrow();
  expect(await page.locator("#login").inputValue()).toBe("");
});
