import * as fx from "@/test/fixtures";
import { describeOperations, type Operation, shellScript } from "./operations_harness";

const mine = fx.makeConductor({ name: "mine" });
const theirs = fx.makeConductor({ id: "55555555-5555-5555-5555-555555555555", name: "theirs", owned_by_me: false });

const entry = fx.makeConductorDirectoryEntry();

const operations: Record<string, Operation> = {
  conductorDirectory: {
    call: (c) => c.conductorDirectory(),
    method: "GET",
    url: "/api/conductors/directory",
    response: { conductors: [entry] },
    result: [entry],
  },
  archiveConductor: {
    call: (c) => c.archiveConductor("c1"),
    method: "POST",
    url: "/api/conductors/c1/archive",
    response: fx.makeConductor(),
  },
  unarchiveConductor: {
    call: (c) => c.unarchiveConductor("c1"),
    method: "POST",
    url: "/api/conductors/c1/unarchive",
    response: fx.makeConductor(),
  },
  listConductors: {
    call: (c) => c.listConductors(),
    method: "GET",
    url: "/api/conductors",
    response: { conductors: [mine], others: [theirs] },
    result: { own: [mine], others: [theirs] },
  },
  getConductorHost: {
    call: (c) => c.getConductorHost("c1"),
    method: "GET",
    url: "/api/conductors/c1/host",
    response: { online: true },
  },
  listConversations: {
    call: (c) => c.listConversations("c1"),
    method: "GET",
    url: "/api/conductors/c1/conversations",
    response: { conversations: [fx.makeConversation()] },
    result: [fx.makeConversation()],
  },
  startConversation: {
    call: (c) =>
      c.startConversation("c1", { message: "hi", clientId: "11111111-1111-1111-1111-111111111111" }),
    method: "POST",
    url: "/api/conductors/c1/conversations",
    body: { message: "hi", client_id: "11111111-1111-1111-1111-111111111111" },
    response: fx.makeConversation(),
  },
  getConversation: {
    call: (c) => c.getConversation("c1", "v1"),
    method: "GET",
    url: "/api/conductors/c1/conversations/v1",
    response: fx.makeConversationDetail(),
  },
  renameConversation: {
    call: (c) => c.renameConversation("c1", "v1", "Mine"),
    method: "PATCH",
    url: "/api/conductors/c1/conversations/v1",
    body: { title: "Mine" },
    response: fx.makeConversation({ title: "Mine" }),
  },
  deleteConversation: {
    call: (c) => c.deleteConversation("c1", "v1"),
    method: "DELETE",
    url: "/api/conductors/c1/conversations/v1",
  },
  listRecentConversationEvents: {
    call: (c) => c.listRecentConversationEvents("c1", "v1", { before: 0, turns: 10 }),
    method: "GET",
    url: "/api/conductors/c1/conversations/v1/events?before=0&turns=10",
    response: { events: [fx.makeConversationEvent({ seq: 5 })], has_more: true, next_before: 5, prompts: ["hi"] },
    result: { events: [fx.makeConversationEvent({ seq: 5 })], hasMore: true, nextBefore: 5, prompts: ["hi"] },
  },
  sendConversationMessage: {
    call: (c) => c.sendConversationMessage("c1", "v1", { text: "hi", clientId: "11111111-1111-1111-1111-111111111111" }),
    method: "POST",
    url: "/api/conductors/c1/conversations/v1/messages",
    body: { text: "hi", client_id: "11111111-1111-1111-1111-111111111111" },
    response: fx.makeConversationEvent({ seq: 5 }),
  },
  disconnectConversation: {
    call: (c) => c.disconnectConversation("c1", "v1"),
    method: "POST",
    url: "/api/conductors/c1/conversations/v1/disconnect",
    response: fx.makeConversation({ state: "stopped" }),
  },
  interruptConversation: {
    call: (c) => c.interruptConversation("c1", "v1", 3),
    method: "POST",
    url: "/api/conductors/c1/conversations/v1/interrupt",
    body: { turn: 3 },
    response: fx.makeConversation({ state: "ready" }),
  },
  clearConversation: {
    call: (c) => c.clearConversation("c1", "v1", { interrupt: true }),
    method: "POST",
    url: "/api/conductors/c1/conversations/v1/clear",
    body: { interrupt: true },
    response: fx.makeConversation({ state: "ready" }),
  },
  compactConversation: {
    call: (c) => c.compactConversation("c1", "v1"),
    method: "POST",
    url: "/api/conductors/c1/conversations/v1/compact",
    body: {},
    response: fx.makeConversation({ state: "ready" }),
  },
  setConductorDisabled: {
    call: (c) => c.setConductorDisabled("c1", true),
    method: "PATCH",
    url: "/api/conductors/c1",
    body: { disabled: true },
    response: fx.makeConductor({ disabled_at: "2026-01-02T00:00:00Z" }),
  },
  sendAgentMessage: {
    call: (c) => c.sendAgentMessage("a1", "stop"),
    method: "POST",
    url: "/api/agents/a1/messages",
    body: { body: "stop" },
    response: fx.makeAgentMessage(),
  },
  listAgentMessages: {
    call: (c) => c.listAgentMessages(),
    method: "GET",
    url: "/api/agent_messages",
    response: { messages: [fx.makeAgentMessage()] },
    result: [fx.makeAgentMessage()],
  },
  registerConductor: {
    call: (c) => c.registerConductor("mine"),
    method: "POST",
    url: "/api/conductors",
    body: { name: "mine" },
    reply: shellScript,
    result: { filename: "setup-x.sh", script: "#!/bin/sh\necho hi\n" },
  },
  rotateConductorSetupScript: {
    call: (c) => c.rotateConductorSetupScript("c1"),
    method: "POST",
    url: "/api/conductors/c1/setup_script",
    reply: shellScript,
    result: { filename: "setup-x.sh", script: "#!/bin/sh\necho hi\n" },
  },
};

// Not requests: these open an EventSource (covered in conversations_api.test.ts and event_stream.test.ts).
const notFetched = ["streamBoardChanges", "streamConversation"];

describeOperations("conversations operations", () => import("./conversations").then((m) => m.conversationsApi), operations, notFetched);
