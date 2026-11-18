import { describe, expect, it, vi } from "vitest";
import { SessionClient, TENANT_HEADER } from "./session_client";

function jsonResponse(status: number, body: unknown = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

// A BroadcastChannel stand-in that delivers to the others of the same name,
// as the browser does, never to the sender.
function fakeChannels() {
  const open: { name: string; onmessage: ((e: MessageEvent) => void) | null }[] = [];
  const create = (name: string) => {
    const channel = {
      name,
      onmessage: null as ((e: MessageEvent) => void) | null,
      postMessage(data: unknown) {
        for (const other of open) if (other !== channel && other.name === name) other.onmessage?.({ data } as MessageEvent);
      },
      close() {},
    };
    open.push(channel);
    return channel as unknown as BroadcastChannel;
  };
  return create;
}

function newSession(answers: Response[] = [], createChannel?: (name: string) => BroadcastChannel, scoped = true) {
  const sent: { url: string; tenant: string | null }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(new URL(String(input), "http://app.test"), init);
    sent.push({ url: new URL(request.url).pathname, tenant: request.headers.get(TENANT_HEADER) });
    return answers.shift() ?? jsonResponse(200);
  });
  const session = new SessionClient({
    refreshUrl: "http://app.test/api/auth/refresh",
    fetch: fetchMock,
    tenantScoped: scoped,
    tenantChannel: createChannel ? { name: "tenant", create: createChannel } : undefined,
  });
  return { session, sent };
}

const tenantChanged = () => jsonResponse(409, { code: "tenant_changed" });

describe("SessionClient tenant", () => {
  it("names the tenant the page loaded in on every request, and for senders of its own", async () => {
    const { session, sent } = newSession();
    await session.send("/api/before");
    session.signedIn({ tenant_id: "a", csrf_token: "tok" });
    await session.send("/api/read");
    await session.send("/api/write", { method: "POST" });
    expect(sent.map((s) => s.tenant)).toEqual([null, "a", "a"]);
    expect(session.writeHeaders()).toMatchObject({ [TENANT_HEADER]: "a" });
    expect(session.tenantId()).toBe("a");
  });

  it("announces a tenant_changed answer once, never retries, and hands the answer back", async () => {
    const { session, sent } = newSession([tenantChanged()]);
    session.signedIn({ tenant_id: "a", csrf_token: "tok" });
    const changed = vi.fn();
    session.onTenantChanged(changed);
    const signedOut = vi.fn();
    session.onSignedOut(signedOut);

    const response = await session.send("/api/write", { method: "POST" });

    expect(response.status).toBe(409);
    expect(sent).toHaveLength(1);
    expect(changed).toHaveBeenCalledTimes(1);
    expect(changed).toHaveBeenCalledWith("a");
    expect(signedOut).not.toHaveBeenCalled();
  });

  it("does not take a 409 with another code for a tenant change", async () => {
    const { session } = newSession([jsonResponse(409, { code: "role_in_use" })]);
    session.signedIn({ tenant_id: "a", csrf_token: "tok" });
    const changed = vi.fn();
    session.onTenantChanged(changed);
    await session.send("/api/things", { method: "DELETE" });
    expect(changed).not.toHaveBeenCalled();
  });

  it("tells other tabs after a switch, and is told by them", async () => {
    const create = fakeChannels();
    const tab1 = newSession([], create).session;
    const tab2 = newSession([], create).session;
    tab1.signedIn({ tenant_id: "a", csrf_token: "tok" });
    tab2.signedIn({ tenant_id: "a", csrf_token: "tok" });
    const changed1 = vi.fn();
    const changed2 = vi.fn();
    tab1.onTenantChanged(changed1);
    tab2.onTenantChanged(changed2);

    tab1.tenantSwitched({ tenant_id: "b", csrf_token: "tok" });

    expect(changed2).toHaveBeenCalledWith("a");
    expect(changed1).not.toHaveBeenCalled();
    expect(tab1.tenantId()).toBe("b");
  });

  it("ignores a broadcast that names the tenant it is already in", () => {
    const create = fakeChannels();
    const tab1 = newSession([], create).session;
    const tab2 = newSession([], create).session;
    tab1.signedIn({ tenant_id: "a", csrf_token: "tok" });
    tab2.signedIn({ tenant_id: "b", csrf_token: "tok" });
    const changed = vi.fn();
    tab2.onTenantChanged(changed);
    tab1.tenantSwitched({ tenant_id: "b", csrf_token: "tok" });
    expect(changed).not.toHaveBeenCalled();
  });

  it("ignores a broadcast about a session it has since left", () => {
    const create = fakeChannels();
    const tab1 = newSession([], create).session;
    const tab2 = newSession([], create).session;
    tab1.signedIn({ tenant_id: "a", csrf_token: "tok" });
    tab2.signedIn({ tenant_id: "c", csrf_token: "tok" });
    const changed = vi.fn();
    tab2.onTenantChanged(changed);
    tab1.tenantSwitched({ tenant_id: "b", csrf_token: "tok" });
    expect(changed).not.toHaveBeenCalled();
  });

  it("does not take a late refusal for the tenant this page has since switched from", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    const session = new SessionClient({
      refreshUrl: "/refresh",
      tenantScoped: true,
      tenantChannel: null,
      fetch: (async () => {
        await gate;
        return tenantChanged();
      }),
    });
    session.signedIn({ tenant_id: "a", csrf_token: "tok" });
    const changed = vi.fn();
    session.onTenantChanged(changed);

    const pending = session.send("/api/read");
    session.tenantSwitched({ tenant_id: "b", csrf_token: "tok" });
    release();
    await pending;

    expect(changed).not.toHaveBeenCalled();
  });

  it("leaves tenants alone in an app that has none", async () => {
    const { session, sent } = newSession([], undefined, false);
    session.signedIn({ tenant_id: "a", csrf_token: "tok" });
    await session.send("/api/read");
    expect(sent[0].tenant).toBeNull();
    expect(session.writeHeaders()).toEqual({ "X-CSRF-Token": "tok" });
    expect(session.tenantId()).toBeNull();
  });

  it("does not announce a tenant change on a page with no session", () => {
    const create = fakeChannels();
    const tab1 = newSession([], create).session;
    const anonymous = newSession([], create).session;
    const changed = vi.fn();
    anonymous.onTenantChanged(changed);
    tab1.signedIn({ tenant_id: "a", csrf_token: "tok" });
    tab1.tenantSwitched({ tenant_id: "b", csrf_token: "tok" });
    expect(changed).not.toHaveBeenCalled();
  });
});
