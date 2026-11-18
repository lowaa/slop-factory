import { execFileSync } from "node:child_process";

import type { Page } from "@playwright/test";
import { expect, test } from "./support/fixtures";
import { credentialAgentViaCodeFlow, credentialAgentViaDeviceFlow } from "./support/oauth";
import { openHeaderTab } from "./support/widgets";

const SETUP_HELP_SUMMARY = "Add a slot manually";

// The Agents screen is the one the human lives on: it answers "which agent
// has gone quiet" without opening four terminals.

test("shows what an agent is holding", async ({ page, api }) => {
  const name = `holder-ui-${Date.now()}`;
  const { accessToken } = await credentialAgentViaCodeFlow(api, name);

  const ticketResp = await api.post("/api/tickets", {
    data: { title: "Held by the UI test", paths: [{ path: `server/ui-${Date.now()}/` }] },
  });
  const ticket = await ticketResp.json();

  await api.post("/api/claims", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { ticket_id: ticket.id },
  });

  await page.goto("/");
  await openHeaderTab(page, "Workers");

  const row = page.locator(".agents-panel__row", { hasText: name });
  await expect(row).toContainText(`#${ticket.id}`);
  await expect(row).toContainText(/expires in/i);
});

// The loop breaker lets an agent that keeps being sent back with nothing to
// show for it stop, and says so where the human looks: on the agent's row.
test("an agent the loop breaker let stop shows as stuck, with why", async ({ page, api }) => {
  const name = `stuck-ui-${Date.now()}`;
  const { accessToken } = await credentialAgentViaCodeFlow(api, name);
  const auth = { Authorization: `Bearer ${accessToken}` };
  const session = { session_id: `claude-${Date.now()}`, cwd: "/srv/ws/1" };

  await api.post("/hooks/session-start", { headers: auth, data: session });
  const ticket = await (await api.post("/api/tickets", { data: { title: "Never finished", paths: [{ path: `server/stuck-${Date.now()}/` }] } })).json();
  const claimed = await api.post("/api/claims", { headers: auth, data: { ticket_id: ticket.id } });
  expect(claimed.status(), await claimed.text()).toBe(200);

  // Held and never worked: each stop is blocked, and the third trips the breaker.
  for (let i = 0; i < 2; i++) {
    const blocked = await api.post("/hooks/stop", { headers: auth, data: session });
    expect((await blocked.json()).hookSpecificOutput.decision).toBe("block");
  }
  const released = await api.post("/hooks/stop", { headers: auth, data: session });
  expect((await released.json()).hookSpecificOutput ?? null).toBeNull();

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  const row = page.locator(".agents-panel__row", { hasText: name });
  await expect(row).toContainText("Stuck since");
  await expect(row).toContainText(`Stuck on #${ticket.id}`);
});

// The Agents panel also lists every OAuth grant, grouped under the agent
// it acts as, with a Revoke button -- the replacement for the old
// "here is your token" reveal.
test("lists an authorization and revokes it, which kills the access token", async ({ page, api, playwright }) => {
  // Pipeline 11555: ~45s went on a stalled runner before the device flow even
  // started, then the claim below was cut off by the 60s timeout (the api saw
  // "commit: context canceled", a 500). Every step here is a round trip to
  // the same stack, so the one test with the longest chain gets the budget.
  test.slow();
  const name = `revocable-ui-${Date.now()}`;
  const { accessToken } = await credentialAgentViaDeviceFlow(playwright, api, name);

  // Proven live before revoking, so the later 401 is really the revoke's
  // doing and not some other reason the token never worked.
  const before = await api.post("/api/claims", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: {},
  });
  expect(before.status(), await before.text()).toBe(200);

  await page.goto("/");
  await openHeaderTab(page, "Workers");

  const row = page.locator(".agents-panel__row", { hasText: name });
  const grant = row.locator(".agent-authorizations__item");
  await expect(grant).toContainText("slop-haus workspace");
  await grant.getByRole("button", { name: "Revoke" }).click();
  await expect(grant).toHaveCount(0);

  const after = await api.post("/api/claims", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: {},
  });
  expect(after.status(), await after.text()).toBe(401);
});

// Disabling is the agent's own switch, distinct from revoking one client's
// grant above: agent.Service.Authenticate (server/internal/agent/
// service.go) refuses *every* token for a disabled agent, whichever client
// it was issued to.
test("disabling an agent in the roster kills its access token", async ({ page, api }) => {
  const name = `disable-ui-${Date.now()}`;
  const { accessToken } = await credentialAgentViaCodeFlow(api, name);

  const before = await api.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { session_id: `before-disable-${Date.now()}`, cwd: "/srv/ws/1" },
  });
  expect(before.status(), await before.text()).toBe(200);

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  const row = page.locator(".agents-panel__row", { hasText: name });
  await row.getByRole("button", { name: "Disable" }).click();
  await expect(row.getByRole("button", { name: "Enable" })).toBeVisible();
  await expect(row).toContainText(/disabled/i);

  const after = await api.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { session_id: `after-disable-${Date.now()}`, cwd: "/srv/ws/1" },
  });
  expect(after.status(), await after.text()).toBe(401);
});

// A new setup script for an existing agent: the script carries no
// credential, so the rotation is signing out every workspace acting as the
// agent -- its old token stops working the moment the download is
// confirmed, and the only way back is the new script's device sign-in.
test("a new setup script for an existing agent signs its workspaces out", async ({ page, api }) => {
  const name = `rotate-ui-${Date.now()}`;
  const { accessToken } = await credentialAgentViaCodeFlow(api, name);
  const signedIn = () =>
    api.post("/hooks/session-start", {
      headers: { Authorization: `Bearer ${accessToken}` },
      data: { session_id: `rotate-${Date.now()}`, cwd: "/srv/ws/1" },
    });

  // Proven live first, so the later 401 is the rotation's doing.
  const before = await signedIn();
  expect(before.status(), await before.text()).toBe(200);

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  const row = page.locator(".agents-panel__row", { hasText: name });
  await row.getByRole("button", { name: "New setup script" }).click();

  // Nothing is signed out until the human confirms.
  await expect(row.getByRole("group", { name: `Confirm new setup script for ${name}` })).toBeVisible();
  const unconfirmed = await signedIn();
  expect(unconfirmed.status(), await unconfirmed.text()).toBe(200);

  const downloaded = page.waitForEvent("download");
  await row.getByRole("button", { name: "Sign out and download" }).click();
  const file = await downloaded;

  expect(file.suggestedFilename()).toBe(`slop-haus-setup-${name}.sh`);
  const script = (await (await file.createReadStream()).toArray()).join("");
  expect(script).toContain("Signing in as agent");
  await expect(row.getByRole("status")).toContainText(`slop-haus-setup-${name}.sh`);

  const after = await signedIn();
  expect(after.status(), await after.text()).toBe(401);
  // The same agent, not a second one registered alongside it.
  await expect(page.locator(".agents-panel__row", { hasText: name })).toHaveCount(1);
  await expect(page.locator(".agents-panel__row", { hasText: name }).locator(".agent-authorizations__item")).toHaveCount(0);
});

test("hide disabled drops disabled agents, which offer no new setup script", async ({ page, api }) => {
  const stamp = Date.now();
  const on = `hide-on-${stamp}`;
  const off = `hide-off-${stamp}`;
  await api.post("/api/agents", { data: { name: on } });
  const created = await (await api.post("/api/agents", { data: { name: off } })).json();
  const disabled = await api.patch(`/api/agents/${created.id}`, { data: { disabled: true } });
  expect(disabled.status(), await disabled.text()).toBe(200);

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  const offRow = page.locator(".agents-panel__row", { hasText: off });
  const onRow = page.locator(".agents-panel__row", { hasText: on });
  await expect(offRow).toContainText(/disabled/i);
  await expect(offRow.getByRole("button", { name: "New setup script" })).toHaveCount(0);

  await page.getByRole("checkbox", { name: "Hide disabled" }).check();
  await expect(offRow).toHaveCount(0);
  await expect(onRow).toBeVisible();

  await page.getByRole("checkbox", { name: "Hide disabled" }).uncheck();
  await expect(offRow).toBeVisible();
});

// Signing in is now the gate that used to be the admin token: the fleet
// view (and everything else) needs a session, full stop. There is no more
// "read the roster, but can't change it" middle tier -- an anonymous
// visitor gets sent to /login before ever seeing a roster to read.
test("the fleet is not reachable at all without signing in", async ({ anonPage, api }) => {
  const name = `readonly-${Date.now()}`;
  await api.post("/api/agents", { data: { name } });

  await anonPage.goto("/");
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();
});

// The hand-run route sits at the bottom of Workers -> Add a machine.
async function openManualSlot(page: Page) {
  await openHeaderTab(page, "Workers");
  await page.getByRole("link", { name: "Add a machine" }).click();
  await page.getByText(SETUP_HELP_SUMMARY, { exact: true }).click();
}

// The setup script is the whole onboarding path for someone with a repo
// and no slop-haus checkout: name an agent, get a file, run it there.
test("hands over a runnable setup script as a download", async ({ page }) => {
  const name = `script-ui-${Date.now()}`;

  await page.goto("/");
  await openManualSlot(page);

  // This is the only way onto the roster from here: the old name-only
  // "Create agent" form was a lesser duplicate of it.
  await expect(page.getByRole("button", { name: "Create agent" })).toHaveCount(0);

  await page.getByLabel("Worker name").fill(name);

  const downloaded = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download setup script" }).click();
  const file = await downloaded;

  expect(file.suggestedFilename()).toBe(`slop-haus-setup-${name}.sh`);

  const stream = await file.createReadStream();
  const script = (await stream.toArray()).join("");

  // No static credential any more (server/internal/workspace/script.go):
  // it knows where to find us, and signs itself in with a device code
  // instead of carrying a bearer token.
  expect(script).not.toContain("SLOP_HAUS_TOKEN=");
  expect(script).toContain("/mcp");
  expect(script).toContain("@AGENTS.md");
  expect(script).toContain("Signing in as agent");
  // One sign-in covers MCP too: .mcp.json's headersHelper asks token.sh.
  expect(script).toContain("headersHelper");
  expect(script).not.toContain("claude mcp login");

  // And the agent it minted is on the roster, holding nothing yet.
  await openHeaderTab(page, "Workers");
  const row = page.locator(".agents-panel__row").filter({ hasText: name });
  await expect(row).toBeVisible();
  await expect(row).toContainText(/idle/i);
});

test("does not render the script it downloads", async ({ page }) => {
  const name = `screenshot-safe-${Date.now()}`;

  await page.goto("/");
  await openManualSlot(page);
  await page.getByLabel("Worker name").fill(name);

  const downloaded = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download setup script" }).click();
  const file = await downloaded;
  const stream = await file.createReadStream();
  const script = (await stream.toArray()).join("");

  // The script carries no credential any more, but it is still not meant
  // to land on screen -- an agent name and this server's address are still
  // nobody else's business to read over a shoulder. "set -euo pipefail" is
  // a line straight out of the script body (server/internal/workspace/
  // script.go), not anything the UI itself would ever render, so its
  // absence from the page proves the raw script text never made it there.
  expect(script).toContain("set -euo pipefail");
  await expect(page.locator("body")).not.toContainText("set -euo pipefail");
});

// A command offered for pasting has to parse as one. This caught an
// apostrophe inside a single-quoted placeholder, which closed the quote
// and left the line unbalanced while every substring assertion about it
// still passed.
//
// `bash -n` parses without running: nothing here is executed.
test("every command offered for copying parses as shell", async ({ page }) => {
  await page.goto("/");
  await openManualSlot(page);

  for (const route of ["Into a repo you already have", "Start a fresh workspace"]) {
    await page.getByRole("tab", { name: route }).click();

    const commands = await page.locator(".command pre").allTextContents();
    expect(commands.length, `${route} should offer commands`).toBeGreaterThan(0);

    for (const command of commands) {
      expect(() => execFileSync("bash", ["-n"], { input: command }), command).not.toThrow();
    }
  }
});
