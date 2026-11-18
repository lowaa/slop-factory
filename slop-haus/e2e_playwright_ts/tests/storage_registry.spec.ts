// The cookie and storage registry check (go-privacy/README.md, section 9):
// after the main flows, every cookie, localStorage, sessionStorage and
// IndexedDB key the browser holds must be declared in the data inventory
// (server/privacy/inventory.yaml), and nothing declared under a purpose that
// needs consent may exist without it. A new key added to the app fails here
// until the inventory says what it is -- which also puts it in the cookie
// policy.
import { expect, test } from "@/support/fixtures";
import { newAccount, registerAndActivate, signInViaUI } from "@/support/auth_flow";
import { observeStorage, storageProblems } from "@e2e-support/storage_registry";
import { assertStorageDeclared, loadRegistry } from "@/support/storage_registry";
import { openAccountPage, openHeaderTab } from "@/support/widgets";

test("the registry is read from the inventory and declares the session cookies", () => {
  const registry = loadRegistry();
  const names = registry.map((d) => d.key);
  expect(names).toEqual(expect.arrayContaining(["slop_haus_session", "slop_haus_refresh", "slop_haus_consent"]));
});

test("signing in leaves only declared storage", async ({ anonPage }) => {
  // IndexedDB is denied on the initial about:blank page, so load the app first.
  await anonPage.goto("/login");
  await assertStorageDeclared(anonPage, "the first visit");

  const account = newAccount("registry");
  await registerAndActivate(anonPage, account);
  await signInViaUI(anonPage, account.email, account.password);
  await expect(anonPage.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  // Signed in is when the session and refresh cookies exist, so make sure
  // the check saw them rather than passing on an empty browser.
  const seen = (await observeStorage(anonPage)).map((s) => s.key);
  expect(seen).toEqual(expect.arrayContaining(["slop_haus_session", "slop_haus_refresh"]));
  await assertStorageDeclared(anonPage, "signing in");
});

test("the main flows leave only declared storage", async ({ page, api }) => {
  const title = `registry-${Date.now()}`;
  const created = await api.post("/api/tickets", { data: { title, status: "open" } });
  expect(created.status(), await created.text()).toBe(201);
  const { id } = (await created.json()) as { id: number };

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await assertStorageDeclared(page, "the board");

  await page.goto(`/tickets/${id}`);
  await expect(page.getByText(title).first()).toBeVisible();
  await assertStorageDeclared(page, "a ticket's detail");

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  await expect(page).toHaveURL(/\/workers$/);
  await assertStorageDeclared(page, "workers");

  await openAccountPage(page);
  await expect(page.getByRole("heading", { name: "Sign-in methods" })).toBeVisible();
  await assertStorageDeclared(page, "settings");

  // Refused everything (the fixture's start): no key of a purpose that needs
  // consent may be there.
  const keys = (await observeStorage(page)).map((s) => s.key);
  expect(keys).not.toContain("slop-factory-logging:session-id");
});

// The check has to be able to fail: each of these is a thing it exists to
// catch, put in a browser that is otherwise clean.
test.describe("the check catches what it is for", () => {
  test("a cookie the inventory does not declare", async ({ page }) => {
    await page.goto("/");
    await page.context().addCookies([
      { name: "tracker_id", value: "1", url: page.url() },
    ]);
    await expect(assertStorageDeclared(page, "a rogue cookie")).rejects.toThrow(/undeclared cookie "tracker_id"/);
  });

  test("a localStorage key, a sessionStorage key and an IndexedDB database", async ({ page }) => {
    await page.goto("/");
    await page.evaluate(async () => {
      localStorage.setItem("rogue-local", "1");
      sessionStorage.setItem("rogue-session", "1");
      await new Promise((resolve, reject) => {
        const open = indexedDB.open("rogue-db");
        open.onsuccess = () => resolve(open.result.close());
        open.onerror = () => reject(open.error);
      });
    });
    const problems = storageProblems(await observeStorage(page), loadRegistry(), new Set());
    expect(problems.join("\n")).toMatch(/undeclared local_storage "rogue-local"/);
    expect(problems.join("\n")).toMatch(/undeclared session_storage "rogue-session"/);
    expect(problems.join("\n")).toMatch(/undeclared indexeddb "rogue-db"/);
  });

  test("a declared key of a purpose that needs consent, with no consent", async ({ page }) => {
    await page.goto("/");
    // The logger removes this key when the consent store's first load finds
    // analytics refused, so plant it after that load or the removal races
    // the plant. The button is disabled while the store is loading.
    await expect(page.getByRole("button", { name: "Do Not Sell or Share My Personal Information" })).toBeEnabled();
    // Declared, as a key under analytics; the fixture's visitor refused it.
    await page.evaluate(() => sessionStorage.setItem("slop-factory-logging:session-id", "x"));
    await expect(assertStorageDeclared(page, "a key set without consent")).rejects.toThrow(
      /"slop-factory-logging:session-id" is set without consent for analytics/,
    );
  });
});
