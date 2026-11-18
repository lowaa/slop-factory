// The consent banner and the choices behind it, through a real browser and
// the real server. Each spec starts with no decision made (consentStart:
// "none"); the other specs start with everything refused, see
// support/fixtures.ts.
import type { Page } from "@playwright/test";

import { expect, test } from "@/support/fixtures";
import { query } from "@/support/db";
import { fillLoginForm } from "@/support/auth_flow";
import { DEV_USER_EMAIL, DEV_USER_PASSWORD } from "@/support/env";
import { grantedPurposes } from "@e2e-support/storage_registry";
import { openAccountPage } from "./support/widgets";

test.use({ consentStart: "none" });

// Headroom over the config's 15s expect timeout for the slow e2e host, still
// well inside the 60s test timeout.
const CONSENT_WRITE_TIMEOUT_MS = 20_000;

const CONSENT_COOKIE = "slop_haus_consent";

const banner = (page: Page) => page.getByRole("region", { name: "Your privacy choices" });
const dialog = (page: Page) => page.getByRole("dialog", { name: "Cookie preferences" });

// The server records a decision after the request that triggered it has been
// answered, and on a slow host that is after the UI has moved on, so a test
// that reads the log once right after a UI step races the write. Polling
// toEqual (not "at least one row") keeps a duplicate write a failure: the
// poll only settles on exactly the expected rows. A duplicate landing after it
// settled is not seen; a double merge is also refused by the merge-token
// unique index, which the server's own tests cover.
async function expectConsentRows<T extends Record<string, unknown>>(sql: string, expected: T[]) {
  await expect
    .poll(() => query<T>(sql, [DEV_USER_EMAIL]), {
      message: "the consent write lands, and only once",
      timeout: CONSENT_WRITE_TIMEOUT_MS,
    })
    .toEqual(expected);
}

async function consentCookie(page: Page) {
  return (await page.context().cookies()).find((c) => c.name === CONSENT_COOKIE);
}

test.describe("a first visit", () => {
  test("shows the banner, sets nothing, and does not block the app", async ({ anonPage }) => {
    await anonPage.goto("/login");

    await expect(banner(anonPage)).toBeVisible();
    await expect(banner(anonPage).getByRole("button", { name: "Reject all" })).toBeVisible();
    await expect(banner(anonPage).getByRole("button", { name: "Accept all" })).toBeVisible();
    expect(await consentCookie(anonPage), "nothing is stored before the visitor decides").toBeUndefined();
    // No cookie wall: the page behind it is usable.
    await expect(anonPage.getByLabel("Email")).toBeEditable();
    expect(await grantedPurposes(anonPage.context())).toEqual(new Set(["necessary"]));
  });

  test("links the cookie policy, a public draft", async ({ anonPage }) => {
    await anonPage.goto("/login");
    await banner(anonPage).getByRole("link", { name: "Read the cookie policy" }).click();

    await expect(anonPage).toHaveURL(/\/legal\/cookie_policy$/);
    await expect(anonPage.getByRole("heading", { name: "Cookie policy" })).toBeVisible();
    await expect(anonPage.getByText(/DRAFT/).first()).toBeVisible();
  });
});

test.describe("Reject all and Accept all", () => {
  test("Reject all refuses every optional purpose and the banner stays gone on reload", async ({ anonPage }) => {
    await anonPage.goto("/login");
    await banner(anonPage).getByRole("button", { name: "Reject all" }).click();

    await expect(banner(anonPage)).toBeHidden();
    const cookie = await consentCookie(anonPage);
    expect(cookie?.httpOnly, "the server sets it, and no script can read it").toBe(true);
    expect(await grantedPurposes(anonPage.context())).toEqual(new Set(["necessary"]));

    await anonPage.reload();
    await expect(anonPage.getByRole("button", { name: "Sign in" })).toBeVisible();
    await expect(banner(anonPage)).toBeHidden();
  });

  test("Accept all grants analytics and marketing", async ({ anonPage }) => {
    await anonPage.goto("/login");
    await banner(anonPage).getByRole("button", { name: "Accept all" }).click();

    await expect(banner(anonPage)).toBeHidden();
    expect(await grantedPurposes(anonPage.context())).toEqual(new Set(["necessary", "analytics", "marketing"]));

    await anonPage.reload();
    await expect(banner(anonPage)).toBeHidden();
  });
});

test.describe("withdrawal", () => {
  test("a choice is changed from the footer, as easily as it was made", async ({ anonPage }) => {
    await anonPage.goto("/login");
    await banner(anonPage).getByRole("button", { name: "Accept all" }).click();
    await expect(banner(anonPage)).toBeHidden();
    expect(await grantedPurposes(anonPage.context())).toContain("analytics");

    await anonPage.getByRole("button", { name: "Cookie preferences" }).click();
    await expect(dialog(anonPage)).toBeVisible();
    await expect(dialog(anonPage).getByRole("switch", { name: "Analytics" })).toBeChecked();
    await dialog(anonPage).getByRole("switch", { name: "Analytics" }).uncheck();
    await dialog(anonPage).getByRole("button", { name: "Save preferences" }).click();
    await expect(dialog(anonPage)).toBeHidden();

    // Analytics is withdrawn; marketing, not touched, stands.
    expect(await grantedPurposes(anonPage.context())).toEqual(new Set(["necessary", "marketing"]));
    await anonPage.reload();
    await expect(banner(anonPage)).toBeHidden();
  });

  test("Account's settings show the choices and can withdraw them", async ({ page }) => {
    await page.goto("/");
    await banner(page).getByRole("button", { name: "Accept all" }).click();
    await openAccountPage(page);

    const settings = page.getByRole("region", { name: "Cookie preferences" });
    await expect(settings.getByText("Analytics:")).toContainText("On");
    await settings.getByRole("button", { name: "Change cookie preferences" }).click();
    await dialog(page).getByRole("button", { name: "Reject all" }).click();

    await expect(settings.getByText("Analytics:")).toContainText("Off");
    expect(await grantedPurposes(page.context())).toEqual(new Set(["necessary"]));
  });

  test("Do Not Sell or Share refuses marketing and leaves analytics alone", async ({ anonPage }) => {
    await anonPage.goto("/login");
    await banner(anonPage).getByRole("button", { name: "Accept all" }).click();

    await anonPage.getByRole("button", { name: "Do Not Sell or Share My Personal Information" }).click();

    await expect(anonPage.getByText("Your choice was saved.")).toBeVisible();
    expect(await grantedPurposes(anonPage.context())).toEqual(new Set(["necessary", "analytics"]));
  });
});

test.describe("signed in", () => {
  test("a decision is recorded against the account, with where it was made", async ({ page }) => {
    await page.goto("/");
    await banner(page).getByRole("button", { name: "Reject all" }).click();
    await expect(banner(page)).toBeHidden();

    await expectConsentRows(
      `SELECT e.purpose, e.granted, e.source, e.document_kind
         FROM global_consent_events e JOIN global_users u ON u.id = e.user_id
        WHERE u.email = $1 ORDER BY e.purpose`,
      [
        { purpose: "analytics", granted: false, source: "banner", document_kind: "cookie_policy" },
        { purpose: "marketing", granted: false, source: "banner", document_kind: "cookie_policy" },
      ],
    );
  });

  test("a choice made signed out joins the record at sign-in, once", async ({ anonPage }) => {
    await anonPage.goto("/login");
    await banner(anonPage).getByRole("button", { name: "Accept all" }).click();
    await expect(banner(anonPage)).toBeHidden();

    await fillLoginForm(anonPage, DEV_USER_EMAIL, DEV_USER_PASSWORD);
    await expect(anonPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();
    // The provider reads the state as the app loads and after sign-in.
    await anonPage.reload();
    await expect(anonPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();

    await expectConsentRows(
      `SELECT e.purpose, e.granted, e.merged_from_anonymous
         FROM global_consent_events e JOIN global_users u ON u.id = e.user_id
        WHERE u.email = $1 ORDER BY e.purpose`,
      [
        { purpose: "analytics", granted: true, merged_from_anonymous: true },
        { purpose: "marketing", granted: true, merged_from_anonymous: true },
      ],
    );
    await expect(banner(anonPage)).toBeHidden();
  });
});

test.describe("Global Privacy Control", () => {
  // The header is what the server reads; the browser's own signal is only
  // used to explain it.
  test.use({ extraHTTPHeaders: { "Sec-GPC": "1" } });

  test("is honoured: Accept all grants analytics but never marketing", async ({ anonPage }) => {
    await anonPage.goto("/login");
    await expect(banner(anonPage).getByTestId("gpc-notice")).toBeVisible();

    await banner(anonPage).getByRole("button", { name: "Accept all" }).click();
    await expect(banner(anonPage)).toBeHidden();

    expect(await grantedPurposes(anonPage.context())).toEqual(new Set(["necessary", "analytics"]));
    await anonPage.getByRole("button", { name: "Cookie preferences" }).click();
    await expect(dialog(anonPage).getByRole("switch", { name: "Marketing" })).toBeDisabled();
    await expect(dialog(anonPage).getByRole("switch", { name: "Marketing" })).not.toBeChecked();
  });

  test("is a persistent opt-out on a signed-in account, recorded as such", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();

    await expectConsentRows(
      `SELECT e.purpose, e.granted, e.source
         FROM global_consent_events e JOIN global_users u ON u.id = e.user_id
        WHERE u.email = $1`,
      [{ purpose: "marketing", granted: false, source: "gpc" }],
    );
  });
});
