import { expect, test } from "./support/fixtures";
import { API_BASE_URL } from "./support/env";
import { credentialAgentViaCodeFlow } from "./support/oauth";
import { openHeaderTab } from "./support/widgets";

// Harness config is set once in slop-haus and fetched by each workspace's
// run_remote_control.sh (GET /api/workspace/harness_config, with the agent's own token).
// These drive the board the way a person does and fetch the way run_remote_control.sh
// does.

interface OwnConfig {
  revision: number;
  settings: Record<string, Record<string, string>>;
}

test("set once on the board, every agent fetches it; an override is that agent's alone; an older run is marked stale", async ({
  page,
  api,
}) => {
  const stamp = Date.now();
  const builder = await credentialAgentViaCodeFlow(api, `builder-${stamp}`);
  const gpuBox = await credentialAgentViaCodeFlow(api, `gpu-box-${stamp}`);
  const fetchOwn = async (token: string) => {
    const resp = await api.get("/api/workspace/harness_config", { headers: { Authorization: `Bearer ${token}` } });
    return { status: resp.status(), body: resp.status() === 200 ? ((await resp.json()) as OwnConfig) : null };
  };

  // Nothing set yet: run_remote_control.sh gets a 204 and runs on run.env alone.
  expect((await fetchOwn(builder.accessToken)).status).toBe(204);

  // A run started before the change, reporting the revision it had (none).
  const startRun = (revision: string) =>
    api.post("/hooks/session-start", {
      headers: { Authorization: `Bearer ${builder.accessToken}` },
      data: {
        session_id: `harness-${revision}-${stamp}`,
        cwd: "/srv/ws/1",
        slop_haus: { harness: "claude", model: "claude-opus-5-5", config_revision: revision },
      },
    });
  expect((await startRun("0")).status()).toBe(200);

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  await page.getByRole("button", { name: "Edit defaults" }).click();
  const defaults = page.getByRole("form", { name: "Default harness config" });
  await defaults.getByLabel("Main model").fill("sonnet");
  await defaults.getByLabel("Subagent model").fill("sonnet");
  await defaults.getByRole("button", { name: "Save" }).click();
  await expect(page.locator(".agents-panel__config-summary")).toContainText(
    "Claude Code: Main model sonnet, Subagent model sonnet",
  );

  for (const agent of [builder, gpuBox]) {
    const own = await fetchOwn(agent.accessToken);
    expect(own.status).toBe(200);
    expect(own.body!.settings.claude).toEqual({ model: "sonnet", subagent_model: "sonnet" });
  }

  const builderRow = page.locator(".agents-panel__row", { hasText: `builder-${stamp}` });
  await expect(builderRow.getByText("stale config")).toBeVisible();

  // gpu-box's own override: only the main model, the rest from the defaults.
  const gpuRow = page.locator(".agents-panel__row", { hasText: `gpu-box-${stamp}` });
  await gpuRow.getByRole("button", { name: "Harness config" }).click();
  const override = gpuRow.getByRole("form", { name: `Harness config for gpu-box-${stamp}` });
  await expect(override.getByLabel("Main model")).toHaveAttribute("placeholder", "default (sonnet)");
  await override.getByLabel("Main model").fill("opus");
  await override.getByRole("button", { name: "Save" }).click();
  await expect(gpuRow.getByText("config override")).toBeVisible();

  const gpu = await fetchOwn(gpuBox.accessToken);
  expect(gpu.body!.settings.claude).toEqual({ model: "opus", subagent_model: "sonnet" });
  const built = await fetchOwn(builder.accessToken);
  expect(built.body!.settings.claude).toEqual({ model: "sonnet", subagent_model: "sonnet" });
  expect(built.body!.revision).toBeLessThan(gpu.body!.revision);

  // Restarted on the current revision: no longer stale.
  expect((await startRun(String(built.body!.revision))).status()).toBe(200);
  await page.reload();
  await openHeaderTab(page, "Workers");
  await expect(page.locator(".agents-panel__row", { hasText: `builder-${stamp}` })).toBeVisible();
  await expect(page.locator(".agents-panel__row", { hasText: `builder-${stamp}` }).getByText("stale config")).toHaveCount(0);
});

test("the server refuses a setting that is not in its registry or fails its pattern", async ({ api }) => {
  for (const settings of [
    { claude: { model: "--dangerously-skip-permissions" } },
    { claude: { ANTHROPIC_BASE_URL: "http://elsewhere" } },
    { claude: { effort: "ludicrous" } },
  ]) {
    const resp = await api.put("/api/harness_config", { data: { settings } });
    expect(resp.status(), await resp.text()).toBe(422);
  }
  const read = await api.get("/api/harness_config");
  expect(((await read.json()) as { defaults: unknown }).defaults).toBeNull();
});

// A fresh request context rather than `api`, which is always signed in as
// the board's dev user -- so a 401 proves the bearer token itself is
// refused, not that the request lacked a cookie.
test("an agent's token can neither read the tenant's config nor set any", async ({ api, playwright }) => {
  const { agentId, accessToken } = await credentialAgentViaCodeFlow(api, `config-climber-${Date.now()}`);
  const asAgent = { Authorization: `Bearer ${accessToken}` };
  const settings = { claude: { model: "sonnet" } };

  const anon = await playwright.request.newContext({ baseURL: API_BASE_URL });
  try {
    expect((await anon.get("/api/harness_config", { headers: asAgent })).status()).toBe(401);
    expect((await anon.put("/api/harness_config", { headers: asAgent, data: { settings } })).status()).toBe(401);
    expect(
      (await anon.put(`/api/agents/${agentId}/harness_config`, { headers: asAgent, data: { settings } })).status(),
    ).toBe(401);
    // The token is good: it reaches its own config, and nothing was saved.
    expect((await anon.get("/api/workspace/harness_config", { headers: asAgent })).status()).toBe(204);
  } finally {
    await anon.dispose();
  }
});
