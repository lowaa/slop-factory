import type { APIRequestContext } from "@playwright/test";

import { expect } from "./fixtures";

// The spec standing in for a supervisor, against its own routes with its own credential.

type Report = { id: string; generation: number; phase: string; reason?: string };

export class StubSupervisor {
  constructor(
    private readonly api: APIRequestContext,
    private readonly auth: { Authorization: string },
  ) {}

  async document(): Promise<{ revision: number; agents: Array<{ id: string; name: string; kind: string; state: string; generation: number }> }> {
    const resp = await this.api.get("/api/supervisor/desired_state", { headers: this.auth });
    expect(resp.status(), await resp.text()).toBe(200);
    return resp.json();
  }

  async agent(name: string) {
    const found = (await this.document()).agents.find((a) => a.name === name);
    expect(found, `${name} is in the supervisor's document`).toBeDefined();
    return found!;
  }

  // The board's write lands after the click returns, so wait for the document rather than read it once.
  async expectState(name: string, state: string) {
    await expect.poll(async () => (await this.agent(name)).state).toBe(state);
  }

  // What the supervisor's enrolment of a launch=supervisor Remote Control does first: the server makes its conductor.
  async enrolRemoteControl(name: string): Promise<{ id: string }> {
    const resp = await this.api.put(`/api/supervisor/remote-control/${encodeURIComponent(name)}`, { headers: this.auth });
    expect(resp.status(), await resp.text()).toBe(200);
    return resp.json();
  }

  async report(name: string, phase: string, reason?: string) {
    const agent = await this.agent(name);
    const body: Report = { id: agent.id, generation: agent.generation, phase, reason };
    const resp = await this.api.post("/api/supervisor/desired_state/observed", { headers: this.auth, data: { agents: [body] } });
    expect(resp.status(), await resp.text()).toBe(204);
  }
}

export async function setProvisionFromBoard(api: APIRequestContext, on: boolean) {
  const resp = await api.patch("/api/settings/provision_from_board", { data: { type: "bool", bool_val: on } });
  expect(resp.status(), await resp.text()).toBe(200);
}

export async function provisionFromBoard(api: APIRequestContext): Promise<boolean> {
  const resp = await api.get("/api/settings/provision_from_board");
  expect(resp.status(), await resp.text()).toBe(200);
  return Boolean((await resp.json()).bool_val);
}
