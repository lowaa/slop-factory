// One origin has to serve everything: the human's browser, the API, MCP
// and the hooks. That is what lets a workspace be configured with a single
// URL -- the address of the page the human is already looking at -- rather
// than asking someone to supply a second one for the agents.
//
// It is only true because frontend/nginx.conf.template proxies /mcp,
// /hooks/, /oauth/ and /.well-known/oauth- as well as /api/. Without those
// blocks they fall through to the SPA, and an agent (or an MCP client
// discovering this server) asking for its tools gets index.html back: a
// 200, with no error anywhere, which is the worst shape a misconfiguration
// can take. oauth.spec.ts is what actually exercises the OAuth blocks end
// to end; the ones checked directly here are the pre-existing agent
// surface.
import { expect, test } from "@/support/fixtures";
import { FRONTEND_BASE_URL } from "@/support/env";
import { credentialAgentViaCodeFlow } from "@/support/oauth";

// The paths an agent uses that are not under /api.
const AGENT_PATHS = ["/mcp", "/hooks/stop", "/hooks/session-start"];

for (const path of AGENT_PATHS) {
  test(`${path} reaches the api through the frontend origin`, async ({ frontendApi }) => {
    const resp = await frontendApi.post(path, {
      headers: { "Content-Type": "application/json" },
      data: {},
    });

    // `frontendApi` is signed in as a user (see support/fixtures.ts), but
    // these are agent routes -- a user's session cookie doesn't satisfy
    // authn.RequireAgent(), so the api still rejects it, which is the
    // point: the rejection proves the api answered. The SPA fallback would
    // serve index.html instead, and nginx answers a POST to a static
    // location with 405 before that.
    expect(resp.status(), `${path} should be answered by the api`).toBe(401);

    const type = resp.headers()["content-type"] ?? "";
    expect(type, `${path} returned a page, so it was served by the SPA fallback`).not.toContain(
      "text/html",
    );
  });
}

// The half that already worked, kept alongside so the three are read
// together as "the whole agent surface". Now needs `frontendApi` signed in
// (see support/fixtures.ts) -- GET /api/tickets is authn.RequireUser() --
// but the point of this test is the proxy hop, not the auth, so a signed-in
// 200 makes the same case a public one used to.
test("/api reaches the api through the frontend origin", async ({ frontendApi }) => {
  const resp = await frontendApi.get("/api/tickets");

  expect(resp.status()).toBe(200);
  expect(resp.headers()["content-type"] ?? "").toContain("application/json");
});

// GET /api/workspace/update_script is authn.RequireAgent(), not
// RequireUser() like the rest of /api -- `frontendApi`'s signed-in session
// cookie is the wrong kind of credential for it, same as the AGENT_PATHS
// above, so this needs a real agent access token to see the 200 side of
// the proxy hop, not just the 401 the cookie alone would already prove.
test("GET /api/workspace/update_script works with an agent token through the frontend origin, 401 without one", async ({
  frontendApi,
  api,
}) => {
  const { accessToken } = await credentialAgentViaCodeFlow(api, `update-script-${Date.now()}`);

  const withToken = await frontendApi.get("/api/workspace/update_script", {
    headers: { Authorization: `Bearer ${accessToken}` },
  });
  expect(withToken.status(), await withToken.text()).toBe(200);
  expect(withToken.headers()["content-type"] ?? "").toContain("text/x-shellscript");

  const withoutToken = await frontendApi.get("/api/workspace/update_script");
  expect(withoutToken.status()).toBe(401);
});

// The setup script carries the address the server was reached on, so
// somebody with a repo and no slop-haus checkout has nothing to fill in.
test.describe("setup script", () => {
  // Minting an agent needs the signed-in user `frontendApi` already is (see
  // support/fixtures.ts), not an admin token.
  test("carries the address it was fetched from, port and all", async ({ frontendApi }) => {
    const resp = await frontendApi.post("/api/agents/setup_script", {
      headers: { "Content-Type": "application/json" },
      data: { name: `script-agent-${Date.now()}` },
    });
    expect(resp.status(), await resp.text()).toBe(201);
    expect(resp.headers()["content-type"] ?? "").toContain("text/x-shellscript");
    // A bearer token must not sit in a shared cache.
    expect(resp.headers()["cache-control"] ?? "").toContain("no-store");

    const script = await resp.text();

    expect(script).toContain("/mcp");
    // No static credential any more (server/internal/workspace/script.go):
    // it signs itself in with a device code instead of carrying one.
    expect(script).not.toContain("SLOP_HAUS_TOKEN=");
    expect(script).toContain("@AGENTS.md");
  });

  // The bug this exists for: nginx's $host drops the port, so the api
  // reconstructed its own address as "http://localhost" and the script
  // configured every agent to talk to nothing. The suite reaches the
  // frontend on the default port, where an absent port is correct and the
  // bug is invisible -- so this asks for one explicitly.
  test("keeps the port of the address it was reached on", async ({ frontendApi }) => {
    const resp = await frontendApi.post("/api/agents/setup_script", {
      headers: { "Content-Type": "application/json", Host: "slop-haus.test:9443" },
      data: { name: `ported-agent-${Date.now()}` },
    });
    expect(resp.status(), await resp.text()).toBe(201);

    const script = await resp.text();
    expect(script).toContain("slop-haus.test:9443/mcp");
    expect(script).not.toContain("slop-haus.test/mcp");
  });

  // `frontendApi` is always signed in (see support/fixtures.ts), so this
  // needs its own, genuinely unauthenticated context rather than `frontendApi`.
  test("needs a signed-in user, since it mints an agent", async ({ playwright }) => {
    const anon = await playwright.request.newContext({ baseURL: FRONTEND_BASE_URL });
    const resp = await anon.post("/api/agents/setup_script", {
      headers: { "Content-Type": "application/json" },
      data: { name: `unauthorised-${Date.now()}` },
    });

    expect(resp.status()).toBe(401);
    await anon.dispose();
  });
});
