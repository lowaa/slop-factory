// Tests for pi-extension.ts, the slop-haus extension the Remote Control host loads.
//
// The extension runs against a stand-in pi (the handful of ExtensionAPI
// calls it makes) and a stand-in slop-haus server, but through the real
// hook scripts and describe-session.py, installed the way a workspace gets
// them -- so what the server sees is what a real pi run would send.
//
//   node --test --experimental-strip-types slop-haus/server/internal/workspace/assets/ts/pi-extension.test.ts

import { after, before, beforeEach, describe, it, test, type TestContext } from "node:test";
import assert from "node:assert/strict";
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { extractCommands, MODEL, PROVIDER, SCRIPT_TIMEOUT_MS, sentByHuman, TOOL, TOOL_CALL_TIMEOUT_MS } from "./pi-extension.ts";

const here = dirname(fileURLToPath(import.meta.url));
// go-llm-usage's shared bash fixture, which every client's extractor runs.
const LABELS_FIXTURE = join(here, "..", "..", "..", "..", "..", "..", "go-llm-usage", "fixtures", "record_labels.json");
const BASH_FIXTURE = join(here, "..", "..", "..", "..", "..", "..", "go-llm-usage", "fixtures", "bash_commands.json");

const TOOLS: { name: string; description: string; inputSchema: Record<string, unknown> }[] = [
  {
    name: "checkin",
    description: "Confirm who you are.",
    inputSchema: { type: "object", properties: {}, additionalProperties: false },
  },
  {
    name: "ticket_get",
    description: "Read a ticket.",
    inputSchema: { type: "object", properties: { ticket_id: { type: "integer" } }, required: ["ticket_id"] },
  },
  // The calls that hand a ticket back.
  ...["ticket_finish", "ticket_release", "ticket_discuss", "ticket_prepare"].map((name) => ({
    name,
    description: `${name}.`,
    inputSchema: { type: "object", properties: { handoff: { type: "string" } } },
  })),
];
const TOOL_NAMES = TOOLS.map((tool) => tool.name);

// FakeServer answers /mcp the way go-sdk's streamable handler does --
// session ids, event-stream responses, 404 for a session it has forgotten
// -- and records what the hook endpoints were sent.
class FakeServer {
  server!: Server;
  url = "";
  // Access tokens it refuses, as if expired.
  rejected = new Set<string>();
  sessions = new Set<string>();
  sessionCount = 0;
  initialized = new Set<string>();
  mcpAuth: string[] = [];
  calls: { name: string; arguments: unknown }[] = [];
  sessionStarts: any[] = [];
  stops: any[] = [];
  prompts: any[] = [];
  promptWaiters: (() => void)[] = [];
  // What /hooks/stop answers: a reason to block with, or nothing.
  blockReason = "";
  down = false;
  // Usage reports received, and those still to be handed to nextUsage.
  usage: any[] = [];
  private unclaimed: any[] = [];
  private usageWaiters: ((body: any) => void)[] = [];
  usageInFlight = 0;
  maxUsageInFlight = 0;
  // Held until released, so a test can line reports up behind one.
  usageGate?: Promise<void>;

  // nextUsage resolves with the next usage report the server receives.
  nextUsage(): Promise<any> {
    const waiting = this.unclaimed.shift();
    if (waiting) return Promise.resolve(waiting);
    return new Promise((resolve) => this.usageWaiters.push(resolve));
  }

  async start() {
    this.server = createServer((req, res) => void this.handle(req, res));
    await new Promise<void>((resolve) => this.server.listen(0, "127.0.0.1", resolve));
    const address = this.server.address();
    if (!address || typeof address === "string") throw new Error("no address");
    this.url = `http://127.0.0.1:${address.port}`;
  }

  async stop() {
    this.server.closeAllConnections();
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
  }

  private async handle(req: IncomingMessage, res: ServerResponse) {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    if (this.down) {
      res.writeHead(503).end("down");
      return;
    }
    const auth = req.headers.authorization ?? "";
    if (!auth.startsWith("Bearer ") || this.rejected.has(auth.slice(7))) {
      res.writeHead(401).end("unauthorized");
      return;
    }
    const body = JSON.parse(raw);
    if (req.url === "/hooks/usage") {
      this.usageInFlight++;
      this.maxUsageInFlight = Math.max(this.maxUsageInFlight, this.usageInFlight);
      await this.usageGate;
      this.usageInFlight--;
      this.usage.push(body);
      const waiter = this.usageWaiters.shift();
      if (waiter) waiter(body);
      else this.unclaimed.push(body);
      this.json(res, { scope: "overhead" });
      return;
    }
    if (req.url === "/hooks/session-start") {
      this.sessionStarts.push(body);
      this.json(res, {
        hookSpecificOutput: { hookEventName: "SessionStart", additionalContext: "You are agent builder-1." },
        systemMessage: "slop-haus: another run looks live",
      });
      return;
    }
    if (req.url === "/hooks/user-prompt-submit") {
      this.prompts.push(body);
      for (const wake of this.promptWaiters.splice(0)) wake();
      res.writeHead(200).end();
      return;
    }
    if (req.url === "/hooks/stop") {
      this.stops.push(body);
      if (this.blockReason) {
        this.json(res, {
          hookSpecificOutput: { hookEventName: "Stop", decision: "block", reason: this.blockReason },
          continue: true,
        });
      } else {
        this.json(res, {});
      }
      return;
    }
    if (req.url !== "/mcp") {
      res.writeHead(404).end();
      return;
    }
    this.mcpAuth.push(auth.slice(7));
    assert.match(String(req.headers.accept), /application\/json/);
    assert.match(String(req.headers.accept), /text\/event-stream/);

    const sessionId = req.headers["mcp-session-id"];
    if (body.method === "initialize") {
      const id = `session-${++this.sessionCount}`;
      this.sessions.add(id);
      res.setHeader("Mcp-Session-Id", id);
      this.sse(res, { jsonrpc: "2.0", id: body.id, result: { protocolVersion: "2025-06-18", capabilities: { tools: {} } } });
      return;
    }
    if (typeof sessionId !== "string" || !this.sessions.has(sessionId)) {
      res.writeHead(404).end("session not found");
      return;
    }
    assert.equal(req.headers["mcp-protocol-version"], "2025-06-18");
    if (body.method === "notifications/initialized") {
      this.initialized.add(sessionId);
      res.writeHead(202).end();
      return;
    }
    assert.ok(this.initialized.has(sessionId), `${body.method} before notifications/initialized`);
    if (body.method === "tools/list") {
      // One tool a page, so the client has to follow the cursor.
      const at = Number(body.params?.cursor ?? 0);
      const next = at + 1 < TOOLS.length ? { nextCursor: String(at + 1) } : {};
      this.sse(res, { jsonrpc: "2.0", id: body.id, result: { tools: [TOOLS[at]], ...next } });
      return;
    }
    if (body.method === "tools/call") {
      this.calls.push(body.params);
      const failed =
        (body.params.name === "ticket_get" && body.params.arguments.ticket_id === 404) ||
        body.params.arguments.handoff === "refuse";
      this.sse(res, {
        jsonrpc: "2.0",
        id: body.id,
        result: {
          content: [{ type: "text", text: failed ? "refused" : `called ${body.params.name}\nsecond line` }],
          isError: failed,
          // ticket_finish answering "merge" leaves the ticket with the agent.
          ...(body.params.arguments.handoff === "merge" ? { structuredContent: { next: "merge" } } : {}),
        },
      });
      return;
    }
    this.sse(res, { jsonrpc: "2.0", id: body.id, error: { code: -32601, message: "method not found" } });
  }

  private json(res: ServerResponse, body: unknown) {
    res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(body));
  }

  // A notification first, as a server may send one ahead of the answer.
  private sse(res: ServerResponse, message: unknown) {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`event: message\ndata: ${JSON.stringify({ jsonrpc: "2.0", method: "notifications/message", params: {} })}\n\n`);
    res.end(`event: message\ndata: ${JSON.stringify(message)}\n\n`);
  }
}

// FakePi records what the extension registers and sends.
class FakePi {
  handlers = new Map<string, ((event: any, ctx: any) => any)[]>();
  tools = new Map<string, any>();
  commands = new Map<string, any>();
  sent: { message: any; options: any }[] = [];
  userSent: { content: any; options: any }[] = [];

  on(event: string, handler: (event: any, ctx: any) => any) {
    this.handlers.set(event, [...(this.handlers.get(event) ?? []), handler]);
  }

  // pi's own tools, as getAllTools lists them alongside registered ones.
  builtins = ["read", "bash", "edit", "write"];

  registerTool(tool: any) {
    this.tools.set(tool.name, tool);
  }

  getAllTools() {
    return [...this.builtins, ...this.tools.keys()].map((name) => ({ name }));
  }

  registerCommand(name: string, command: any) {
    this.commands.set(name, command);
  }

  sendMessage(message: any, options: any) {
    this.sent.push({ message, options });
  }

  sendUserMessage(content: any, options: any) {
    this.userSent.push({ content, options });
  }

  async emit(event: string, payload: any, ctx: any) {
    let result;
    for (const handler of this.handlers.get(event) ?? []) result = await handler(payload, ctx);
    return result;
  }
}

function makeCtx(root: string, sessionId = "pi-session-1") {
  const notices: { text: string; level: string }[] = [];
  return {
    notices,
    // Which providers pi signs in to with OAuth.
    oauth: new Set<string>(),
    get modelRegistry() {
      const oauth = this.oauth;
      return {
        find: (provider: string, id: string) => ({ provider, id }),
        isUsingOAuth: (model: { provider: string }) => oauth.has(model.provider),
      };
    },
    mode: "tui",
    hasUI: true,
    ui: { notify: (text: string, level: string) => notices.push({ text, level }) },
    cwd: root,
    model: { provider: "anthropic", id: "claude-sonnet-4-5" },
    sessionManager: { getSessionId: () => sessionId, getSessionFile: () => `/sessions/${sessionId}.jsonl` },
  };
}

// installWorkspace lays out a workspace as setup would, with a token.sh
// stand-in that hands out token-1, token-2, ... one per call.
function installWorkspace(serverURL: string): string {
  const root = mkdtempSync(join(tmpdir(), "slop-haus-pi-"));
  mkdirSync(join(root, ".slop-haus", "pi"), { recursive: true });
  copyFileSync(join(here, "pi-extension.ts"), join(root, ".slop-haus", "pi", "slop-haus.ts"));
  for (const [lang, name] of [["sh", "hook-stop.sh"], ["sh", "hook-session-start.sh"], ["sh", "hook-user-prompt.sh"], ["py", "describe-session.py"]]) {
    copyFileSync(join(here, "..", lang, name), join(root, ".slop-haus", name));
  }
  chmodSync(join(root, ".slop-haus", "hook-stop.sh"), 0o755);
  chmodSync(join(root, ".slop-haus", "hook-session-start.sh"), 0o755);
  chmodSync(join(root, ".slop-haus", "hook-user-prompt.sh"), 0o755);
  writeFileSync(join(root, ".slop-haus", "server-url"), `${serverURL}/\n`);
  const counter = join(root, "token-count");
  writeFileSync(
    join(root, ".slop-haus", "token.sh"),
    `#!/usr/bin/env bash
n=$(cat '${counter}' 2> /dev/null || echo 0)
n=$((n + 1))
echo "$n" > '${counter}'
printf 'token-%s' "$n"
`,
    { mode: 0o755 },
  );
  return root;
}

function tokenCalls(root: string): number {
  return Number(readFileSync(join(root, "token-count"), "utf8").trim());
}

async function loadExtension(root: string): Promise<FakePi> {
  const module = await import(pathToFileURL(join(root, ".slop-haus", "pi", "slop-haus.ts")).href);
  const pi = new FakePi();
  module.default(pi);
  return pi;
}

// asHostSession makes this test's pi look as a Remote Control host started it.
function asHostSession(t: TestContext) {
  process.env.SLOP_HAUS_HOST_SESSION = "1";
  t.after(() => {
    delete process.env.SLOP_HAUS_HOST_SESSION;
  });
}

describe("slop-haus pi extension", () => {
  const server = new FakeServer();
  let root = "";
  let pi: FakePi;
  let ctx: ReturnType<typeof makeCtx>;

  before(() => server.start());
  after(() => server.stop());

  beforeEach(async () => {
    Object.assign(server, {
      rejected: new Set(),
      sessions: new Set(),
      initialized: new Set(),
      mcpAuth: [],
      calls: [],
      sessionStarts: [],
      stops: [],
      prompts: [],
      promptWaiters: [],
      blockReason: "",
      down: false,
      usage: [],
      unclaimed: [],
      usageWaiters: [],
      usageInFlight: 0,
      maxUsageInFlight: 0,
      usageGate: undefined,
    });
    if (root) rmSync(root, { recursive: true, force: true });
    root = installWorkspace(server.url);
    ctx = makeCtx(root);
    pi = await loadExtension(root);
  });

  it("registers every server tool, and binds the session with what it is running", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);

    assert.deepEqual([...pi.tools.keys()], TOOL_NAMES);
    assert.deepEqual(pi.tools.get("ticket_get").parameters, TOOLS[1].inputSchema);

    assert.equal(server.sessionStarts.length, 1);
    const payload = server.sessionStarts[0];
    assert.equal(payload.session_id, "pi-session-1");
    assert.equal(payload.cwd, root);
    assert.equal(payload.slop_haus.harness, "pi");
    assert.equal(payload.slop_haus.provider, "anthropic");
    assert.equal(payload.slop_haus.model, "claude-sonnet-4-5");

    assert.deepEqual(pi.sent, [
      {
        message: { customType: "slop-haus", content: "You are agent builder-1.", display: false },
        options: { deliverAs: "nextTurn" },
      },
    ]);
    assert.deepEqual(ctx.notices, [{ text: "slop-haus: another run looks live", level: "warning" }]);
  });

  it("never lets a server tool replace one of pi's own", async () => {
    TOOLS.push({ name: "bash", description: "Not pi's bash.", inputSchema: { type: "object" } });
    try {
      await pi.emit("session_start", { reason: "startup" }, ctx);
    } finally {
      TOOLS.pop();
    }
    assert.deepEqual([...pi.tools.keys()], TOOL_NAMES);
    assert.ok(ctx.notices.some((n) => n.level === "warning" && /not registering bash/.test(n.text)));
  });

  it("forwards a tool call and returns its text, and reports a failed one as a failure", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);

    const ok = await pi.tools.get("ticket_get").execute("call-1", { ticket_id: 7 }, undefined, undefined, ctx);
    assert.deepEqual(ok.content, [{ type: "text", text: "called ticket_get\nsecond line" }]);
    assert.equal(ok.terminate, undefined, "only a call that hands the ticket back ends the run");
    assert.deepEqual(server.calls, [{ name: "ticket_get", arguments: { ticket_id: 7 } }]);

    await assert.rejects(pi.tools.get("ticket_get").execute("call-2", { ticket_id: 404 }, undefined), /refused/);
  });

  it("refreshes a refused token once and retries with the new one", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    const before = server.mcpAuth.at(-1)!;
    server.rejected.add(before);

    const result = await pi.tools.get("checkin").execute("call-1", {}, undefined);
    assert.deepEqual(result.content, [{ type: "text", text: "called checkin\nsecond line" }]);
    const after = server.mcpAuth.at(-1)!;
    assert.notEqual(after, before, "the retry carried a fresh token");
  });

  it("gives up after one retry when the fresh token is refused too", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    // Every token token.sh could hand out from here on is refused.
    for (let n = 1; n < 50; n++) server.rejected.add(`token-${n}`);
    const tokensBefore = tokenCalls(root);

    await assert.rejects(pi.tools.get("checkin").execute("call-1", {}, undefined), /HTTP 401/);
    assert.equal(tokenCalls(root) - tokensBefore, 1, "one refresh, not a loop");
  });

  it("starts a new MCP session when the server has forgotten the old one", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    server.sessions.clear();

    const result = await pi.tools.get("checkin").execute("call-1", {}, undefined);
    assert.deepEqual(result.content, [{ type: "text", text: "called checkin\nsecond line" }]);
    assert.equal(server.sessions.size, 1, "re-initialised");
  });

  it("sends the agent back to work with the server's reason when the Stop hook blocks", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    server.blockReason = "Ticket #3 is claimable. Call ticket_next to take it.";
    const earlier = { type: "custom", customType: "someone-else" };

    const result = await pi.emit("agent_before_settle", { outcome: "completed", entries: [earlier] }, ctx);

    assert.equal(result.continue, true);
    assert.deepEqual(result.entries, [
      earlier,
      { type: "custom_message", customType: "slop-haus", content: server.blockReason, display: true },
    ]);
    assert.equal(server.stops.length, 1);
    assert.equal(server.stops[0].session_id, "pi-session-1");
    assert.equal(server.stops[0].slop_haus.model, "claude-sonnet-4-5");
  });

  it("tells the server a human spoke, leaving the prompt itself alone", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    const arrived = new Promise<void>((resolve) => server.promptWaiters.push(resolve));

    const result = await pi.emit("input", { text: "check slophaus", source: "interactive" }, ctx);
    await arrived;

    assert.deepEqual(result, { action: "continue" }, "the prompt is never held, changed or handled");
    assert.equal(server.prompts.length, 1);
    assert.equal(server.prompts[0].session_id, "pi-session-1");
  });

  it("does not count the extension's own follow-ups as a human speaking", () => {
    assert.equal(sentByHuman("extension"), false);
    assert.equal(sentByHuman("interactive"), true);
    assert.equal(sentByHuman("rpc"), true);
  });

  describe("in a Remote Control host's session", () => {
    const guarded = (toolName: string, path: unknown) => pi.emit("tool_call", { toolName, input: { path } }, ctx);

    it("keeps the file tools off this workspace's credentials", async (t) => {
      asHostSession(t);
      for (const tool of ["read", "grep", "find", "ls", "edit", "write"]) {
        for (const path of [".slop-haus/credentials.json", join(root, ".slop-haus"), "@.slop-haus/token.sh", "sub/../.slop-haus.env"]) {
          const result = await guarded(tool, path);
          assert.equal(result?.block, true, `${tool} ${path}`);
          assert.match(result.reason, /credentials/);
        }
      }
    });

    it("follows a link to them", async (t) => {
      asHostSession(t);
      mkdirSync(join(root, ".slop-haus"), { recursive: true });
      writeFileSync(join(root, ".slop-haus", "credentials.json"), "{}");
      symlinkSync(join(root, ".slop-haus", "credentials.json"), join(root, "innocent.json"));
      assert.equal((await guarded("read", "innocent.json"))?.block, true);
    });

    it("follows a link to their directory for a file that does not exist yet", async (t) => {
      asHostSession(t);
      mkdirSync(join(root, ".slop-haus"), { recursive: true });
      symlinkSync(join(root, ".slop-haus"), join(root, "innocent-dir"));
      assert.equal((await guarded("write", "innocent-dir/new-secret"))?.block, true);
      assert.equal((await guarded("write", "innocent-dir/deeper/new-secret"))?.block, true);
    });

    it("follows a dangling link to a file there", async (t) => {
      asHostSession(t);
      mkdirSync(join(root, ".slop-haus"), { recursive: true });
      symlinkSync(join(root, ".slop-haus", "new-secret"), join(root, "innocent"));
      symlinkSync(".slop-haus.env", join(root, "also-innocent"));
      assert.equal((await guarded("write", "innocent"))?.block, true);
      assert.equal((await guarded("write", "also-innocent"))?.block, true);
      symlinkSync("loop", join(root, "loop"));
      assert.equal(await guarded("write", "loop"), undefined, "a link loop is not a credential, and does not hang");
    });

    it("leaves everything else alone, and other tools too", async (t) => {
      asHostSession(t);
      for (const path of ["src/main.go", ".env", ".slop-haus-notes.md", join(root, "README.md")]) {
        assert.equal(await guarded("read", path), undefined, path);
      }
      assert.equal(await guarded("bash", ".slop-haus/credentials.json"), undefined, "a shell is not something this can guard");
      assert.equal(await pi.emit("tool_call", { toolName: "read", input: {} }, ctx), undefined, "no path, no verdict");
    });

    it("does not touch a worker's own session", async () => {
      assert.equal(await guarded("read", ".slop-haus/credentials.json"), undefined);
    });
  });

  it("lets pi settle when the Stop hook allows it", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    const result = await pi.emit("agent_before_settle", { outcome: "completed", entries: [] }, ctx);
    assert.equal(result, undefined);
    assert.equal(server.stops.length, 1);
  });

  it("reports the model in use at each settle, so a switch mid-run reaches the server", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    ctx.model = { provider: "openrouter", id: "openai/gpt-5" };
    await pi.emit("agent_before_settle", { outcome: "completed", entries: [] }, ctx);
    assert.equal(server.stops[0].slop_haus.provider, "openrouter");
    assert.equal(server.stops[0].slop_haus.model, "openai/gpt-5");
  });

  // Esc is the human taking the terminal back; an error would only repeat.
  it("never sends back a turn that was aborted or errored", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    server.blockReason = "keep going";
    for (const outcome of ["aborted", "error"]) {
      assert.equal(await pi.emit("agent_before_settle", { outcome, entries: [] }, ctx), undefined);
    }
    assert.equal(server.stops.length, 0);
  });

  it("fails open: an unreachable server lets pi settle", async () => {
    await pi.emit("session_start", { reason: "startup" }, ctx);
    server.down = true;
    // The hook would ride out a restart for its full default first.
    process.env.SLOP_HAUS_STOP_RETRY_FOR = "0";
    try {
      assert.equal(await pi.emit("agent_before_settle", { outcome: "completed", entries: [] }, ctx), undefined);
    } finally {
      delete process.env.SLOP_HAUS_STOP_RETRY_FOR;
    }
  });

  it("gives a tool call the tool-call timeout and every other request the short one", async () => {
    const timeouts: number[] = [];
    const real = AbortSignal.timeout;
    AbortSignal.timeout = (ms: number) => {
      timeouts.push(ms);
      return real.call(AbortSignal, ms);
    };
    try {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      const setup = timeouts.length;
      assert.ok(setup > 0, "initialize and tools/list went through the timeout too");
      assert.ok(timeouts.every((ms) => ms === 120_000), "the short limit for everything but a tool call");

      await pi.tools.get("checkin").execute("call-1", {}, undefined);
      assert.deepEqual(timeouts.slice(setup), [2 * 60_000]);
    } finally {
      AbortSignal.timeout = real;
    }
  });

  it("asks for the tools again before the next prompt when the server was down at startup", async () => {
    server.down = true;
    await pi.emit("session_start", { reason: "startup" }, ctx);
    assert.equal(pi.tools.size, 0);
    assert.ok(ctx.notices.some((n) => n.level === "error" && /could not load its tools/.test(n.text)));

    server.down = false;
    await pi.emit("before_agent_start", {}, ctx);
    assert.deepEqual([...pi.tools.keys()], TOOL_NAMES);
  });

  it("does nothing in a directory that was never set up", async () => {
    rmSync(join(root, ".slop-haus", "server-url"));
    await pi.emit("session_start", { reason: "startup" }, ctx);
    assert.equal(pi.tools.size, 0);
    assert.equal(server.sessionStarts.length, 0);
    assert.ok(ctx.notices.some((n) => n.level === "error" && /not set up here/.test(n.text)));
    assert.equal(await pi.emit("agent_before_settle", { outcome: "completed", entries: [] }, ctx), undefined);
  });

  describe("LLM usage", () => {
    // A report that never comes fails its test rather than hanging the run.
    const it = (name: string, fn: (t: TestContext) => Promise<void>) => test(name, { timeout: 10_000 }, fn);

    // assistant is pi finishing one assistant message.
    function assistant(provider: string, model: string, usage: Record<string, unknown>) {
      return pi.emit("message_end", { message: { role: "assistant", provider, model, usage } }, ctx);
    }

    async function tool(id: string, toolName: string, args: unknown, isError = false, result: unknown = "ok", running = () => {}) {
      await pi.emit("tool_execution_start", { toolCallId: id, toolName, args }, ctx);
      running();
      await pi.emit("tool_execution_end", { toolCallId: id, toolName, result, isError }, ctx);
    }

    const opus = { input: 100, output: 40, cacheRead: 1000, cacheWrite: 200, reasoning: 10, cost: { total: 0.25 } };

    it("reports this session's totals per model when pi settles", async (t) => {
      // A clock that moves only when told, so tool durations are exact.
      let now = Date.parse("2026-10-01T09:00:00Z");
      t.mock.method(Date, "now", () => now);
      const tick = (ms: number) => () => (now += ms);
      ctx.oauth.add("anthropic");
      await pi.emit("session_start", { reason: "startup" }, ctx);
      await assistant("anthropic", "claude-opus-5-5", opus);
      await tool("t1", "bash", { command: "cd go-db && GOWORK=off go test ./... 2>&1 | tail -5" }, false, "ok ✓", tick(1500));
      const refused = { content: [{ type: "text", text: "refused" }] };
      await tool("t2", "ticket_get", { ticket_id: 1 }, true, refused, tick(20));
      await assistant("anthropic", "claude-opus-5-5", { ...opus, cost: { total: 0.5 } });
      await assistant("openrouter", "openai/gpt-5", { input: 7, output: 3, cacheRead: 0, cacheWrite: 0, cost: { total: 0.001 } });
      await pi.emit("agent_settled", {}, ctx);

      const report = await server.nextUsage();
      assert.equal(report.transcript_key, "pi-session-1");
      assert.equal(report.session_id, "pi-session-1");
      assert.equal(report.harness, "pi");
      assert.equal(report.scope, "main");
      const [claude, gpt] = report.records;
      assert.deepEqual(
        { ...claude, started_at: undefined, ended_at: undefined, tools: undefined },
        {
          schema_version: 1, provider: "anthropic", model: "claude-opus-5-5", harness: "pi",
          // pi signs in to anthropic with OAuth here: a subscription.
          billing_mode: "subscription",
          requests: 2, input_tokens: 200, output_tokens: 80, cache_read_tokens: 2000, cache_write_tokens: 400,
          reasoning_tokens: 20, reported_cost_microdollars: 750_000,
          started_at: undefined, ended_at: undefined, tools: undefined,
        },
      );
      assert.equal(claude.started_at, "2026-10-01T09:00:00.000Z");
      assert.equal(claude.ended_at, "2026-10-01T09:00:01.520Z");
      assert.deepEqual(
        claude.tools,
        [
          { tool: "bash", command: "cd", calls: 1, errors: 0, duration_ms: 0, result_bytes: 0 },
          { tool: "bash", command: "go test", calls: 1, errors: 0, duration_ms: 0, result_bytes: 0 },
          // "ok ✓" as JSON, in UTF-8 bytes: the quotes, "ok ", and three for the tick.
          { tool: "bash", command: "tail", calls: 1, errors: 0, duration_ms: 1500, result_bytes: 8 },
          { tool: "ticket_get", calls: 1, errors: 1, duration_ms: 20, result_bytes: JSON.stringify(refused).length },
        ],
        "a bash call counts each program it ran; its time and output go to the last",
      );
      assert.equal(gpt.billing_mode, "metered");
      assert.equal(gpt.requests, 1);
      assert.equal(gpt.reasoning_tokens, 0);
      assert.equal(gpt.reported_cost_microdollars, 1000);
      assert.deepEqual(gpt.tools, []);
    });

    // A command line can hold a secret; only the programs leave the machine.
    it("never sends a bash command line", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      await assistant("anthropic", "claude-opus-5-5", opus);
      const secret = "sk-live-0123456789";
      await tool("t1", "bash", { command: `curl -H 'Authorization: Bearer ${secret}' https://api.example/v1` });
      await pi.emit("agent_settled", {}, ctx);
      const report = await server.nextUsage();
      assert.doesNotMatch(JSON.stringify(report), /sk-live|Authorization|api\.example/);
      assert.deepEqual(report.records[0].tools.map((t: any) => t.command), ["curl"]);
    });

    it("sends the same totals again when nothing has changed", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      await assistant("anthropic", "claude-opus-5-5", opus);
      await pi.emit("agent_settled", {}, ctx);
      const first = await server.nextUsage();
      await pi.emit("agent_settled", {}, ctx);
      assert.deepEqual(await server.nextUsage(), first);
    });

    it("sends nothing before the model has been called", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      await pi.emit("agent_settled", {}, ctx);
      await pi.emit("session_shutdown", { reason: "quit" }, ctx);
      assert.deepEqual(server.usage, []);
    });

    it("leaves the usage of a session a Remote Control host started to the host", async (t) => {
      asHostSession(t);
      await pi.emit("session_start", { reason: "startup" }, ctx);
      await assistant("anthropic", "claude-opus-5-5", opus);
      await pi.emit("agent_settled", {}, ctx);
      await pi.emit("session_shutdown", { reason: "quit" }, ctx);
      assert.deepEqual(server.usage, [], "the host posts it with the conversation's id; a second copy would be overhead");
    });

    // Reports go one at a time; those that pile up behind one are replaced
    // by the latest, since each is the whole of the totals.
    it("sends one report at a time, and only the latest of those waiting", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      let release!: () => void;
      server.usageGate = new Promise((resolve) => (release = resolve));
      await assistant("anthropic", "claude-opus-5-5", opus);
      await pi.emit("agent_settled", {}, ctx);
      for (let n = 0; n < 3; n++) {
        await assistant("anthropic", "claude-opus-5-5", opus);
        await pi.emit("agent_settled", {}, ctx);
      }
      release();
      assert.equal((await server.nextUsage()).records[0].requests, 1);
      assert.equal((await server.nextUsage()).records[0].requests, 4);
      await pi.emit("session_shutdown", { reason: "quit" }, ctx);
      assert.equal(server.usage.length, 3, "the shutdown sent the same totals once more, and nothing else waited");
      assert.equal(server.maxUsageInFlight, 1);
    });

    it("sends the last totals when the session ends", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      await assistant("anthropic", "claude-opus-5-5", opus);
      await pi.emit("session_shutdown", { reason: "quit" }, ctx);
      assert.equal(server.usage.length, 1, "sent before the shutdown handler returned");
      assert.equal(server.usage[0].records[0].requests, 1);
    });

    it("fails open: an unreachable server never holds pi up, and says so once", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      server.down = true;
      await assistant("anthropic", "claude-opus-5-5", opus);
      await pi.emit("agent_settled", {}, ctx);
      await pi.emit("session_shutdown", { reason: "quit" }, ctx);
      await pi.emit("session_shutdown", { reason: "quit" }, ctx);
      const warnings = ctx.notices.filter((n) => /could not report LLM usage/.test(n.text));
      assert.equal(warnings.length, 1);
      assert.match(warnings[0].text, /HTTP 503/);
    });

    it("refreshes a refused token once", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      server.rejected.add(server.mcpAuth.at(-1)!);
      await assistant("anthropic", "claude-opus-5-5", opus);
      await pi.emit("agent_settled", {}, ctx);
      assert.equal((await server.nextUsage()).records[0].requests, 1);
    });

    it("leaves out what the server would refuse", async () => {
      await pi.emit("session_start", { reason: "startup" }, ctx);
      await assistant("Not A Provider", "m", opus);
      await assistant("anthropic", "claude-opus-5-5", { input: 5, output: 2, reasoning: 9, cost: { total: Number.NaN } });
      await tool("t1", "weird tool name!", {});
      await pi.emit("agent_settled", {}, ctx);
      const [record, ...rest] = (await server.nextUsage()).records;
      assert.deepEqual(rest, []);
      assert.equal(record.reasoning_tokens, 2, "never more reasoning than output");
      assert.equal(record.reported_cost_microdollars, undefined);
      assert.deepEqual(record.tools.map((t: any) => t.tool), ["other"]);
    });
  });
});

// The patterns the server refuses a whole report over, as go-llm-usage's fixture has them.
describe("record labels", () => {
  const labels = JSON.parse(readFileSync(LABELS_FIXTURE, "utf8"));
  for (const [label, pattern] of [["provider", PROVIDER], ["model", MODEL], ["tool", TOOL]] as const) {
    it(`${label} pattern matches the server's`, () => {
      for (const value of labels[label].valid) assert.ok(pattern.test(value), `${label} ${JSON.stringify(value)} is valid`);
      for (const value of labels[label].invalid) assert.ok(!pattern.test(value), `${label} ${JSON.stringify(value)} is invalid`);
    });
  }
});

describe("bash command extraction", () => {
  const fixture = JSON.parse(readFileSync(BASH_FIXTURE, "utf8"));

  for (const c of fixture.cases) {
    it(c.name, () => {
      const got = extractCommands(c.command_line);
      assert.deepEqual(got, c.expected);
      for (const command of got) assert.match(command, /^[a-z0-9._-]{1,32}( [a-z0-9._-]{1,32})?$/);
    });
  }

  // The extractor's own copy of the allowlist must be the fixture's (and
  // go-llm-usage's, which its Go test checks against the fixture).
  it("knows the fixture's subcommand programs, with their value options", () => {
    for (const [program, { value_options }] of Object.entries<{ value_options: string[] }>(fixture.subcommand_programs)) {
      const [first, ...more] = value_options;
      assert.deepEqual(extractCommands(`${program} ${first} value sub`), [`${program} sub`], program);
      for (const option of more) assert.deepEqual(extractCommands(`${program} ${option} v sub`), [`${program} sub`], program);
    }
    assert.deepEqual(extractCommands("make -C dir test"), ["make"], "others take no subcommand");
  });

  // The fixture can't hold the empty line (it needs a program in every case).
  // Programs named like Object's own properties are programs, not lookups.
  for (const [name, line, expected] of [
    ["empty line", "", []],
    ["prototype-ish program", "constructor x && __proto__ y && toString z", ["constructor", "__proto__", "other"]],
  ] as [string, string, string[]][]) {
    it(name, () => assert.deepEqual(extractCommands(line), expected));
  }
});


describe("contracts manifest", () => {
  const contracts = join(here, "..", "..", "..", "..", "..", "contracts");
  const manifest = (name: string) => JSON.parse(readFileSync(join(contracts, name), "utf8"));

  it("holds the extension's timeouts", () => {
    const timeouts = manifest("timeouts.json");
    assert.equal(TOOL_CALL_TIMEOUT_MS, timeouts.tool_call_ms.value);
    assert.equal(SCRIPT_TIMEOUT_MS, timeouts.token_and_stop_script_s.value * 1000);
  });

  it("only names tools a console is offered", () => {
    const console_ = new Set<string>(manifest("tools.json").roles.console);
    for (const name of TOOL_NAMES) assert.ok(console_.has(name), `${name} is not in the console role`);
  });

  it("only uses env vars the registry lists", () => {
    const registry = manifest("env.json").env;
    const source = readFileSync(join(here, "pi-extension.ts"), "utf8");
    const used = new Set([...source.matchAll(/(?<![A-Za-z0-9_])(SLOP_HAUS_[A-Z][A-Z_]*[A-Z])\b/g)].map((m) => m[1]));
    assert.ok(used.size > 0);
    for (const name of used) assert.ok(name in registry, `${name} is not in contracts/env.json`);
  });
});
