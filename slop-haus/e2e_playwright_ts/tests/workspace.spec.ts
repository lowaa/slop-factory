import type { APIRequestContext } from "@playwright/test";
import { expect, test } from "./support/fixtures";
import { credentialAgentViaCodeFlow } from "./support/oauth";
import { headerTab, openHeaderTab } from "./support/widgets";

const HELP_SUMMARY = "How workspaces work";

test("the Workspace tab sits directly right of Workers", async ({ page }) => {
  await page.goto("/");
  // goto resolves on the load event, which no longer waits for the page code
  // (it is split into chunks): wait for the header before reading it.
  await expect(headerTab(page, "Workspace")).toBeVisible();

  const tabs = await page.getByRole("navigation").getByRole("link").allTextContents();
  const workspace = tabs.indexOf("Workspace");
  expect(workspace, tabs.join(", ")).toBeGreaterThanOrEqual(0);
  expect(tabs[workspace - 1]).toBe("Workers");

  await openHeaderTab(page, "Workspace");
  expect(new URL(page.url()).pathname).toBe("/workspace");
  await expect(headerTab(page, "Workspace")).toHaveAttribute("aria-current", "page");
});

test("the help section is closed until opened, and closes again", async ({ page }) => {
  await page.goto("/workspace");

  const help = page.locator("details.workspace-help");
  await expect(help).toBeVisible();
  await expect(help).not.toHaveAttribute("open", "");
  await expect(page.getByText("One root, the same for every agent")).toBeHidden();

  await page.getByText(HELP_SUMMARY, { exact: true }).click();
  await expect(help).toHaveAttribute("open", "");
  await expect(page.getByText("One root, the same for every agent")).toBeVisible();

  await page.getByText(HELP_SUMMARY, { exact: true }).click();
  await expect(help).not.toHaveAttribute("open", "");
});

// What reaches the page is what a real workspace's hook sends: the layout
// rides in the payload's slop_haus object. An agent that never sends one is
// unknown, and is not held back from the hooks for it.
test("shows the layout an agent reported, and unknown for one that has not", async ({ page, api }) => {
  const stamp = Date.now();
  const reporter = await credentialAgentViaCodeFlow(api, `reporter-${stamp}`);
  const silent = await credentialAgentViaCodeFlow(api, `silent-${stamp}`);

  const reported = await api.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${reporter.accessToken}` },
    data: {
      session_id: `ws-${stamp}`,
      cwd: "/srv/ws/1",
      slop_haus: {
        layout: {
          root: { path: ".", git: false, origin: "" },
          repos: [{ path: "app", git: true, origin: "https://user:secret-token@github.com/acme/app" }],
        },
      },
    },
  });
  expect(reported.status(), await reported.text()).toBe(200);

  const quiet = await api.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${silent.accessToken}` },
    data: { session_id: `ws-quiet-${stamp}`, cwd: "/srv/ws/2" },
  });
  expect(quiet.status(), await quiet.text()).toBe(200);

  await page.goto("/workspace");

  const row = page.locator(".workspace-panel__row", { hasText: `reporter-${stamp}` });
  await expect(row).toContainText("reported");
  await expect(row).toContainText("not a git repo");
  await expect(row).toContainText("github.com/acme/app");
  await expect(row).not.toContainText("secret-token");

  const unknown = page.locator(".workspace-panel__row", { hasText: `silent-${stamp}` });
  await expect(unknown).toContainText("unknown");
  await expect(unknown).toContainText("Restart the agent");
});

// The manifest is the tenant's, so these tests share it with every other
// test in the file: each one leaves it undeclared.
test.describe("the manifest", () => {
  test.describe.configure({ mode: "serial" });

  test.afterEach(async ({ api }) => {
    const cleared = await api.delete("/api/workspace/manifest");
    expect(cleared.status(), await cleared.text()).toBe(204);
  });

  async function reportLayout(api: APIRequestContext, name: string, origin: string, sessionId: string) {
    const agent = await credentialAgentViaCodeFlow(api, name);
    const resp = await api.post("/hooks/session-start", {
      headers: { Authorization: `Bearer ${agent.accessToken}` },
      data: {
        session_id: sessionId,
        cwd: "/srv/ws",
        slop_haus: {
          layout: {
            root: { path: ".", git: false, origin: "" },
            repos: [{ path: "app", git: true, origin }],
          },
        },
      },
    });
    expect(resp.status(), await resp.text()).toBe(200);
  }

  test("an owner adopts the proposal, edits it, and removes it", async ({ page, api }) => {
    const stamp = Date.now();
    const remote = `github.com/e2e-${stamp}/app`;
    for (const n of [1, 2, 3]) {
      await reportLayout(api, `agree-${n}-${stamp}`, `https://${remote}.git`, `ws-agree-${n}-${stamp}`);
    }
    await reportLayout(api, `odd-${stamp}`, `https://github.com/e2e-${stamp}/other`, `ws-odd-${stamp}`);

    await page.goto("/workspace");
    const manifest = page.getByRole("region", { name: "Manifest" });
    await expect(manifest).toContainText("nothing about the workspace layout is enforced");
    await expect(manifest).toContainText(remote);
    await expect(manifest).toContainText(`odd-${stamp} found app → github.com/e2e-${stamp}/other`);

    const row = (name: string) => page.locator(".workspace-panel__row", { hasText: `${name}-${stamp}` });
    await expect(row("agree-1")).toContainText("reported");

    await manifest.getByRole("button", { name: "Adopt this manifest" }).click();
    await expect(manifest.getByRole("button", { name: "Edit" })).toBeVisible();
    await expect(manifest).toContainText(remote);
    await expect(row("agree-1")).toContainText("matches");
    await expect(row("odd")).toContainText("mismatch");
    await expect(row("odd")).toContainText(`origin is github.com/e2e-${stamp}/other`);

    // Adding a repo nobody has checked out turns every agent into a mismatch.
    await manifest.getByRole("button", { name: "Edit" }).click();
    await manifest.getByRole("button", { name: "Add repo" }).click();
    await manifest.getByLabel("Path 2").fill("deploy");
    await manifest.getByLabel("Remote 2").fill(`github.com/e2e-${stamp}/deploy`);
    await manifest.getByRole("button", { name: "Save" }).click();
    await expect(manifest.getByRole("button", { name: "Edit" })).toBeVisible();
    await expect(manifest).toContainText(`github.com/e2e-${stamp}/deploy`);
    await expect(row("agree-1")).toContainText("mismatch");
    await expect(row("agree-1")).toContainText("no git repository found there");

    await page.reload();
    await expect(page.getByRole("region", { name: "Manifest" })).toContainText(`github.com/e2e-${stamp}/deploy`);

    await manifest.getByRole("button", { name: "Remove manifest" }).click();
    await manifest.getByRole("button", { name: "Remove it" }).click();
    await expect(manifest).toContainText("nothing about the workspace layout is enforced");
    await expect(row("agree-1")).toContainText("reported");
  });

  test("the server's refusal of an invalid manifest is shown", async ({ page, api }) => {
    const stamp = Date.now();
    const remote = `github.com/e2e-${stamp}/app`;
    await reportLayout(api, `solo-${stamp}`, `https://${remote}`, `ws-solo-${stamp}`);
    const set = await api.put("/api/workspace/manifest", { data: { repos: [{ path: "app", remote }] } });
    expect(set.status(), await set.text()).toBe(200);

    await page.goto("/workspace");
    const manifest = page.getByRole("region", { name: "Manifest" });
    await manifest.getByRole("button", { name: "Edit" }).click();
    await manifest.getByLabel("Path 1").fill("a/b");
    await manifest.getByRole("button", { name: "Save" }).click();

    await expect(manifest.getByRole("alert")).toBeVisible();
    await expect(manifest.getByLabel("Path 1")).toHaveValue("a/b");
  });
});
