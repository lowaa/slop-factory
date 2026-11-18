// A minimal MCP client over the streamable HTTP transport -- just enough to
// call a tool the way an agent's Claude Code does: initialize a session,
// confirm it, then send tools/call on it. The server's Go tests drive the
// tools through the SDK's own client; this proves the same calls work
// through the deployed stack, with a real OAuth access token.
import type { APIRequestContext } from "@playwright/test";

const PROTOCOL_VERSION = "2025-06-18";

export interface ToolResult {
  isError: boolean;
  text: string;
}

// A streamable HTTP response is either plain JSON or a server-sent event
// stream carrying the JSON-RPC message as an event's data.
function parseMessage(contentType: string, body: string): { result?: unknown; error?: unknown } {
  if (!contentType.includes("text/event-stream")) return JSON.parse(body);
  const data = body
    .split("\n")
    .filter((line) => line.startsWith("data:"))
    .map((line) => line.slice("data:".length).trim())
    .join("");
  return JSON.parse(data);
}

export class McpSession {
  private nextId = 1;

  private constructor(
    private readonly request: APIRequestContext,
    private readonly headers: Record<string, string>,
  ) {}

  static async open(request: APIRequestContext, accessToken: string): Promise<McpSession> {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${accessToken}`,
      Accept: "application/json, text/event-stream",
      "Content-Type": "application/json",
    };
    const init = await request.post("/mcp", {
      headers,
      data: {
        jsonrpc: "2.0",
        id: 0,
        method: "initialize",
        params: { protocolVersion: PROTOCOL_VERSION, capabilities: {}, clientInfo: { name: "e2e", version: "0" } },
      },
    });
    if (!init.ok()) throw new Error(`mcp initialize failed: ${init.status()} ${await init.text()}`);
    const sessionId = init.headers()["mcp-session-id"];
    if (!sessionId) throw new Error("mcp initialize returned no session id");

    const session = new McpSession(request, {
      ...headers,
      "Mcp-Session-Id": sessionId,
      "Mcp-Protocol-Version": PROTOCOL_VERSION,
    });
    const initialized = await request.post("/mcp", {
      headers: session.headers,
      data: { jsonrpc: "2.0", method: "notifications/initialized" },
    });
    if (!initialized.ok()) throw new Error(`mcp initialized failed: ${initialized.status()}`);
    return session;
  }

  async callTool(name: string, args: Record<string, unknown> = {}): Promise<ToolResult> {
    const resp = await this.request.post("/mcp", {
      headers: this.headers,
      data: { jsonrpc: "2.0", id: this.nextId++, method: "tools/call", params: { name, arguments: args } },
    });
    if (!resp.ok()) throw new Error(`mcp ${name} failed: ${resp.status()} ${await resp.text()}`);
    const message = parseMessage(resp.headers()["content-type"] ?? "", await resp.text());
    if (message.error) throw new Error(`mcp ${name} returned a protocol error: ${JSON.stringify(message.error)}`);
    const result = message.result as { isError?: boolean; content?: { type: string; text?: string }[] };
    return {
      isError: result.isError ?? false,
      text: (result.content ?? []).map((c) => c.text ?? "").join(""),
    };
  }
}
