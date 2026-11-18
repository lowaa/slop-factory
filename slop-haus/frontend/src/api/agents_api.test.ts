// The agents half of apiClient's wire behaviour, against a mocked fetch; see index.test.ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { wireOf, installFetch, jsonResponse, sent, urlsOf, type FetchMock } from "./fetch_recorder";

type Api = typeof import("./index");
type ApiClient = Api["apiClient"];

describe("apiClient agents", () => {
  let fetchMock: FetchMock;
  let api: Api;
  let apiClient: ApiClient;

  beforeEach(async () => {
    ({ fetchMock, api } = await installFetch());
    apiClient = api.apiClient;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("asks for archived fleet instructions only when told to", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(200, { versions: [] })));

    await apiClient.getFleetInstructions();
    await apiClient.getFleetInstructions(true);
    await apiClient.getFleetInstructions(false, "sensitive_words");
    await apiClient.getFleetInstructions(true, "steward");

    expect(urlsOf(fetchMock)[0]).toBe("/api/fleet_instructions");
    expect(urlsOf(fetchMock)[1]).toBe("/api/fleet_instructions?include_archived=true");
    expect(urlsOf(fetchMock)[2]).toBe("/api/fleet_instructions?kind=sensitive_words");
    expect(urlsOf(fetchMock)[3]).toBe("/api/fleet_instructions?include_archived=true&kind=steward");
  });

  it("unwraps the {agents: [...]} envelope listAgents gets back", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { agents: [{ id: "a1", name: "bot", lease: null }] }));

    const agents = await apiClient.listAgents();

    expect(agents).toEqual([{ id: "a1", name: "bot", lease: null }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/agents");
  });

  it("PATCHes the disabled flag to /api/agents/{id}", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: "a1", name: "bot", disabled_at: "2026-01-01T00:00:00Z" }));

    await apiClient.setAgentDisabled("a1", true);

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/agents/a1");
    expect(req.method).toBe("PATCH");
    expect(req.body).toEqual({ disabled: true });
  });

  it("unwraps the {roles: [...]} envelope listAgentRoles gets back", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { roles: [{ id: "r1", name: "reviewer", description: "" }] }));

    const roles = await apiClient.listAgentRoles();

    expect(roles).toEqual([{ id: "r1", name: "reviewer", description: "" }]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/agent-roles");
  });

  it("creates a role with its name and description", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: "r1", name: "reviewer", description: "reviews PRs" }));

    const created = await apiClient.createAgentRole("reviewer", "reviews PRs");

    expect(created).toEqual({ id: "r1", name: "reviewer", description: "reviews PRs" });
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/agent-roles");
    expect(req.method).toBe("POST");
    expect(req.body).toEqual({ name: "reviewer", description: "reviews PRs" });
  });

  it("creates a role with an empty description when none is given", async () => {
    fetchMock.mockResolvedValue(jsonResponse(201, { id: "r1", name: "reviewer", description: "" }));

    await apiClient.createAgentRole("reviewer");

    const req = await sent(fetchMock, 0);
    expect(req.body).toEqual({ name: "reviewer", description: "" });
  });

  it("PATCHes only the given fields of a role", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: "r1", name: "renamed", description: "" }));

    const updated = await apiClient.updateAgentRole("r1", { name: "renamed" });

    expect(updated).toEqual({ id: "r1", name: "renamed", description: "" });
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/agent-roles/r1");
    expect(req.method).toBe("PATCH");
    expect(req.body).toEqual({ name: "renamed" });
  });

  it("deletes a role by id", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.deleteAgentRole("r1");

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/agent-roles/r1");
    expect(req.method).toBe("DELETE");
  });

  it("passes clear_tickets=true when asked to delete anyway", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.deleteAgentRole("r1", true);

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/agent-roles/r1?clear_tickets=true");
  });

  it("throws a RoleInUseError naming the tickets on a 409 with a ticket list", async () => {
    const { RoleInUseError } = api;
    fetchMock.mockResolvedValue(
      jsonResponse(409, {
        status: 409,
        title: "Conflict",
        detail: "1 ticket(s) require this role.",
        code: "role_in_use",
        tickets: [{ id: 4, title: "Deploy it", status: "open" }],
      }),
    );

    const err = await apiClient.deleteAgentRole("r1").catch((e: unknown) => e);

    expect(err).toBeInstanceOf(RoleInUseError);
    expect(err).toMatchObject({ status: 409, message: "1 ticket(s) require this role." });
    expect((err as InstanceType<typeof RoleInUseError>).tickets).toEqual([
      { id: 4, title: "Deploy it", status: "open" },
    ]);
  });

  it("does not mistake an ordinary 409 for a role-in-use error", async () => {
    const { ApiError, RoleInUseError } = api;
    for (const body of [
      { status: 409, detail: "stale revision" },
      // A ticket list alone isn't enough: only the role_in_use code says what it means.
      { status: 409, detail: "something else", tickets: [] },
    ]) {
      fetchMock.mockResolvedValue(jsonResponse(409, body));

      const err = await apiClient.deleteAgentRole("r1").catch((e: unknown) => e);

      expect(err).toBeInstanceOf(ApiError);
      expect(err).not.toBeInstanceOf(RoleInUseError);
    }
  });

  it("PUTs an agent's role ids and returns the updated agent", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: "a1", name: "bot", roles: [{ id: "r1", name: "reviewer" }] }));

    const agent = await apiClient.setAgentRoles("a1", ["r1", "r2"]);

    expect(agent).toEqual({ id: "a1", name: "bot", roles: [{ id: "r1", name: "reviewer" }] });
    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/agents/a1/roles");
    expect(req.method).toBe("PUT");
    expect(req.body).toEqual({ role_ids: ["r1", "r2"] });
  });

  it("POSTs to /api/agents/{id}/setup_script and returns the script under the server's filename", async () => {
    fetchMock.mockResolvedValue(
      new Response("#!/usr/bin/env bash\n", {
        status: 200,
        headers: { "Content-Disposition": 'attachment; filename="slop-haus-setup-bot.sh"' },
      }),
    );

    const got = await apiClient.rotateAgentSetupScript("a1");

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/agents/a1/setup_script");
    expect(req.method).toBe("POST");
    expect(req.body).toBeUndefined();
    expect(got).toEqual({ filename: "slop-haus-setup-bot.sh", script: "#!/usr/bin/env bash\n" });
  });

  it("throws the server's detail when a new setup script is refused", async () => {
    fetchMock.mockResolvedValue(jsonResponse(409, { detail: "agent is disabled" }));

    await expect(apiClient.rotateAgentSetupScript("a1")).rejects.toThrow("agent is disabled");
  });

  it("names a setup script itself when the server gives no filename", async () => {
    fetchMock.mockResolvedValue(new Response("#!/bin/sh\n", { status: 200 }));

    await expect(api.apiClient.rotateAgentSetupScript("a1")).resolves.toEqual({
      filename: "slop-haus-setup.sh",
      script: "#!/bin/sh\n",
    });
  });
});

// The consent/device/grants routes, pinned against server/internal/oauth/
// api.go's wire shapes the same way the sections above pin the rest of the
// client.
describe("apiClient oauth", () => {
  let fetchMock: FetchMock;
  let api: Api;
  let apiClient: ApiClient;

  beforeEach(async () => {
    ({ fetchMock, api } = await installFetch());
    apiClient = api.apiClient;
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches a pending authorization by request id", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { client_name: "Claude Code", redirect_host: "127.0.0.1:9876" }));

    await expect(apiClient.getAuthRequest("req-1")).resolves.toEqual({
      client_name: "Claude Code",
      redirect_host: "127.0.0.1:9876",
    });
    expect(urlsOf(fetchMock)[0]).toBe("/api/oauth/requests/req-1");
  });

  it("decides an authorization request with an existing agent, and unwraps redirect_to", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { redirect_to: "http://127.0.0.1:9876/callback?code=abc" }));

    const redirectTo = await apiClient.decideAuthRequest("req-1", { approve: true, agent_id: "a1" });

    expect(redirectTo).toBe("http://127.0.0.1:9876/callback?code=abc");
    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/oauth/requests/req-1/decision",
      method: "POST",
      body: { approve: true, agent_id: "a1" },
    });
  });

  it("decides an authorization request with a new agent's name", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { redirect_to: "http://127.0.0.1:9876/callback?code=abc" }));

    await apiClient.decideAuthRequest("req-1", { approve: true, agent_name: "builder-x" });

    expect((await wireOf(fetchMock)).body).toEqual({ approve: true, agent_name: "builder-x" });
  });

  it("denies an authorization request", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { redirect_to: "http://127.0.0.1:9876/callback?error=access_denied" }));

    await apiClient.decideAuthRequest("req-1", { approve: false });

    expect((await wireOf(fetchMock)).body).toEqual({ approve: false });
  });

  it("looks up a device code", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(200, { client_name: "slop-haus workspace", user_code: "BCDF-GHJK", expires_at: "2026-01-01T00:10:00Z" }),
    );

    const pending = await apiClient.lookupDevice("bcdf-ghjk");

    expect(pending.user_code).toBe("BCDF-GHJK");
    expect(await wireOf(fetchMock)).toEqual({ url: "/api/oauth/device/lookup", method: "POST", body: { user_code: "bcdf-ghjk" } });
  });

  it("decides a device authorization, sending the user code alongside the decision", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.decideDevice("BCDF-GHJK", { approve: true, agent_name: "builder-x" });

    expect(await wireOf(fetchMock)).toEqual({
      url: "/api/oauth/device/decision",
      method: "POST",
      body: { user_code: "BCDF-GHJK", approve: true, agent_name: "builder-x" },
    });
  });

  it("unwraps the {grants: [...]} envelope listGrants gets back", async () => {
    const grant = {
      id: "g1",
      agent_id: "a1",
      client_id: "c1",
      client_name: "Claude Code",
      created_at: "2026-01-01T00:00:00Z",
      last_used_at: null,
    };
    fetchMock.mockResolvedValue(jsonResponse(200, { grants: [grant] }));

    await expect(apiClient.listGrants()).resolves.toEqual([grant]);
    expect(urlsOf(fetchMock)[0]).toBe("/api/oauth/grants");
  });

  it("revokes a grant by id", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await apiClient.revokeGrant("g1");

    const req = await sent(fetchMock, 0);
    expect(req.url).toBe("/api/oauth/grants/g1");
    expect(req.method).toBe("DELETE");
  });
});
