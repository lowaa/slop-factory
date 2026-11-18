// The browser's side of Remote Control: where a person reaches it, and which
// machines they are offered. Running a session end to end against a host is
// conversation_chat.spec.ts.
import type { APIRequestContext } from "@playwright/test";

import { DEV_USER_PASSWORD } from "./support/env";
import { expect, test } from "./support/fixtures";
import { openHeaderTab } from "./support/widgets";
import { registerConductor } from "./support/api_helpers";

// Registering needs a recent sign-in. Each password confirmation counts
// against a rate limit shared with the other specs', so a test confirms once.
async function reauth(api: APIRequestContext): Promise<void> {
  const resp = await api.post("/api/auth/reauth/password", { data: { password: DEV_USER_PASSWORD } });
  expect(resp.ok(), await resp.text()).toBeTruthy();
}

test("the Remote Control tab sits in the Agents group and lists the person's own machines, a disabled one only to enable again", async ({ page, api }) => {
  const stamp = Date.now();
  const mine = `rc-mine-${stamp}`;
  const off = `rc-off-${stamp}`;
  await reauth(api);
  await registerConductor(api, mine);
  await registerConductor(api, off);
  const list = await (await api.get("/api/conductors")).json();
  const offId = list.conductors.find((c: { name: string }) => c.name === off).id;
  expect((await api.patch(`/api/conductors/${offId}`, { data: { disabled: true } })).ok()).toBeTruthy();

  await page.goto("/");
  // allTextContents does not wait, and the nav renders after the page loads.
  await expect
    .poll(async () => (await page.locator(".app__nav").getByRole("link").allTextContents()).slice(5, 8))
    .toEqual(["Remote Control", "Workers", "Workspace"]);
  await openHeaderTab(page, "Remote Control");

  const sessions = page.locator(".sessions-panel");
  await expect(sessions.getByRole("region", { name: mine })).toBeVisible();
  // A disabled machine is listed to be enabled again, but offers no session.
  const disabled = sessions.getByRole("region", { name: off });
  await expect(disabled.getByText("Disabled")).toBeVisible();
  await expect(disabled.getByRole("button", { name: "+ New session" })).toHaveCount(0);
  await expect(sessions.getByRole("region", { name: mine }).getByRole("button", { name: "+ New session" })).toBeVisible();
});

test("the board no longer offers Draft a ticket", async ({ page }) => {
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "File a ticket" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Draft a ticket" })).toHaveCount(0);
});

test("a ticket's Discuss shortcut opens a new session with the prompt in the message box, unsent", async ({ page, api }) => {
  const stamp = Date.now();
  await reauth(api);
  await registerConductor(api, `rc-discuss-${stamp}`);
  const ticket = await (await api.post("/api/tickets", { data: { title: `Discuss ${stamp}` } })).json();

  await page.goto(`/tickets/${ticket.id}`);
  await page.getByRole("button", { name: "Discuss in Remote Control" }).click();

  await expect(page).toHaveURL(/\/remote-control\?/);
  await expect(page.getByRole("textbox", { name: "Message" })).toHaveValue(`discuss slop-haus #${ticket.id}`);
  const conductor = (await (await api.get("/api/conductors")).json()).conductors.find((c: { name: string }) => c.name === `rc-discuss-${stamp}`);
  const sessions = await (await api.get(`/api/conductors/${conductor.id}/conversations`)).json();
  expect(sessions.conversations).toEqual([]);
});

test("a session address on a machine that is not the person's says so", async ({ page }) => {
  await page.goto("/remote-control?conductor=11111111-1111-4111-8111-111111111111&session=22222222-2222-4222-8222-222222222222");

  await expect(page.getByRole("alert")).toContainText("not on one of your remote controls");
});
