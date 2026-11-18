import { expect, test } from "./support/fixtures";
import { API_BASE_URL } from "./support/env";
import { credentialAgentViaCodeFlow } from "./support/oauth";
import { openHeaderTab } from "./support/widgets";

// Agent roles decide which tickets an agent may claim, so only a signed-in
// person may manage them: an agent that could reach these routes could
// grant itself work.

test("creates a role, gives it to an agent, and it survives a reload", async ({ page, api }) => {
  const agentName = `role-holder-${Date.now()}`;
  await credentialAgentViaCodeFlow(api, agentName);
  const roleName = `reviewer-${Date.now()}`;

  await page.goto("/");
  await openHeaderTab(page, "Workers");

  const rolesSection = page.locator(".agent-roles");
  await rolesSection.getByLabel("Role name").fill(roleName);
  await rolesSection.getByRole("button", { name: "Add role" }).click();
  await expect(rolesSection.getByText(roleName, { exact: true })).toBeVisible();

  const row = page.locator(".agents-panel__row", { hasText: agentName });
  await row.getByRole("button", { name: "Edit roles" }).click();
  const editor = row.getByRole("group", { name: `Roles for ${agentName}` });
  await editor.getByRole("checkbox", { name: roleName }).check();
  await editor.getByRole("button", { name: "Save" }).click();

  await expect(row.locator(".agents-panel__role-chip", { hasText: roleName })).toBeVisible();

  await page.reload();
  await openHeaderTab(page, "Workers");
  await expect(
    page.locator(".agents-panel__row", { hasText: agentName }).locator(".agents-panel__role-chip", { hasText: roleName }),
  ).toBeVisible();
});

// The agent's own token, with no user session riding along -- a fresh
// request context (rather than `api`, which is always signed in as the
// board's dev user -- see support/fixtures.ts) so a 401 here proves the
// bearer token itself is refused, not just that the request happened to
// lack a cookie.
test("an agent's own token is refused at every agent-roles route", async ({ api, playwright }) => {
  const agentName = `role-climber-${Date.now()}`;
  const { agentId, accessToken } = await credentialAgentViaCodeFlow(api, agentName);
  const asAgent = { Authorization: `Bearer ${accessToken}` };

  const anon = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    const list = await anon.get("/api/agent-roles", { headers: asAgent });
    expect(list.status(), await list.text()).toBe(401);

    const create = await anon.post("/api/agent-roles", {
      headers: asAgent,
      data: { name: `should-not-exist-${Date.now()}` },
    });
    expect(create.status(), await create.text()).toBe(401);

    const setRoles = await anon.put(`/api/agents/${agentId}/roles`, {
      headers: asAgent,
      data: { role_ids: [] },
    });
    expect(setRoles.status(), await setRoles.text()).toBe(401);

    // The token is good -- it is refused for being an agent's.
    const own = await anon.get("/api/workspace/update_script", { headers: asAgent });
    expect(own.status(), await own.text()).toBe(200);
  } finally {
    await anon.dispose();
  }
});
