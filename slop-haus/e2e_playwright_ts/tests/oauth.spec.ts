// End-to-end coverage of server/internal/oauth: the two ways a client gets
// credentialed to act as an agent (the authorization code grant a real MCP
// client like Claude Code uses, and the device grant a headless workspace
// uses), discovery, revocation and refresh-token rotation -- all driven
// through the frontend origin and its real nginx, the same way a real
// client would reach this server. The unit suite (server/internal/oauth/
// *_test.go) covers the algorithm; this proves the wiring -- the SPA's
// consent/device pages, and nginx's /oauth/ and /.well-known/oauth- proxy
// blocks -- actually holds together.
import { expect, test } from "./support/fixtures";
import { DEV_USER_EMAIL, DEV_USER_PASSWORD } from "./support/env";
import { fillLoginForm, newAccount, registerAndActivate } from "./support/auth_flow";
import { openHeaderTab } from "./support/widgets";
import {
  buildAuthorizeUrl,
  credentialAgentViaDeviceFlow,
  newPkcePair,
  registerClient,
  unusedLoopbackPort,
} from "./support/oauth";

test("MCP-style authorization code flow through the browser and nginx", async ({ page, frontendApi }) => {
  const client = await registerClient(frontendApi, "Test MCP Client", unusedLoopbackPort());
  const { verifier, challenge } = newPkcePair();
  const state = `state-${Date.now()}`;
  const authorizeUrl = buildAuthorizeUrl("", client, challenge, state);

  // The browser is about to be told to navigate to client.redirectUri --
  // nothing listens there on purpose, so this intercepts that navigation
  // rather than requiring it to load (it never could). Capturing the
  // query string is the whole point: that is where the authorization code
  // and state land.
  let captured: URLSearchParams | null = null;
  await page.route(`${client.redirectUri}**`, async (route) => {
    captured = new URL(route.request().url()).searchParams;
    await route.abort();
  });

  // A real navigation, through nginx: GET /oauth/authorize on the api's
  // mux redirects to this same origin's /authorize?request=<id>, which is
  // the SPA's consent page -- `page` is already signed in (see
  // support/fixtures.ts), so it renders straight away.
  await page.goto(authorizeUrl);
  await expect(page.getByText(/wants to act as one of your agents/)).toBeVisible();
  const redirectHost = client.redirectUri.replace(/^https?:\/\//, "").replace(/\/callback$/, "");
  await expect(page.getByText(redirectHost).first()).toBeVisible();

  const agentName = `mcp-code-flow-${Date.now()}`;
  await page.getByLabel("New agent name").fill(agentName);
  await page.getByRole("button", { name: "Approve" }).click();

  await expect.poll(() => captured, { message: "the browser should have been sent to the client's redirect" }).not.toBeNull();
  const code = captured!.get("code");
  expect(code, "the redirect should carry an authorization code").toBeTruthy();
  expect(captured!.get("state")).toBe(state);

  const tokenResp = await frontendApi.post("/oauth/token", {
    form: {
      grant_type: "authorization_code",
      code: code!,
      redirect_uri: client.redirectUri,
      client_id: client.clientId,
      code_verifier: verifier,
    },
  });
  expect(tokenResp.status(), await tokenResp.text()).toBe(200);
  const tokens = await tokenResp.json();
  expect(tokens.access_token).toBeTruthy();

  // The minted access token actually works against an agent route, through
  // the same frontend origin.
  const check = await frontendApi.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${tokens.access_token}` },
    data: { session_id: `mcp-code-flow-${Date.now()}`, cwd: "/srv/ws/1" },
  });
  expect(check.status(), await check.text()).toBe(200);
});

test("GET /mcp without a token discovers protected-resource metadata, reachable through the frontend origin", async ({
  frontendApi,
}) => {
  const resp = await frontendApi.get("/mcp");
  expect(resp.status()).toBe(401);

  const challenge = resp.headers()["www-authenticate"] ?? "";
  const match = /resource_metadata="([^"]+)"/.exec(challenge);
  expect(match, `expected a resource_metadata challenge, got: ${challenge}`).toBeTruthy();
  expect(match![1]).toContain("/.well-known/oauth-protected-resource/mcp");

  // Both well-known documents, through the frontend origin -- proving
  // nginx's /.well-known/oauth- proxy, not just the header's claim.
  const resource = await frontendApi.get("/.well-known/oauth-protected-resource/mcp");
  expect(resource.status(), await resource.text()).toBe(200);
  const resourceBody = await resource.json();
  expect(resourceBody.resource).toContain("/mcp");
  expect(resourceBody.authorization_servers.length).toBeGreaterThan(0);

  const authServer = await frontendApi.get("/.well-known/oauth-authorization-server");
  expect(authServer.status(), await authServer.text()).toBe(200);
  const authServerBody = await authServer.json();
  expect(authServerBody.authorization_endpoint).toContain("/oauth/authorize");
  expect(authServerBody.token_endpoint).toContain("/oauth/token");
  expect(authServerBody.device_authorization_endpoint).toContain("/oauth/device_authorization");
});

test("device flow through the UI: approve on /device, then poll for tokens", async ({ page, frontendApi }) => {
  const startResp = await frontendApi.post("/oauth/device_authorization", {
    form: { client_id: "slop-haus-workspace" },
  });
  expect(startResp.status(), await startResp.text()).toBe(200);
  const start = await startResp.json();

  // token.sh's cmd_login appends "&agent=<name>" when it already knows its
  // own name (see internal/workspace/assets/sh/token.sh); the device page preselects
  // by it, or -- as here, since this agent doesn't exist yet -- prefills
  // it as the new agent's name.
  const agentName = `device-ui-${Date.now()}`;
  await page.goto(`/device?code=${encodeURIComponent(start.user_code)}&agent=${agentName}`);
  await expect(page.getByText(/wants to sign in as one of your agents/)).toBeVisible();
  await expect(page.getByLabel("New agent name")).toHaveValue(agentName);

  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText(/Done — return to your terminal/)).toBeVisible();

  const tokenResp = await frontendApi.post("/oauth/token", {
    form: {
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
      device_code: start.device_code,
      client_id: "slop-haus-workspace",
    },
  });
  expect(tokenResp.status(), await tokenResp.text()).toBe(200);
  const tokens = await tokenResp.json();
  expect(tokens.access_token).toBeTruthy();
  expect(tokens.refresh_token).toBeTruthy();
});

test("revoking a grant from the Agents panel kills its access token", async ({ page, api, playwright }) => {
  const name = `oauth-revoke-${Date.now()}`;
  const { accessToken } = await credentialAgentViaDeviceFlow(playwright, api, name);

  const before = await api.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { session_id: `before-revoke-${Date.now()}`, cwd: "/srv/ws/1" },
  });
  expect(before.status(), await before.text()).toBe(200);

  await page.goto("/");
  await openHeaderTab(page, "Workers");
  const row = page.locator(".agents-panel__row", { hasText: name });
  const grant = row.locator(".agent-authorizations__item");
  await expect(grant).toContainText("slop-haus workspace");
  await grant.getByRole("button", { name: "Revoke" }).click();
  await expect(grant).toHaveCount(0);

  const after = await api.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${accessToken}` },
    data: { session_id: `after-revoke-${Date.now()}`, cwd: "/srv/ws/1" },
  });
  expect(after.status(), await after.text()).toBe(401);
});

// RFC 8628 section 5's reuse detection: a refresh token presented a second
// time is theft as far as the server can tell, since a legitimate holder
// would have gotten the rotated one back and never presented the old one
// again -- so it revokes the whole grant, not just the one bad request.
// The token replayed here is two rotations old, outside the short reuse
// grace window that only covers the most recent one.
test("refresh rotates once; replaying an older refresh token kills the whole grant", async ({ api, playwright, frontendApi }) => {
  const { refreshToken } = await credentialAgentViaDeviceFlow(playwright, api, `refresh-rotate-${Date.now()}`);

  const first = await frontendApi.post("/oauth/token", {
    form: { grant_type: "refresh_token", refresh_token: refreshToken, client_id: "slop-haus-workspace" },
  });
  expect(first.status(), await first.text()).toBe(200);
  const firstTokens = await first.json();
  expect(firstTokens.refresh_token).not.toBe(refreshToken);

  const second = await frontendApi.post("/oauth/token", {
    form: { grant_type: "refresh_token", refresh_token: firstTokens.refresh_token, client_id: "slop-haus-workspace" },
  });
  expect(second.status(), await second.text()).toBe(200);

  const replay = await frontendApi.post("/oauth/token", {
    form: { grant_type: "refresh_token", refresh_token: refreshToken, client_id: "slop-haus-workspace" },
  });
  expect(replay.status(), await replay.text()).toBe(400);
  expect((await replay.json()).error).toBe("invalid_grant");

  // Not just the replayed token refused -- the *new* access token the
  // legitimate refresh above just minted is dead too, because the grant
  // underneath both of them is gone.
  const check = await frontendApi.post("/hooks/session-start", {
    headers: { Authorization: `Bearer ${(await second.json()).access_token}` },
    data: { session_id: `dead-after-replay-${Date.now()}`, cwd: "/srv/ws/1" },
  });
  expect(check.status(), await check.text()).toBe(401);
});

// A refresh whose response was lost is retried with the same token: inside
// the reuse grace window that gets the identical pair back, not a revocation.
test("replaying the just-rotated refresh token returns the same pair", async ({ api, playwright, frontendApi }) => {
  const { refreshToken } = await credentialAgentViaDeviceFlow(playwright, api, `refresh-grace-${Date.now()}`);
  const form = { grant_type: "refresh_token", refresh_token: refreshToken, client_id: "slop-haus-workspace" };

  const first = await frontendApi.post("/oauth/token", { form });
  expect(first.status(), await first.text()).toBe(200);
  const firstTokens = await first.json();

  const retry = await frontendApi.post("/oauth/token", { form });
  expect(retry.status(), await retry.text()).toBe(200);
  const retryTokens = await retry.json();
  expect(retryTokens.refresh_token).toBe(firstTokens.refresh_token);
  expect(retryTokens.access_token).toBe(firstTokens.access_token);
});

// Mirrors invite_page.tsx's pending-token pattern (see app.test.tsx's own
// "returns to a pending invite" test), for the consent page: a signed-out
// visitor following a real authorization URL is sent to /login, and comes
// back to the same consent page -- not the board -- once signed in.
test("a signed-out visitor to /authorize is sent through /login and back to the consent page", async ({
  anonPage,
  frontendApi,
}) => {
  const client = await registerClient(frontendApi, "Headless Test Client", unusedLoopbackPort());
  const { challenge } = newPkcePair();
  const authorizeUrl = buildAuthorizeUrl("", client, challenge, `state-${Date.now()}`);

  await anonPage.goto(authorizeUrl);
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(anonPage).toHaveURL(/\/login$/);

  await fillLoginForm(anonPage, DEV_USER_EMAIL, DEV_USER_PASSWORD);

  await expect(anonPage.getByText(/wants to act as one of your agents/)).toBeVisible();
  await expect(anonPage).toHaveURL(/\/authorize(\?|$)/);
  await expect(anonPage.getByRole("heading", { name: "slop-haus" })).toHaveCount(0);
});

// The device page's version of the round trip above: the code and agent
// name in the link token.sh prints are gone from the URL by the time the
// visitor is back from /login, and still have to reach the page.
//
// A fresh account rather than the dev user: sign-in is rate-limited per
// address (server/cmd/api/main.go's LoginLimit), and the test above already
// spends one of the dev user's attempts.
test("a signed-out visitor to /device is sent through /login and back to the device page", async ({
  anonPage,
  frontendApi,
}) => {
  const account = newAccount("device-round-trip");
  await registerAndActivate(anonPage, account);

  const startResp = await frontendApi.post("/oauth/device_authorization", {
    form: { client_id: "slop-haus-workspace" },
  });
  expect(startResp.status(), await startResp.text()).toBe(200);
  const start = await startResp.json();

  const agentName = `device-round-trip-${Date.now()}`;
  await anonPage.goto(`/device?code=${encodeURIComponent(start.user_code)}&agent=${agentName}`);
  await expect(anonPage.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(anonPage).toHaveURL(/\/login$/);

  await fillLoginForm(anonPage, account.email, account.password);

  await expect(anonPage.getByText(/wants to sign in as one of your agents/)).toBeVisible();
  await expect(anonPage).toHaveURL(/\/device(\?|$)/);
  await expect(anonPage.getByLabel("New agent name")).toHaveValue(agentName);

  await anonPage.getByRole("button", { name: "Approve" }).click();
  await expect(anonPage.getByText(/Done — return to your terminal/)).toBeVisible();
});

// oauth.go's Service.DecideAuthRequest answers a refusal with
// ErrorRedirect(..., errDenied) -- RFC 6749 section 4.1.2.1's error
// response, with the *original* state carried back so the client can match
// it to the request it started, and no code at all.
test("denying consent sends the browser to the client's redirect_uri with access_denied and no code", async ({
  page,
  frontendApi,
}) => {
  const client = await registerClient(frontendApi, "Deny Test Client", unusedLoopbackPort());
  const { challenge } = newPkcePair();
  const state = `state-${Date.now()}`;
  const authorizeUrl = buildAuthorizeUrl("", client, challenge, state);

  let captured: URLSearchParams | null = null;
  await page.route(`${client.redirectUri}**`, async (route) => {
    captured = new URL(route.request().url()).searchParams;
    await route.abort();
  });

  await page.goto(authorizeUrl);
  await expect(page.getByText(/wants to act as one of your agents/)).toBeVisible();
  await page.getByRole("button", { name: "Deny" }).click();

  await expect.poll(() => captured, { message: "the browser should have been sent to the client's redirect" }).not.toBeNull();
  expect(captured!.get("error")).toBe("access_denied");
  expect(captured!.get("state")).toBe(state);
  expect(captured!.get("code")).toBeNull();
});

// The device page's own "Deny" (store.go's DenyDeviceCode) has to reach the
// *polling* side of the flow, not just the browser: a workspace's token.sh
// has no page to redirect, only PollDeviceCode's answer to go on.
test("denying a device sign-in makes the poll return access_denied", async ({ page, frontendApi }) => {
  const startResp = await frontendApi.post("/oauth/device_authorization", {
    form: { client_id: "slop-haus-workspace" },
  });
  expect(startResp.status(), await startResp.text()).toBe(200);
  const start = await startResp.json();

  await page.goto(`/device?code=${encodeURIComponent(start.user_code)}`);
  await expect(page.getByText(/wants to sign in as one of your agents/)).toBeVisible();
  await page.getByRole("button", { name: "Deny" }).click();
  await expect(page.getByText(/Denied\. Done/)).toBeVisible();

  const tokenResp = await frontendApi.post("/oauth/token", {
    form: {
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
      device_code: start.device_code,
      client_id: "slop-haus-workspace",
    },
  });
  expect(tokenResp.status()).toBe(400);
  const body = await tokenResp.json();
  expect(body.error).toBe("access_denied");
});
