// Credentials an agent the way a real workspace does: over OAuth's device
// grant (RFC 8628), the same path server/internal/workspace/assets/sh/token.sh's
// cmd_login and ../../agent_workspace.sh's approve_device_login walk. Every
// spec that used to mint a static bearer token from POST /api/agents
// (created.token, gone now that agents have no credential of their own --
// see server/internal/agent/handler.go's AgentBody doc comment) goes
// through this instead.
//
// The device_authorization and token calls go through the *frontend*
// origin, not the api service directly: that is what proves
// frontend/nginx.conf.template's /oauth/ proxy actually works, the same
// reasoning one_origin.spec.ts applies to /mcp and /hooks/. The lookup and
// decision calls go through `api` (a signed-in board session) -- that is
// the human's half of the flow, played here by the already-authenticated
// test fixture instead of a real person clicking through /device.
import { createHash, randomBytes } from "node:crypto";
import type { APIRequestContext, PlaywrightWorkerArgs } from "@playwright/test";
import { FRONTEND_BASE_URL } from "@/support/env";

// Seeded by the migration, not registered per workspace -- see
// oauth.WorkspaceClientID.
const WORKSPACE_CLIENT_ID = "slop-haus-workspace";

export interface PkcePair {
  verifier: string;
  challenge: string;
}

// newPkcePair mints a real S256 PKCE pair (RFC 7636) -- oauth.spec.ts's
// code-flow test is only worth having if it drives the same challenge/
// verifier math a real client does, not a hand-waved pair the server
// happens to accept.
//
// 32 random bytes base64url-encoded is 43 characters with no padding --
// exactly oauth.go's verifyPKCE minimum, and base64url's alphabet
// (A-Za-z0-9-_) is a subset of RFC 7636's "unreserved" characters, so
// nothing here needs further encoding.
export function newPkcePair(): PkcePair {
  const verifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  return { verifier, challenge };
}

export interface RegisteredClient {
  clientId: string;
  redirectUri: string;
}

// A fresh, nothing-listens-here port per registration -- oauth.go's
// validRedirectURI only accepts loopback addresses at all, and nothing
// here ever actually dials one: the whole point of these clients is that
// their "callback" is either intercepted (oauth.spec.ts's browser test) or
// never followed at all (credentialAgentViaCodeFlow below just reads the
// code out of the redirect_to URL).
let nextLoopbackPort = 41000;
export function unusedLoopbackPort(): number {
  return nextLoopbackPort++;
}

// registerClient runs dynamic client registration (RFC 7591) against a
// loopback redirect URI nothing listens on -- oauth.go's validRedirectURI
// only accepts loopback addresses at all, which is what the code flow's
// callback interception relies on: the request never actually goes
// anywhere.
export async function registerClient(
  api: APIRequestContext,
  clientName: string,
  port: number,
): Promise<RegisteredClient> {
  const redirectUri = `http://127.0.0.1:${port}/callback`;
  const resp = await api.post("/oauth/register", {
    data: { client_name: clientName, redirect_uris: [redirectUri] },
  });
  if (!resp.ok()) {
    throw new Error(`client registration failed: ${resp.status()} ${await resp.text()}`);
  }
  const body = await resp.json();
  return { clientId: body.client_id as string, redirectUri };
}

// buildAuthorizeUrl is /oauth/authorize's query string, the same shape a
// real MCP client (or oauth.go's own AuthorizeParams) builds it with.
export function buildAuthorizeUrl(
  base: string,
  client: RegisteredClient,
  challenge: string,
  state: string,
): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: client.clientId,
    redirect_uri: client.redirectUri,
    code_challenge: challenge,
    code_challenge_method: "S256",
    state,
  });
  return `${base}/oauth/authorize?${params.toString()}`;
}

export interface AgentCredentials {
  agentId: string;
  accessToken: string;
  refreshToken: string;
}

async function jsonOrThrow(resp: { ok(): boolean; status(): number; text(): Promise<string> }, what: string) {
  if (!resp.ok()) {
    throw new Error(`${what} failed: ${resp.status()} ${await resp.text()}`);
  }
}

// pollForToken polls /oauth/token (through the frontend origin, form-
// encoded, exactly as token.sh's cmd_login does) until the device code's
// grant is issued. The decision is already made by the time this is
// called, so the very first poll normally succeeds -- store.go's
// PollDeviceCode only calls a poll "too soon" *relative to a previous
// poll*, and there is none yet -- but this still loops (honouring
// slow_down/authorization_pending) rather than assuming that, since a slow
// test runner is exactly the kind of thing that would turn "normally"
// into a flake.
async function pollForToken(
  frontend: APIRequestContext,
  deviceCode: string,
  intervalSeconds: number,
  expiresInSeconds: number,
): Promise<{ access_token: string; refresh_token: string }> {
  const deadline = Date.now() + expiresInSeconds * 1000;
  let interval = intervalSeconds;
  while (Date.now() < deadline) {
    const resp = await frontend.post("/oauth/token", {
      form: {
        grant_type: "urn:ietf:params:oauth:grant-type:device_code",
        device_code: deviceCode,
        client_id: WORKSPACE_CLIENT_ID,
      },
    });
    const body = await resp.json();
    if (resp.ok()) {
      return body as { access_token: string; refresh_token: string };
    }
    switch (body.error) {
      case "authorization_pending":
        await new Promise((resolve) => setTimeout(resolve, interval * 1000));
        continue;
      case "slow_down":
        interval += 5;
        await new Promise((resolve) => setTimeout(resolve, interval * 1000));
        continue;
      default:
        throw new Error(`device token poll failed: ${resp.status()} ${JSON.stringify(body)}`);
    }
  }
  throw new Error("device flow timed out waiting for a token");
}

// credentialAgentViaDeviceFlow creates (or reuses, if agentId is given) an
// agent and hands back a live access/refresh token pair for it, exactly as
// a workspace ends up with one: start a device authorization, approve it
// as this agent with the given signed-in api context, then poll for
// tokens.
export async function credentialAgentViaDeviceFlow(
  playwright: PlaywrightWorkerArgs["playwright"],
  api: APIRequestContext,
  name: string,
): Promise<AgentCredentials> {
  const agentResp = await api.post("/api/agents", { data: { name } });
  await jsonOrThrow(agentResp, `create agent ${name}`);
  const agent = await agentResp.json();

  const frontend = await playwright.request.newContext({ baseURL: FRONTEND_BASE_URL });
  try {
    const startResp = await frontend.post("/oauth/device_authorization", {
      form: { client_id: WORKSPACE_CLIENT_ID },
    });
    await jsonOrThrow(startResp, "start device authorization");
    const start = await startResp.json();

    const lookupResp = await api.post("/api/oauth/device/lookup", { data: { user_code: start.user_code } });
    await jsonOrThrow(lookupResp, `look up device code ${start.user_code}`);

    const decisionResp = await api.post("/api/oauth/device/decision", {
      data: { user_code: start.user_code, approve: true, agent_id: agent.id },
    });
    if (decisionResp.status() !== 204) {
      throw new Error(`approve device code failed: ${decisionResp.status()} ${await decisionResp.text()}`);
    }

    const tokens = await pollForToken(frontend, start.device_code, start.interval, start.expires_in);
    return { agentId: agent.id as string, accessToken: tokens.access_token, refreshToken: tokens.refresh_token };
  } finally {
    await frontend.dispose();
  }
}

// -- Bulk credentialing, over the authorization code grant --
//
// oauth.go's device flow is rate limited on purpose: LookupDevice (which
// both /api/oauth/device/lookup and DecideDevice call) is bounded at 20
// attempts per 10 minutes *per signed-in user* (cmd/api/main.go's
// DeviceLookupLimit) -- sized for a human occasionally approving a
// workspace, not for a test suite where dozens of specs each mint an agent
// under the one shared, signed-in dev user. Running credentialAgentViaDeviceFlow
// at that volume was tried first and reliably 429s partway through the
// suite (see the CONTRACT PROBLEM note in this repo's task notes / the PR
// description this change ships with) -- worse, once a handful of those
// 429s happen the suite's login rate limit (10/minute, same file) gets
// caught in the crossfire too, and every remaining test fails.
//
// credentialAgentViaDeviceFlow above still exists, and is still what
// oauth.spec.ts's own device-flow tests and one Agents-panel revoke test
// use -- proving the device grant works end to end matters, at low volume.
// But it is the wrong tool for "give this test an agent with a token",
// used dozens of times a run: DecideAuthRequest (the consent page's
// decision, which is what this drives) carries no rate limiter at all, so
// this is what the bulk of the ported specs use instead. One client is
// registered once and reused for the life of the suite (registering one
// per call would just trade the device limiter for RegisterLimit's 20/hour).
let sharedCodeFlowClient: Promise<RegisteredClient> | null = null;

async function codeFlowClient(api: APIRequestContext): Promise<RegisteredClient> {
  sharedCodeFlowClient ??= registerClient(api, "e2e shared code-flow client", unusedLoopbackPort());
  return sharedCodeFlowClient;
}

// credentialAgentViaCodeFlow is credentialAgentViaDeviceFlow's sibling for
// every other spec that just needs a working agent token: create the
// agent, drive it through the authorization code grant (GET
// /oauth/authorize without following the redirect, approve it exactly as
// the consent page's own decision call does, then exchange the code) and
// hand back the same {agentId, accessToken, refreshToken} shape.
export async function credentialAgentViaCodeFlow(
  api: APIRequestContext,
  name: string,
): Promise<AgentCredentials> {
  const agentResp = await api.post("/api/agents", { data: { name } });
  await jsonOrThrow(agentResp, `create agent ${name}`);
  const agent = await agentResp.json();
  return credentialExistingAgentViaCodeFlow(api, agent.id as string);
}

// credentialExistingAgentViaCodeFlow is the same sign-in for an agent that
// already exists -- a conductor, which is registered through its own route.
export async function credentialExistingAgentViaCodeFlow(
  api: APIRequestContext,
  agentId: string,
): Promise<AgentCredentials> {
  const tokens = await codeFlowTokens(api, { agent_id: agentId });
  return { agentId, ...tokens };
}

// credentialConsoleViaCodeFlow registers a console -- the person's own
// session -- the way the consent screen does: a new agent of kind console,
// chosen in the decision itself.
export async function credentialConsoleViaCodeFlow(
  api: APIRequestContext,
  name: string,
): Promise<AgentCredentials> {
  const tokens = await codeFlowTokens(api, { agent_name: name, agent_kind: "console" });
  const listResp = await api.get("/api/agents");
  await jsonOrThrow(listResp, "list agents");
  const agent = (await listResp.json()).agents.find((a: { name: string }) => a.name === name);
  if (!agent) {
    throw new Error(`console ${name} is not in the agent list`);
  }
  return { agentId: agent.id as string, ...tokens };
}

// credentialSupervisorViaCodeFlow registers a supervisor the way the add-a-machine
// consent does, so a spec can act as the supervisor against its own routes.
export async function credentialSupervisorViaCodeFlow(
  api: APIRequestContext,
  name: string,
  slotCap = 4,
): Promise<AgentCredentials> {
  const tokens = await codeFlowTokens(api, { agent_name: name, agent_kind: "supervisor", slot_cap: slotCap });
  const listResp = await api.get("/api/supervisors");
  await jsonOrThrow(listResp, "list supervisors");
  const supervisor = (await listResp.json()).supervisors.find((s: { name: string }) => s.name === name);
  if (!supervisor) {
    throw new Error(`supervisor ${name} is not in the supervisor list`);
  }
  return { agentId: supervisor.id as string, ...tokens };
}

async function codeFlowTokens(
  api: APIRequestContext,
  agentChoice: Record<string, string | number>,
): Promise<Omit<AgentCredentials, "agentId">> {
  const client = await codeFlowClient(api);
  const { verifier, challenge } = newPkcePair();

  // maxRedirects: 0 -- the Location header is the point: oauth.go's
  // Authorize hands back "<issuer>/authorize?request=<id>", and nothing
  // here needs (or wants) to actually follow it to the SPA.
  const authorizeResp = await api.get(buildAuthorizeUrl("", client, challenge, `state-${Date.now()}`), {
    maxRedirects: 0,
  });
  if (authorizeResp.status() !== 302) {
    throw new Error(`authorize did not redirect: ${authorizeResp.status()} ${await authorizeResp.text()}`);
  }
  const location = authorizeResp.headers()["location"];
  const requestId = new URL(location, FRONTEND_BASE_URL).searchParams.get("request");
  if (!requestId) {
    throw new Error(`authorize's redirect carried no request id: ${location}`);
  }

  const decisionResp = await api.post(`/api/oauth/requests/${requestId}/decision`, {
    data: { approve: true, ...agentChoice },
  });
  await jsonOrThrow(decisionResp, "decide auth request");
  const decision = await decisionResp.json();
  const code = new URL(decision.redirect_to).searchParams.get("code");
  if (!code) {
    throw new Error(`decision's redirect carried no code: ${decision.redirect_to}`);
  }

  const tokenResp = await api.post("/oauth/token", {
    form: {
      grant_type: "authorization_code",
      code,
      redirect_uri: client.redirectUri,
      client_id: client.clientId,
      code_verifier: verifier,
    },
  });
  await jsonOrThrow(tokenResp, "exchange code for tokens");
  const tokens = await tokenResp.json();
  return { accessToken: tokens.access_token, refreshToken: tokens.refresh_token };
}
