// Multi-tenant isolation: two accounts, two boards, and nothing either can
// see or touch about the other's. `api`/`page` are always the seeded dev
// user (tenant A, see support/fixtures.ts); a second, freshly registered
// account (tenant B) is signed up through the real UI so it exists exactly
// the way a real second customer would.
import type { APIRequestContext, Page, PlaywrightWorkerArgs } from "@playwright/test";

import { expect, signedInApiContext, test } from "./support/fixtures";
import { newTicket, newAgent } from "./support/api_helpers";
import { newAccount, registerAndActivate, uniqueEmail } from "./support/auth_flow";
import { credentialAgentViaCodeFlow } from "./support/oauth";

// tenantB registers and activates a second account through the real UI
// (MailHog), then returns a signed-in API context for it -- driven at the
// API level from here on, the way agent/ticket assertions need.
async function tenantB(anonPage: Page, playwright: PlaywrightWorkerArgs["playwright"]): Promise<APIRequestContext> {
  const account = newAccount("tenant-b");
  await registerAndActivate(anonPage, account);
  return signedInApiContext(playwright, account.email, account.password);
}

test("a new tenant's board is empty even though another tenant has tickets", async ({ api, anonPage, playwright }) => {
  await newTicket(api, "Tenant A's own ticket");

  const bApi = await tenantB(anonPage, playwright);
  const listed = await bApi.get("/api/tickets");
  expect(listed.status(), await listed.text()).toBe(200);
  expect((await listed.json()).tickets).toEqual([]);
  await bApi.dispose();
});

// ticket_sequences is per-tenant (see server/internal/schema/migrations/
// 000172_baseline.sql's doc comment), so a brand new tenant's first ticket
// is #1 regardless of how many tickets other tenants already hold.
test("both tenants' first ticket is #1", async ({ api, anonPage, playwright }) => {
  const aTicket = await newTicket(api, "A's first ticket");
  expect(aTicket.id).toBe(1);

  const bApi = await tenantB(anonPage, playwright);
  const bTicket = await newTicket(bApi, "B's first ticket");
  expect(bTicket.id).toBe(1);
  await bApi.dispose();
});

// RLS hides the row entirely rather than returning a permission error --
// the same 404 an id that was never used at all would get, so a probe
// learns nothing about whether that id exists in someone else's tenant.
//
// A files a *second* ticket (id 2) rather than reusing id 1: since ids are
// only unique per tenant (tickets' primary key is (tenant_id, id) -- see
// the baseline migration), B's own first ticket is also id 1, and fetching
// "1" as B would just resolve to B's own row -- true, but not evidence of
// anything. Id 2 is one B's tenant has never minted, so a 404 there can
// only mean isolation is holding, not a same-id coincidence.
test("a tenant cannot get another tenant's ticket by id", async ({ api, anonPage, playwright }) => {
  await newTicket(api, "A's first ticket");
  const aSecondTicket = await newTicket(api, "A's second ticket");
  expect(aSecondTicket.id).toBe(2);

  const bApi = await tenantB(anonPage, playwright);
  const bTicket = await newTicket(bApi, "B's only ticket");
  expect(bTicket.id).toBe(1);

  const crossTenantGet = await bApi.get(`/api/tickets/${aSecondTicket.id}`);
  expect(crossTenantGet.status()).toBe(404);

  // B's own #1 is still reachable -- this isn't a blanket "ids above 1 are
  // refused" rule, just tenant isolation.
  const ownGet = await bApi.get(`/api/tickets/${bTicket.id}`);
  expect(ownGet.status(), await ownGet.text()).toBe(200);
  await bApi.dispose();
});

// An agent belongs to the tenant of whoever registered it (agents.tenant_id
// defaults to the caller's current_tenant()); RLS scopes every claim-path
// query the same way, so A's agent asking for B's ticket by id sees no
// candidate at all -- indistinguishable from asking for a ticket that
// doesn't exist, per lease_store.go's candidateTickets.
test("an agent registered by one tenant cannot claim or see another tenant's tickets", async ({
  api,
  anonPage,
  playwright,
}) => {
  const agent = await newAgent(api, `tenant-a-agent-${Date.now()}`);

  const bApi = await tenantB(anonPage, playwright);
  const bTicket = await newTicket(bApi, "Only B should ever hand this out");

  const claim = await api.post("/api/claims", { headers: agent.auth, data: { ticket_id: bTicket.id } });
  expect(claim.status(), await claim.text()).toBe(200);
  expect((await claim.json()).ticket, "A's agent must not be able to claim B's ticket").toBeNull();

  // Not offered as the next unscoped claim either.
  const claimNext = await api.post("/api/claims", { headers: agent.auth, data: {} });
  expect(claimNext.status(), await claimNext.text()).toBe(200);
  expect((await claimNext.json()).ticket).toBeNull();

  // The control: the same agent does claim a ticket of its own tenant's --
  // so the nulls above are isolation, not claiming being broken outright.
  const aTicket = await newTicket(api, "A's own ticket");
  const ownClaim = await api.post("/api/claims", { headers: agent.auth, data: {} });
  expect(ownClaim.status(), await ownClaim.text()).toBe(200);
  expect((await ownClaim.json()).ticket?.id).toBe(aTicket.id);

  await bApi.dispose();
});

test("a tenant's queue blockage does not show another tenant's tickets", async ({ api, anonPage, playwright }) => {
  const scope = `server/tenancy-${Date.now()}/`;
  const holder = await credentialAgentViaCodeFlow(api, `holder-${Date.now()}`);
  await credentialAgentViaCodeFlow(api, `idle-${Date.now()}`);
  const held = await (await api.post("/api/tickets", { data: { title: "A holds this", paths: [{ path: scope }] } })).json();
  await api.post("/api/claims", { headers: { Authorization: `Bearer ${holder.accessToken}` }, data: { ticket_id: held.id } });
  await api.post("/api/tickets", { data: { title: "A waits on it", paths: [{ path: scope }] } });

  const aBlockage = (await (await api.get("/api/queue/blockage")).json()).blockage;
  expect(aBlockage?.holder_title).toBe("A holds this");

  const bApi = await tenantB(anonPage, playwright);
  const bBlockage = (await (await bApi.get("/api/queue/blockage")).json()).blockage;
  expect(bBlockage).toBeNull();

  await bApi.dispose();
});

// Members, invites and OAuth grants are all tenant-scoped rows (RLS, same
// as tickets above). Checked in both directions, each tenant
// seeing its own row, so a query that returned nothing couldn't pass.
test("members, invites and OAuth grants are each scoped to their own tenant", async ({ api, anonPage, playwright }) => {
  const me = await (await api.get("/api/auth/me")).json();
  const bApi = await tenantB(anonPage, playwright);
  const bMe = await (await bApi.get("/api/auth/me")).json();

  const ids = (rows: { id: string }[]) => rows.map((r) => r.id);
  const aMembers = ids((await (await api.get("/api/members")).json()).members);
  const bMembers = ids((await (await bApi.get("/api/members")).json()).members);
  expect(aMembers).toContain(me.id);
  expect(aMembers).not.toContain(bMe.id);
  expect(bMembers).toContain(bMe.id);
  expect(bMembers).not.toContain(me.id);

  const invite = async (ctx: APIRequestContext) => {
    const resp = await ctx.post("/api/invites", { data: { email: uniqueEmail("tenancy-invite") } });
    expect(resp.status(), await resp.text()).toBe(201);
    return (await resp.json()).id as string;
  };
  const aInvite = await invite(api);
  const bInvite = await invite(bApi);
  const aInvites = ids((await (await api.get("/api/invites")).json()).invites);
  const bInvites = ids((await (await bApi.get("/api/invites")).json()).invites);
  expect(aInvites).toContain(aInvite);
  expect(aInvites).not.toContain(bInvite);
  expect(bInvites).toContain(bInvite);
  expect(bInvites).not.toContain(aInvite);

  const grantAgents = async (ctx: APIRequestContext) =>
    ((await (await ctx.get("/api/oauth/grants")).json()).grants as { agent_id: string }[]).map((g) => g.agent_id);
  const aAgent = await newAgent(api, `tenancy-grant-a-${Date.now()}`);
  const bAgent = await newAgent(bApi, `tenancy-grant-b-${Date.now()}`);
  const aGrants = await grantAgents(api);
  const bGrants = await grantAgents(bApi);
  expect(aGrants).toContain(aAgent.id);
  expect(aGrants).not.toContain(bAgent.id);
  expect(bGrants).toContain(bAgent.id);
  expect(bGrants).not.toContain(aAgent.id);

  await bApi.dispose();
});
