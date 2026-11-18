// A signed-in person starts a tenant of their own from the header. A fresh
// account has one tenant, so no tenant switcher: the entry has to be there
// without it. Creating lands on the new, empty tenant's board, and the
// switcher then offers the way back.
import { expect, test } from "./support/fixtures";
import { fillLoginForm, independentPage, newAccount, registerAndActivate } from "./support/auth_flow";
import { chooseFromDropdown, openNewTenant } from "./support/widgets";

test("a single-tenant person creates a tenant, lands on its empty board, and switches back", async ({ browser }) => {
  const account = newAccount("new-tenant");
  const { context, page } = await independentPage(browser);
  try {
    await registerAndActivate(page, account);
    await page.goto("/login");
    await fillLoginForm(page, account.email, account.password);
    await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Tenant" })).toHaveCount(0);

    await openNewTenant(page);
    await expect(page.getByText(/You own 1 of 3/)).toBeVisible();
    await page.getByLabel("Tenant name").fill("Side project");
    await page.getByRole("button", { name: "Create tenant" }).click();

    await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Tenant" })).toHaveText("Side project");

    await chooseFromDropdown(page, "Tenant", account.displayName);
    await expect(page.getByRole("combobox", { name: "Tenant" })).toHaveText(account.displayName);
  } finally {
    await context.close();
  }
});

test("a clashing name is refused with the reason, and the form stays", async ({ browser }) => {
  const account = newAccount("new-tenant-clash");
  const { context, page } = await independentPage(browser);
  try {
    await registerAndActivate(page, account);
    await page.goto("/login");
    await fillLoginForm(page, account.email, account.password);
    await openNewTenant(page);
    await page.getByLabel("Tenant name").fill(account.displayName);
    await page.getByRole("button", { name: "Create tenant" }).click();
    await expect(page.getByRole("alert")).toContainText("already belong");
    await expect(page.getByRole("button", { name: "Create tenant" })).toBeVisible();
  } finally {
    await context.close();
  }
});
