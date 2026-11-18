// The "Parked" ticket status end to end: a ticket a human is keeping but
// not planning to do leaves the board and the claim queue, comes back with
// "Include parked", and unparks into Preparing -- not the queue.
import { expect, test } from "./support/fixtures";
import { API_BASE_URL } from "./support/env";
import { credentialAgentViaCodeFlow } from "./support/oauth";

test("a parked ticket leaves the board, returns with Include parked, and unparks into Preparing", async ({
  page,
  api,
}) => {
  const title = `parked-flow-${Date.now()}`;
  const created = await api.post("/api/tickets", { data: { title, status: "preparing" } });
  expect(created.status(), await created.text()).toBe(201);

  const column = (name: string) =>
    page.locator(".board__column").filter({ has: page.getByRole("heading", { name, exact: true }) });

  await page.goto("/");
  await expect(column("Preparing").getByText(title)).toBeVisible();
  await page.getByText(title).click();
  await expect(page.getByRole("heading", { name: new RegExp(title) })).toBeVisible();

  await page.getByRole("button", { name: "Park", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Status" })).toHaveText("parked");
  await expect(page.getByText("Parked.", { exact: true })).toBeVisible();

  // Not offered to an agent while parked.
  const { accessToken } = await credentialAgentViaCodeFlow(api, `parked-agent-${Date.now()}`);
  const claim = await api.post("/api/claims", { headers: { Authorization: `Bearer ${accessToken}` }, data: {} });
  expect(claim.status(), await claim.text()).toBe(200);
  expect((await claim.json()).ticket, "parked must not be claimable").toBeNull();

  await page.getByRole("link", { name: "← Back to board" }).click();
  await expect(page.getByRole("heading", { name: "Open", exact: true })).toBeVisible();
  await expect(page.getByText(title)).not.toBeVisible();
  await expect(page.getByRole("heading", { name: "Parked", exact: true })).not.toBeVisible();

  await page.getByRole("checkbox", { name: "Include parked" }).check();
  await expect(column("Parked").getByText(title)).toBeVisible();
  await expect(page).toHaveURL(/[?&]parked=true/);

  // The filter is in the address, so a reload keeps it.
  await page.reload();
  await expect(page.getByRole("checkbox", { name: "Include parked" })).toBeChecked();
  await expect(column("Parked").getByText(title)).toBeVisible();

  await column("Parked").getByText(title).click();
  await page.getByRole("button", { name: "Unpark" }).click();
  await expect(page.getByRole("combobox", { name: "Status" })).toHaveText("preparing");
  await expect(page.getByText("Not in the queue.")).toBeVisible();

  await page.getByRole("link", { name: "← Back to board" }).click();
  await expect(column("Preparing").getByText(title)).toBeVisible();
  await expect(column("Parked").getByText(title)).not.toBeVisible();
});

// Parking and unparking are a person's call: an agent's credential reaches
// neither, and a parked ticket cannot be unparked straight into the queue.
test("an agent cannot park or unpark, and unparking goes only to Preparing", async ({ api, playwright }) => {
  const { accessToken } = await credentialAgentViaCodeFlow(api, `not-parking-${Date.now()}`);
  const created = await api.post("/api/tickets", { data: { title: `idea-${Date.now()}`, status: "open" } });
  const ticket = await created.json();

  const anon = await playwright.request.newContext({ baseURL: API_BASE_URL });
  const agentPark = await anon.patch(`/api/tickets/${ticket.id}`, {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { status: "parked" },
  });
  expect(agentPark.status()).toBe(401);

  const parked = await api.patch(`/api/tickets/${ticket.id}`, { data: { status: "parked" } });
  expect(parked.status(), await parked.text()).toBe(200);

  const agentUnpark = await anon.patch(`/api/tickets/${ticket.id}`, {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { status: "preparing" },
  });
  expect(agentUnpark.status()).toBe(401);
  await anon.dispose();

  const toOpen = await api.patch(`/api/tickets/${ticket.id}`, { data: { status: "open" } });
  expect(toOpen.status(), await toOpen.text()).toBe(409);

  const got = await api.get(`/api/tickets/${ticket.id}`);
  expect((await got.json()).status).toBe("parked");
});
