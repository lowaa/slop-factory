import type { APIRequestContext } from "@playwright/test";

import { DEV_USER_PASSWORD } from "./env";
import { expect } from "./fixtures";
import { credentialAgentViaCodeFlow, credentialExistingAgentViaCodeFlow } from "./oauth";

export interface NewTicketOptions {
  paths?: string[];
  status?: string;
  headers?: Record<string, string>;
}

export async function newTicket(
  api: APIRequestContext,
  title: string,
  { paths, status, headers }: NewTicketOptions = {},
): Promise<{ id: number }> {
  const resp = await api.post("/api/tickets", {
    data: {
      title,
      ...(paths ? { paths: paths.map((path) => ({ path })) } : {}),
      ...(status ? { status } : {}),
    },
    headers,
  });
  expect(resp.status(), await resp.text()).toBe(201);
  return resp.json();
}

export async function fileTicket(api: APIRequestContext, title: string, status?: string): Promise<number> {
  return (await newTicket(api, title, { status })).id;
}

export interface TestAgent {
  id: string;
  name: string;
  accessToken: string;
  auth: { Authorization: string };
}

// An agent has no credential of its own until something is authorized to
// act as it -- see oauth.ts.
export async function newAgent(api: APIRequestContext, name: string): Promise<TestAgent> {
  const { agentId, accessToken } = await credentialAgentViaCodeFlow(api, name);
  return { id: agentId, name, accessToken, auth: { Authorization: `Bearer ${accessToken}` } };
}

export async function registerConductor(
  api: APIRequestContext,
  name: string,
): Promise<{ id: string; accessToken: string }> {
  // Registering needs a recent sign-in, which the worker's session may have
  // outlived. Confirming the password is rate limited across the suite, so it
  // is done only when asked for.
  let registered = await api.post("/api/conductors", { data: { name } });
  if (registered.status() === 401 || registered.status() === 403) {
    const reauth = await api.post("/api/auth/reauth/password", { data: { password: DEV_USER_PASSWORD } });
    expect(reauth.ok(), await reauth.text()).toBeTruthy();
    registered = await api.post("/api/conductors", { data: { name } });
  }
  expect(registered.status(), await registered.text()).toBe(201);
  const list = (await (await api.get("/api/conductors")).json()) as { conductors: { id: string; name: string }[] };
  const { id } = list.conductors.find((c) => c.name === name)!;
  const { accessToken } = await credentialExistingAgentViaCodeFlow(api, id);
  return { id, accessToken };
}
