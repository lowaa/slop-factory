// The Tenant settings page end to end: the tenant-wide switch it shares with
// the header, and a member's own settings in the tenant. The unit suites
// stub the API; what only this shows is that the page writes the scope it
// says it does -- a "You in this tenant" switch wired to the global scope
// passes every stubbed test and leaks the choice into the person's other
// tenants, and to nobody else here, so the other-member check is the one
// that has to fail for it.
import { boardWriteHeaders, expect, test } from "./support/fixtures";
import * as mailhog from "./support/mailhog";
import { independentPage, newAccount, registerAndActivate, fillLoginForm } from "./support/auth_flow";
import { openHeaderTab } from "./support/widgets";

const PROCESSING = "Ticket processing";
const AUTO_STATUS = "Parking or cancelling a ticket";

test("changing ticket processing on the page moves the header switch, and the history names this user", async ({
  page,
}) => {
  await page.goto("/");
  await openHeaderTab(page, "Tenant settings");
  await expect(page).toHaveURL(/\/settings$/);

  const tenant = page.getByRole("region", { name: "This tenant" });
  const header = page.locator(".processing-toggle").getByRole("switch", { name: PROCESSING });
  // The page lists every tenant setting, so the row is picked by its switch.
  const processingRow = tenant.getByRole("listitem").filter({ has: page.getByRole("switch", { name: PROCESSING }) });
  await expect(processingRow.getByText("Default value")).toBeVisible();
  await expect(header).toHaveAttribute("aria-checked", "true");

  await tenant.getByRole("switch", { name: PROCESSING }).click();
  await expect(tenant.getByRole("switch", { name: PROCESSING })).toHaveAttribute("aria-checked", "false");
  await expect(header).toHaveAttribute("aria-checked", "false");
  await expect(processingRow.getByText("Changed from its default")).toBeVisible();

  await tenant.getByRole("button", { name: `History of ${PROCESSING}` }).click();
  const changes = tenant.getByRole("list", { name: "Changes" }).getByRole("listitem");
  await expect(changes).toHaveCount(1);
  await expect(changes.first()).toContainText("you");
  await expect(changes.first()).toContainText("on → off");

  // And the other way: the header changes it, the page follows.
  await header.click();
  await expect(header).toHaveAttribute("aria-checked", "true");
  await expect(tenant.getByRole("switch", { name: PROCESSING })).toHaveAttribute("aria-checked", "true");
  await expect(changes).toHaveCount(2);
});

test("an auto-approve setting survives a reload, and is off for another member of the tenant", async ({
  page,
  browser,
}) => {
  const other = newAccount("tenant-settings");
  const { context, page: member } = await independentPage(browser);
  await registerAndActivate(member, other);

  await page.goto("/");
  await openHeaderTab(page, "Members");
  await page.getByLabel("Email").fill(other.email);
  await page.getByRole("button", { name: "Send invite" }).click();
  await expect(page.getByText(other.email)).toBeVisible();
  const body = await mailhog.waitForEmail(other.email, "invited you to slop-haus");
  await member.goto("/login");
  await fillLoginForm(member, other.email, other.password);
  await expect(member.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await member.goto(mailhog.linkPath(body));
  await expect(member.getByRole("heading", { name: "You're invited" })).toBeVisible();
  await member.getByRole("button", { name: "Accept" }).click();
  await expect(member.getByRole("heading", { name: "File a ticket" })).toBeVisible();

  await openHeaderTab(page, "Tenant settings");
  const mine = page.getByRole("region", { name: "You in this tenant" });
  const toggle = mine.getByRole("switch", { name: AUTO_STATUS });
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-checked", "true");

  await page.reload();
  await expect(page.getByRole("region", { name: "You in this tenant" }).getByRole("switch", { name: AUTO_STATUS })).toHaveAttribute(
    "aria-checked",
    "true",
  );

  // The other member, in the same tenant, still has it off.
  await openHeaderTab(member, "Tenant settings");
  await expect(
    member.getByRole("region", { name: "You in this tenant" }).getByRole("switch", { name: AUTO_STATUS }),
  ).toHaveAttribute("aria-checked", "false");
  await context.close();

  // Not written to the global scope either.
  const headers = await boardWriteHeaders(page.request);
  const global = await page.request.get("/api/me/settings", { headers });
  const names = ((await global.json()).settings as { name: string; bool_val: boolean | null }[]).filter(
    (s) => s.name === "approvals.auto.status" && s.bool_val,
  );
  expect(names).toEqual([]);
});
