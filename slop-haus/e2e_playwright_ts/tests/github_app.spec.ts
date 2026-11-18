import { createHmac, randomUUID } from "node:crypto";

import type { APIRequestContext } from "@playwright/test";

import { expect, test } from "./support/fixtures";
import { BOARD_ORIGIN, DEV_USER_PASSWORD, FAKE_GITHUB_URL, WEBHOOK_BASE_URL, WEBHOOK_PUBLIC_BASE_URL } from "./support/env";
import { connect, newPrivateKey, scriptGitHub } from "./support/github";

// A tenant's GitHub App credential through the real stack: stored encrypted,
// verified against the (fake) GitHub the api is pointed at, never read back.

// pemBody is a line of the key's base64: enough to look for it in an answer.
function pemBody(pem: string): string {
  return pem.split("\n")[1];
}

async function githubRequests(request: APIRequestContext) {
  const resp = await request.get(`${FAKE_GITHUB_URL}/_admin/requests`);
  return (await resp.json()) as { method: string; path: string; authorization: string; body: string }[];
}

// The manifest is the tenant's and the database cleanup does not reach it, so
// these tests start without one and leave without one: workspace.spec.ts
// expects none.
test.beforeEach(async ({ api }) => {
  await api.delete("/api/workspace/manifest");
});
test.afterEach(async ({ api }) => {
  await api.delete("/api/workspace/manifest");
  await api.delete("/api/codehost/github-app");
});

test("connect, verify against the manifest, and remove -- and the key never comes back", async ({ api, request }) => {
  const key = newPrivateKey();
  const stamp = Date.now();

  const connected = await connect(api, { app_id: 4242, installation_id: 777, private_key: key });
  expect(connected.status(), await connected.text()).toBe(200);
  const connectedText = await connected.text();
  expect(connectedText).not.toContain(pemBody(key));
  expect(JSON.parse(connectedText)).toMatchObject({ app_id: 4242, installation_id: 777, verified_at: null });

  const status = await api.get("/api/codehost/github-app");
  const statusText = await status.text();
  expect(statusText).not.toContain(pemBody(key));
  expect(JSON.parse(statusText)).toMatchObject({ enabled: true, can_edit: true });

  // The manifest names two repos; GitHub's installation sees one of them.
  const manifest = await api.put("/api/workspace/manifest", {
    data: { repos: [{ path: "app", remote: "github.com/acme/app" }, { path: "other", remote: `github.com/acme/missing-${stamp}` }] },
  });
  expect(manifest.ok(), await manifest.text()).toBeTruthy();
  await scriptGitHub(request, {
    installation: { body: { account: { login: "acme" } } },
    token: { status: 201, body: { token: "ghs_e2e_fake", expires_at: new Date(Date.now() + 3600_000).toISOString(), repositories: [] } },
    repos: { body: { repositories: [{ full_name: "acme/app" }] } },
  });

  const verified = await api.post("/api/codehost/github-app/verify");
  expect(verified.status(), await verified.text()).toBe(200);
  const result = await verified.json();
  expect(result.account).toBe("acme");
  expect(result.repos).toEqual(["acme/app"]);
  expect(result.manifest).toEqual([
    { remote: "github.com/acme/app", visible: true },
    { remote: `github.com/acme/missing-${stamp}`, visible: false },
  ]);
  expect(JSON.stringify(result)).not.toContain("ghs_e2e_fake");

  // What the api sent GitHub: an RS256 App JWT for this App, then a token
  // asking for no more than listing needs.
  const sent = await githubRequests(request);
  const mint = sent.find((r) => r.path === "/app/installations/777/access_tokens")!;
  expect(JSON.parse(mint.body)).toEqual({ permissions: { metadata: "read" } });
  const jwt = mint.authorization.replace("Bearer ", "");
  const [header, payload] = jwt.split(".").slice(0, 2).map((part) => JSON.parse(Buffer.from(part, "base64url").toString()));
  expect(header.alg).toBe("RS256");
  expect(payload.iss).toBe("4242");
  expect(payload.exp - payload.iat).toBeLessThanOrEqual(600);

  const removed = await api.delete("/api/codehost/github-app");
  expect(removed.status()).toBe(204);
  const after = await (await api.get("/api/codehost/github-app")).json();
  expect(after.credential).toBeNull();
});

test("a key that is not an RSA key is refused without being echoed", async ({ api }) => {
  const notAKey = `-----BEGIN RSA PRIVATE KEY-----\n${"QUJD".repeat(20)}\n-----END RSA PRIVATE KEY-----`;
  const resp = await connect(api, { app_id: 1, installation_id: 2, private_key: notAKey });
  expect(resp.status()).toBe(422);
  expect(await resp.text()).not.toContain("QUJD");

  const malformed = await api.put("/api/codehost/github-app", {
    data: `{"app_id": 1, "private_key": "${"QUJD".repeat(20)}`,
    headers: { "Content-Type": "application/json" },
  });
  expect(malformed.status()).toBeGreaterThanOrEqual(400);
  expect(await malformed.text()).not.toContain("QUJD");
});

test("a URL in the request cannot redirect where the api talks to GitHub", async ({ api }) => {
  const resp = await api.put("/api/codehost/github-app", {
    data: { app_id: 1, installation_id: 2, private_key: newPrivateKey(), api_base_url: "http://attacker.example" },
  });
  expect(resp.status()).toBe(422);
  const status = await (await api.get("/api/codehost/github-app")).json();
  expect(status.credential).toBeNull();
});

test("the board's Git tab connects, verifies and removes the App", async ({ page, api, request }) => {
  const key = newPrivateKey();
  await api.put("/api/workspace/manifest", { data: { repos: [{ path: "app", remote: "github.com/acme/app" }] } });
  await scriptGitHub(request, {
    installation: { body: { account: { login: "acme" } } },
    token: { status: 201, body: { token: "ghs_e2e_fake", expires_at: new Date(Date.now() + 3600_000).toISOString(), repositories: [] } },
    repos: { body: { repositories: [{ full_name: "acme/app" }] } },
  });

  await page.goto("/git");
  const section = page.getByRole("region", { name: "GitHub App" });
  await expect(section.getByText("No GitHub App is connected.")).toBeVisible();

  await section.getByText("Connect an App that already exists, by hand").click();
  await section.getByLabel("App id").fill("4242");
  await section.getByLabel("Installation id").fill("777");
  await section.getByLabel("Private key (PEM)").fill(key);
  await section.getByRole("button", { name: "Connect" }).click();

  // The step-up prompt if the session is old; the seeded session is new
  // enough, so normally it is just connected.
  const password = page.getByLabel("Password");
  if (await password.isVisible({ timeout: 2000 }).catch(() => false)) {
    await password.fill(DEV_USER_PASSWORD);
    await page.getByRole("button", { name: "Confirm" }).click();
  }
  await expect(section.getByText("4242", { exact: true })).toBeVisible();
  await expect(section.getByText("not yet")).toBeVisible();
  expect(await page.content()).not.toContain(pemBody(key));

  await section.getByRole("button", { name: "Verify" }).click();
  await expect(section.getByText("visible to the installation", { exact: true })).toBeVisible();
  await expect(section.getByText(/which can see 1 repository/)).toBeVisible();
  expect(await page.content()).not.toContain(pemBody(key));

  await section.getByRole("button", { name: "Remove" }).click();
  await section.getByRole("button", { name: "Confirm remove" }).click();
  await expect(section.getByText("No GitHub App is connected.")).toBeVisible();
});

// What the App must be created with: the union of what the server's
// operations ask a token for (permissions.go's appPermissions).
const APP_PERMISSIONS = { checks: "read", contents: "write", metadata: "read", pull_requests: "write", statuses: "read" };

function manifestPosted(sent: Awaited<ReturnType<typeof githubRequests>>, path: string) {
  const post = sent.find((r) => r.method === "POST" && r.path === path)!;
  expect(post, `the board posted a manifest to ${path}`).toBeDefined();
  return JSON.parse(new URLSearchParams(post.body).get("manifest")!);
}

function scriptInstalled(key: string, installPage: Record<string, unknown>) {
  return {
    conversion: {
      status: 201,
      body: { id: 4242, slug: "slop-haus-e2e", pem: key, client_secret: "client-secret-value", webhook_secret: "webhook-secret-value" },
    },
    installPage: { body: installPage },
    installation: { body: { id: 777, app_id: 4242, account: { login: "acme" } } },
    installations: { body: [] },
    token: { status: 201, body: { token: "ghs_e2e_fake", expires_at: new Date(Date.now() + 3600_000).toISOString(), repositories: [] } },
    repos: { body: { repositories: [{ full_name: "acme/app" }] } },
  };
}

test("Create, Install, then connected and verified, through GitHub's manifest flow", async ({ page, api, request }) => {
  const key = newPrivateKey();
  await api.put("/api/workspace/manifest", { data: { repos: [{ path: "app", remote: "github.com/acme/app" }] } });
  await scriptGitHub(request, scriptInstalled(key, { installation_id: 777, setup_action: "install" }));

  await page.goto("/git");
  const section = page.getByRole("region", { name: "GitHub App" });
  await expect(section.getByText("No GitHub App is connected.")).toBeVisible();
  await expect(section.getByText(/an owner of the organisation will be asked to approve/)).toBeVisible();

  await section.getByLabel(/GitHub organisation that should own/).fill("acme");
  await section.getByRole("button", { name: "Create GitHub App" }).click();

  // Through the fake GitHub's create page, back to the board, to the install
  // page, back again, and onto the panel -- connected and already verified.
  await expect(page).toHaveURL(/\/git$/);
  await expect(section.getByText("4242", { exact: true })).toBeVisible();
  await expect(section.getByText("777", { exact: true })).toBeVisible();
  await expect(section.getByText("not yet")).toHaveCount(0);
  await expect(section.getByText("not installed yet")).toHaveCount(0);
  expect(await page.content()).not.toContain(pemBody(key));

  await section.getByRole("button", { name: "Verify" }).click();
  await expect(section.getByText(/which can see 1 repository/)).toBeVisible();
  await expect(section.getByText("visible to the installation", { exact: true })).toBeVisible();

  const sent = await githubRequests(request);
  const manifest = manifestPosted(sent, "/organizations/acme/settings/apps/new");
  expect(manifest.default_permissions).toEqual(APP_PERMISSIONS);
  expect(manifest.public).toBe(false);
  // The App delivers the review events to this environment's App door.
  expect(manifest.hook_attributes).toEqual({ url: `${WEBHOOK_PUBLIC_BASE_URL}/hooks/github-app`, active: true });
  expect(manifest.default_events).toEqual(["pull_request", "pull_request_review"]);
  // The board's own pages and nothing else (the origin may spell out its port).
  expect(new URL(manifest.redirect_url).origin).toBe(BOARD_ORIGIN);
  expect(new URL(manifest.redirect_url).pathname).toBe("/workspace/github-app/created");
  expect(new URL(manifest.setup_url).origin).toBe(BOARD_ORIGIN);
  expect(new URL(manifest.setup_url).pathname).toBe("/workspace/github-app/installed");

  // The code is exchanged server to GitHub with no credential, and the
  // installation the browser named is confirmed with the App's own JWT.
  const conversion = sent.find((r) => r.path.endsWith("/conversions"))!;
  expect(conversion.authorization).toBe("");
  const confirm = sent.find((r) => r.method === "GET" && r.path === "/app/installations/777")!;
  const payload = JSON.parse(Buffer.from(confirm.authorization.replace("Bearer ", "").split(".")[1], "base64url").toString());
  expect(payload.iss).toBe("4242");

  const status = await (await api.get("/api/codehost/github-app")).text();
  expect(status).not.toContain(pemBody(key));
  expect(JSON.parse(status).credential).toMatchObject({ app_id: 4242, installation_id: 777, pending: false });
});

test("an install an org owner has yet to approve waits, and Verify picks it up afterwards", async ({ page, api, request }) => {
  const key = newPrivateKey();
  await scriptGitHub(request, scriptInstalled(key, { setup_action: "request" }));

  await page.goto("/git");
  const section = page.getByRole("region", { name: "GitHub App" });
  await section.getByRole("button", { name: "Create GitHub App" }).click();

  await expect(page.getByText(/Waiting for an org owner to approve/)).toBeVisible();
  await page.getByRole("button", { name: "Back to Git" }).click();
  await expect(section.getByText("not installed yet")).toBeVisible();
  const personal = manifestPosted(await githubRequests(request), "/settings/apps/new");
  expect(personal.default_permissions).toEqual(APP_PERMISSIONS);

  // Not approved yet: Verify says so and the credential stays pending.
  await section.getByRole("button", { name: "Verify" }).click();
  await expect(section.getByRole("alert")).toContainText("not installed yet");

  // The owner approved it: GitHub now lists the installation.
  await scriptGitHub(request, {
    ...scriptInstalled(key, {}),
    installations: { body: [{ id: 777, app_id: 4242, account: { login: "acme" } }] },
  });
  await section.getByRole("button", { name: "Verify" }).click();
  await expect(section.getByText(/which can see 1 repository/)).toBeVisible();
  await expect(section.getByText("not installed yet")).toHaveCount(0);
  const status = await (await api.get("/api/codehost/github-app")).json();
  expect(status.credential).toMatchObject({ installation_id: 777, pending: false });
});

test("an installation id of another App from the browser is refused", async ({ page, api, request }) => {
  const key = newPrivateKey();
  await scriptGitHub(request, {
    ...scriptInstalled(key, { installation_id: 999, setup_action: "install" }),
    installation: { status: 404, body: { message: "Not Found" } },
  });

  await page.goto("/git");
  await page.getByRole("region", { name: "GitHub App" }).getByRole("button", { name: "Create GitHub App" }).click();

  // The click leads through the manifest request, a form post to the scripted
  // GitHub, a redirect back and the server's install check before the alert is
  // rendered, so the default 5s is too tight on a loaded CI runner.
  await expect(page.getByRole("alert")).toContainText("does not belong to this tenant's GitHub App", { timeout: 30_000 });
  const status = await (await api.get("/api/codehost/github-app")).json();
  expect(status.credential).toMatchObject({ installation_id: 0, pending: true });
});

// The App's own webhook: set when the App is connected, "not done" until a
// pull request event actually arrives through it, then receiving.
test("the App webhook is set on connect and shows receiving once a signed event arrives", async ({ page, api, request }) => {
  const key = newPrivateKey();
  await scriptGitHub(request, { hookConfig: { body: { url: "", content_type: "json" } }, hookPatch: { body: {} } });
  const connected = await connect(api, { app_id: 4242, installation_id: 777, private_key: key });
  expect(connected.status(), await connected.text()).toBe(200);

  const patch = (await githubRequests(request)).find((r) => r.method === "PATCH" && r.path === "/app/hook/config")!;
  expect(patch, "the webhook was set on GitHub").toBeDefined();
  const sent = JSON.parse(patch.body);
  expect(sent.url).toBe(`${WEBHOOK_PUBLIC_BASE_URL}/hooks/github-app`);

  const before = await (await api.get("/api/codehost/github-app/webhook")).json();
  expect(before).toMatchObject({ state: "awaiting_events", can_edit: true, last_event: null });
  expect(JSON.stringify(before)).not.toContain(sent.secret);

  await page.goto("/git");
  const section = page.getByRole("region", { name: "GitHub App webhook" });
  await expect(section.getByText(/^Not done\. The App's webhook is set/)).toBeVisible();
  await expect(section.getByText("Pull request review")).toBeVisible();

  // GitHub signs with the secret slop-haus gave it, and names the installation.
  const body = JSON.stringify({
    action: "opened",
    installation: { id: 777 },
    repository: { full_name: "acme/widgets" },
    pull_request: { html_url: "https://github.com/acme/widgets/pull/12" },
  });
  const delivered = await request.post(`${WEBHOOK_BASE_URL}/hooks/github-app`, {
    data: body,
    headers: {
      "Content-Type": "application/json",
      "X-GitHub-Event": "pull_request",
      "X-GitHub-Delivery": randomUUID(),
      "X-Hub-Signature-256": "sha256=" + createHmac("sha256", sent.secret).update(body).digest("hex"),
    },
  });
  expect(delivered.status(), await delivered.text()).toBe(200);

  await page.reload();
  await expect(section.getByText(/^Receiving\./)).toBeVisible();
  expect((await (await api.get("/api/codehost/github-app/webhook")).json()).state).toBe("receiving");

  // Signed with anything else, it is refused and changes nothing.
  const forged = await request.post(`${WEBHOOK_BASE_URL}/hooks/github-app`, {
    data: body,
    headers: {
      "Content-Type": "application/json",
      "X-GitHub-Event": "pull_request",
      "X-GitHub-Delivery": randomUUID(),
      "X-Hub-Signature-256": "sha256=" + createHmac("sha256", "not-the-secret").update(body).digest("hex"),
    },
  });
  expect(forged.status()).toBe(401);
});
