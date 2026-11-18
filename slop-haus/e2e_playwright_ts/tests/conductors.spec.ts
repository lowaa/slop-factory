// Remote controls (conductors, in the API) end to end: registered from the
// Remote Control tab by the signed-in person, owned by them, and able to propose like a console -- and nothing
// else. The Go tests prove each ownership rule against the database; this
// shows the path through the deployed stack with real tokens.
import type { APIRequestContext } from "@playwright/test";

import { API_BASE_URL, DEV_USER_PASSWORD } from "./support/env";
import { expect, test } from "./support/fixtures";
import { McpSession } from "./support/mcp";
import { credentialAgentViaCodeFlow, credentialExistingAgentViaCodeFlow } from "./support/oauth";
import { openHeaderTab } from "./support/widgets";

// Registering one proposes on the person's behalf, as the console flag does,
// so it needs a recent sign-in, which the worker's session may have outlived.
async function reauth(api: APIRequestContext): Promise<void> {
  const resp = await api.post("/api/auth/reauth/password", { data: { password: DEV_USER_PASSWORD } });
  expect(resp.ok(), await resp.text()).toBeTruthy();
}

interface Conductor {
  id: string;
  name: string;
  disabled_at: string | null;
  owned_by_me: boolean;
}

async function listConductors(api: APIRequestContext): Promise<{ conductors: Conductor[]; others: Conductor[] }> {
  const resp = await api.get("/api/conductors");
  expect(resp.status(), await resp.text()).toBe(200);
  return resp.json();
}

test("registers a remote control from the Remote Control tab, shows the setup command once, and disables it from the sidebar", async ({
  page,
  api,
}) => {
  const name = `conductor-ui-${Date.now()}`;
  await reauth(api);

  await page.goto("/");
  await openHeaderTab(page, "Remote Control");
  // A page of its own, opened from the sessions sidebar; not there until asked for.
  await expect(page.locator(".remote-control-setup")).toHaveCount(0);
  await page.getByRole("navigation", { name: "Remote control help" }).getByRole("button", { name: "Add a remote control" }).click();
  await expect(page).toHaveURL(/view=setup/);
  await expect(page.locator(".remote-control-setup")).toBeVisible();
  // Registering one to run by hand is the fallback, closed when a supervisor of this person's could run it instead.
  await expect(page.getByText("Loading your machines…")).toHaveCount(0);
  const byHand = page.locator("details.remote-control-setup__by-hand");
  if (!(await byHand.evaluate((d) => (d as HTMLDetailsElement).open))) await byHand.locator("summary").click();
  const manual = page.getByRole("form", { name: "Register a remote control to run yourself" });
  await manual.getByLabel("Remote control name").fill(name);
  const downloaded = page.waitForEvent("download");
  await manual.getByRole("button", { name: "Register remote control" }).click();
  const file = await downloaded;

  expect(file.suggestedFilename()).toBe(`slop-haus-setup-${name}.sh`);
  const issued = page.locator(".remote-control-setup__issued");
  await expect(issued).toContainText(`bash slop-haus-setup-${name}.sh`);
  await issued.getByRole("button", { name: "Done" }).click();
  await expect(issued).toHaveCount(0);

  expect((await listConductors(api)).conductors.find((c) => c.name === name)?.owned_by_me).toBe(true);
  // Its machine is on the page, offline until its host polls.
  await expect(page.getByRole("region", { name })).toContainText("Offline");

  // Managing it is in the sidebar, not on the setup page.
  const machine = page.getByRole("region", { name });
  await machine.getByRole("button", { name: `Manage ${name}` }).click();
  await machine.getByRole("button", { name: "Disable" }).click();
  await expect(machine).toContainText("Disabled");

  // It is not a worker: the Agents roster does not carry it.
  await openHeaderTab(page, "Workers");
  await expect(page.locator(".agents-panel__row", { hasText: name })).toHaveCount(0);
});

test("a conductor's token proposes without being a console, and is never handed work or the person's powers", async ({
  api,
  playwright,
}) => {
  const name = `conductor-token-${Date.now()}`;
  await reauth(api);
  const registered = await api.post("/api/conductors", { data: { name } });
  expect(registered.status(), await registered.text()).toBe(201);
  const conductor = (await listConductors(api)).conductors.find((c) => c.name === name)!;

  const { accessToken } = await credentialExistingAgentViaCodeFlow(api, conductor.id);
  const auth = { Authorization: `Bearer ${accessToken}` };
  const mcp = await McpSession.open(api, accessToken);

  const ticket = await (await api.post("/api/tickets", { data: { title: `conductor-${name}`, status: "preparing" } })).json();
  const proposed = await mcp.callTool("propose_ready", { ticket_id: ticket.id });
  expect(proposed.isError, proposed.text).toBe(false);
  expect(proposed.text).toContain("Nothing has changed yet");

  // A bare context: nothing but the conductor's own token, no session cookie,
  // so a refusal is the token's and not a missing login.
  const bare = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    // Work is for workers.
    await api.post("/api/tickets", { data: { title: `work-${name}` } });
    const claim = await bare.post("/api/claims", { headers: auth, data: {} });
    expect(claim.status(), await claim.text()).toBe(200);
    expect((await claim.json()).ticket ?? null).toBeNull();

    // Only a person acts: its token reaches none of the person's routes.
    const proposals = await (await api.get("/api/proposals?state=pending")).json();
    const mine = proposals.proposals.find((p: { ticket_id: number }) => p.ticket_id === ticket.id);
    expect(mine).toBeTruthy();
    for (const [method, path, data] of [
      ["POST", `/api/proposals/${mine.id}/approve`, {}],
      ["POST", "/api/conductors", { name: `${name}-child` }],
      ["PATCH", `/api/conductors/${conductor.id}`, { disabled: true }],
      ["POST", `/api/conductors/${conductor.id}/setup_script`, undefined],
    ] as const) {
      const resp = await bare.fetch(path, { method, headers: auth, data });
      expect(resp.status(), `${method} ${path}`).toBe(401);
    }
  } finally {
    await bare.dispose();
  }
  expect((await listConductors(api)).conductors.find((c) => c.id === conductor.id)?.disabled_at).toBeNull();
});

test("a worker's token cannot register a conductor", async ({ api, playwright }) => {
  const { accessToken } = await credentialAgentViaCodeFlow(api, `worker-${Date.now()}`);
  const bare = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    const resp = await bare.post("/api/conductors", {
      headers: { Authorization: `Bearer ${accessToken}` },
      data: { name: `sneaky-${Date.now()}` },
    });

    expect(resp.status(), await resp.text()).toBe(401);
  } finally {
    await bare.dispose();
  }
});
