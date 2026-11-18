import { randomUUID } from "node:crypto";

import type { APIRequestContext, PlaywrightWorkerArgs } from "@playwright/test";

import { API_BASE_URL } from "@/support/env";

export interface WorkItem {
  seq: number;
  type: "start" | "message" | "stop";
  conversation: string;
  // A start of a session that has run before: resume its Claude session.
  resume?: boolean;
  text?: string;
}

// FakeConductorHost is a remote control's host (supervisor/src/slop_haus_supervisor/remote_control/host)
// with no model behind it: it polls and reports as the real one does, over
// the remote control's own token, but only when the test says so. Nothing runs in
// the background, so a test steps the conversation -- take the next item,
// answer it -- and what the browser shows between steps is stable.
export class FakeConductorHost {
  private readonly hostId = randomUUID();
  private acked = 0;
  private readonly hostSeq = new Map<string, number>();

  private constructor(
    private readonly request: APIRequestContext,
    private readonly token: string,
  ) {}

  static async open(playwright: PlaywrightWorkerArgs["playwright"], accessToken: string): Promise<FakeConductorHost> {
    return new FakeConductorHost(await playwright.request.newContext({ baseURL: API_BASE_URL }), accessToken);
  }

  async close(): Promise<void> {
    await this.request.dispose();
  }

  private post(path: string, data: unknown) {
    return this.request.post(path, { headers: { Authorization: `Bearer ${this.token}` }, data });
  }

  // nextItem polls until the server hands over an item, acknowledging the one
  // before it. The server holds a poll open until there is work, so this is
  // as quick as the work is, and a poll that comes back empty (its own cap)
  // is asked again.
  async nextItem(): Promise<WorkItem> {
    for (;;) {
      const resp = await this.post("/api/conductor/poll", { host_id: this.hostId, ack: this.acked, wait_seconds: 20 });
      if (!resp.ok()) throw new Error(`poll answered ${resp.status()}: ${await resp.text()}`);
      const body = (await resp.json()) as { items: WorkItem[] };
      const item = body.items.find((i) => i.seq > this.acked);
      if (item) {
        this.acked = item.seq;
        return item;
      }
    }
  }

  // acknowledge tells the server the items taken so far were handled, without
  // waiting for more: the ack otherwise only rides on the next poll.
  async acknowledge(): Promise<void> {
    const resp = await this.post("/api/conductor/poll", { host_id: this.hostId, ack: this.acked, wait_seconds: 0 });
    if (!resp.ok()) throw new Error(`poll answered ${resp.status()}: ${await resp.text()}`);
  }

  async nextItemOfType(type: WorkItem["type"]): Promise<WorkItem> {
    const item = await this.nextItem();
    if (item.type !== type) throw new Error(`expected a ${type} item, the queue gave ${JSON.stringify(item)}`);
    return item;
  }

  private async report(conversation: string, event: Record<string, unknown>): Promise<void> {
    const seq = (this.hostSeq.get(conversation) ?? 0) + 1;
    const resp = await this.post(`/api/conductor/conversations/${conversation}/events`, {
      host_id: this.hostId,
      events: [{ host_seq: seq, ...event }],
    });
    if (!resp.ok()) throw new Error(`report answered ${resp.status()}: ${await resp.text()}`);
    this.hostSeq.set(conversation, seq);
  }

  // ready answers a start: the Claude process is up.
  ready(item: WorkItem): Promise<void> {
    return this.report(item.conversation, { kind: "ready", work_seq: item.seq });
  }

  // The real host sends assistant text without a turn; only turn_end and
  // repo_state carry one.
  say(conversation: string, text: string): Promise<void> {
    return this.report(conversation, { kind: "assistant", text });
  }

  endTurn(conversation: string, turn: number): Promise<void> {
    return this.report(conversation, { kind: "turn_end", turn });
  }
}
