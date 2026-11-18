import { expect, test } from "./support/fixtures";
import { credentialAgentViaCodeFlow } from "./support/oauth";

// What chat used to be the only place for: a queue that idle agents cannot
// work because every ticket they could take collides with a held scope.
// The board says so itself, and clears it live -- over the board's change
// stream, with no reload -- once the scope frees.
test("the board says when idle agents are blocked on a scope, and clears it when the scope frees", async ({ page, api }) => {
  const stamp = Date.now();
  const scope = `server/blockage-${stamp}/`;
  const holder = await credentialAgentViaCodeFlow(api, `holder-${stamp}`);
  await credentialAgentViaCodeFlow(api, `idler-${stamp}`);
  const holderAuth = { Authorization: `Bearer ${holder.accessToken}` };

  const held = await (await api.post("/api/tickets", { data: { title: `Held ${stamp}`, paths: [{ path: scope }] } })).json();
  const claimed = await api.post("/api/claims", { headers: holderAuth, data: { ticket_id: held.id } });
  expect(claimed.status(), await claimed.text()).toBe(200);
  const waiting = await (await api.post("/api/tickets", { data: { title: `Waiting ${stamp}`, paths: [{ path: scope }] } })).json();

  await page.goto("/");
  const banner = page.getByRole("status", { name: "Queue blocked" });
  await expect(banner).toBeVisible();
  await expect(banner).toContainText(`#${waiting.id} Waiting ${stamp}`);
  await expect(banner).toContainText(`#${held.id} Held ${stamp}`);
  await expect(banner).toContainText(scope);

  let navigated = false;
  page.on("framenavigated", () => {
    navigated = true;
  });
  const released = await api.post("/api/claims/release", { headers: holderAuth, data: { reason: "freeing the scope" } });
  expect(released.status(), await released.text()).toBe(200);

  await expect(banner).toHaveCount(0);
  expect(navigated, "the banner should clear over the open stream, not a reload").toBe(false);
});

test("a queue with free work is not reported as blocked", async ({ page, api }) => {
  const stamp = Date.now();
  const holder = await credentialAgentViaCodeFlow(api, `busy-${stamp}`);
  await credentialAgentViaCodeFlow(api, `free-${stamp}`);
  const held = await (
    await api.post("/api/tickets", { data: { title: `Held ${stamp}`, paths: [{ path: `server/a-${stamp}/` }] } })
  ).json();
  await api.post("/api/claims", { headers: { Authorization: `Bearer ${holder.accessToken}` }, data: { ticket_id: held.id } });
  await api.post("/api/tickets", { data: { title: `Collides ${stamp}`, paths: [{ path: `server/a-${stamp}/` }] } });
  await api.post("/api/tickets", { data: { title: `Free ${stamp}`, paths: [{ path: `server/b-${stamp}/` }] } });

  await page.goto("/");
  await expect(page.getByText(`Free ${stamp}`)).toBeVisible();
  await expect(page.getByRole("status", { name: "Queue blocked" })).toHaveCount(0);
});
