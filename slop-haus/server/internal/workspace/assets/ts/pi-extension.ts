// slop-haus for pi (https://pi.dev).
//
// pi has no MCP client and no hooks of its own, so this extension stands in
// for both of the things Claude Code gets from .mcp.json and
// .claude/settings.json:
//
// - Tools. A small MCP client asks the server for its tools (tools/list)
//   and registers each with pi under the same name, forwarding calls
//   (tools/call). The server stays the one source of tool definitions, so
//   nothing here needs changing when a tool does.
// - The session binding and the keep-working loop. The same hook scripts
//   Claude Code runs, fed the same payloads: hook-session-start.sh when a
//   pi session starts, and hook-stop.sh whenever pi is about to settle,
//   which sends the agent back to work with the server's reason exactly as
//   a Claude Code Stop hook exiting 2 does. Every decision -- fail open,
//   fail closed, the loop breaker -- stays on the server.
//
// Credentials only ever come from .slop-haus/token.sh, as they do for
// Claude Code's headersHelper and the hooks; this file never reads the
// credentials file itself.
//
// the Remote Control host loads this with -e. Installed into a workspace by
// setup and replaced by every update, like the hook scripts it drives.

import type {
  ExtensionAPI,
  ExtensionContext,
} from "@earendil-works/pi-coding-agent";
import { spawn } from "node:child_process";
import { lstatSync, readFileSync, readlinkSync, realpathSync } from "node:fs";
import { homedir } from "node:os";
import { basename, dirname, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

// The newest revision this client speaks; the server answers with the one
// it will use, and every later request says which that was.
const MCP_PROTOCOL_VERSION = "2025-06-18";
const REQUEST_TIMEOUT_MS = 120_000;
// The same as workspace.ToolCallTimeout (contracts/timeouts.json holds them to one value), which sets Claude Code's .mcp.json
// timeout and the server's write deadline.
export const TOOL_CALL_TIMEOUT_MS = 2 * 60_000;
// token.sh waits up to 30s for another of this workspace's processes to
// finish refreshing, and hook-stop.sh then rides out a restarting server
// for up to about 45s. The same as the Stop hook timeout Claude Code gets.
export const SCRIPT_TIMEOUT_MS = 90_000;
const MESSAGE_TYPE = "slop-haus";

interface ScriptResult {
  code: number;
  stdout: string;
  stderr: string;
}

// runScript runs one of the workspace's scripts, never through a shell.
// A script that cannot start, or runs out of time, reports code -1: every
// caller treats that as "no answer", which for the hooks means fail open.
function runScript(
  file: string,
  args: string[],
  options: { input?: string; env: NodeJS.ProcessEnv },
): Promise<ScriptResult> {
  return new Promise((resolve) => {
    let stdout = "";
    let stderr = "";
    let settled = false;
    const finish = (code: number) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ code, stdout, stderr });
    };
    // In a process group of its own, so a timeout takes down what the
    // script started too -- curl, python3, token.sh -- not just the bash
    // running it.
    const child = spawn(file, args, { env: options.env, stdio: ["pipe", "pipe", "pipe"], detached: true });
    const timer = setTimeout(() => {
      try {
        if (child.pid) process.kill(-child.pid, "SIGTERM");
      } catch {
        // Already gone.
      }
      finish(-1);
    }, SCRIPT_TIMEOUT_MS);
    child.stdout.setEncoding("utf8").on("data", (chunk: string) => (stdout += chunk));
    child.stderr.setEncoding("utf8").on("data", (chunk: string) => (stderr += chunk));
    child.on("error", (err) => {
      stderr += String(err);
      finish(-1);
    });
    child.on("close", (code) => finish(code ?? -1));
    // A script that exits without reading its input closes the pipe under
    // us; that is its business, not an error here.
    child.stdin.on("error", () => {});
    child.stdin.end(options.input ?? "");
  });
}

// Workspace is where this extension is installed: .slop-haus/pi/ in the
// workspace root, which is how it finds the scripts beside it no matter
// where pi was started.
class Workspace {
  readonly root: string;
  readonly server: string;

  constructor(extensionFile: string) {
    this.root = join(dirname(extensionFile), "..", "..");
    // The same file token.sh and update.sh read. Not the
    // environment: run_remote_control.sh clears SLOP_HAUS_URL, so this workspace's own
    // configuration always wins over whatever a shell had set.
    const line = readFileSync(this.script("server-url"), "utf8").split("\n")[0].trim();
    if (!line) throw new Error("slop-haus: .slop-haus/server-url is empty -- re-run setup");
    this.server = line.replace(/\/+$/, "");
  }

  script(name: string): string {
    return join(this.root, ".slop-haus", name);
  }
}

// TokenSource hands out this workspace's access token, from token.sh, and
// keeps it until the server refuses it. One fetch at a time: parallel tool
// calls share it rather than each starting token.sh.
class TokenSource {
  private current?: Promise<string>;
  private readonly workspace: Workspace;

  constructor(workspace: Workspace) {
    this.workspace = workspace;
  }

  get(): Promise<string> {
    if (!this.current) {
      const pending = this.fetch().catch((err) => {
        if (this.current === pending) this.current = undefined;
        throw err;
      });
      this.current = pending;
    }
    return this.current;
  }

  // refresh drops the token a 401 was for, unless another caller already
  // replaced it -- so two calls refused together refresh once.
  refresh(refused: Promise<string>): Promise<string> {
    if (this.current === refused) this.current = undefined;
    return this.get();
  }

  private async fetch(): Promise<string> {
    // As the hooks do: the server this client talks to is the one token.sh
    // has to resolve too, or the two could disagree.
    const result = await runScript(this.workspace.script("token.sh"), [], {
      env: { ...process.env, SLOP_HAUS_URL: this.workspace.server },
    });
    const token = result.stdout.trim();
    if (result.code !== 0 || !token) {
      throw new Error(`slop-haus: no valid token (${result.stderr.trim() || "token.sh failed"}) -- run .slop-haus/token.sh login`);
    }
    return token;
  }
}

interface JsonRpcResponse {
  id?: number | string;
  result?: any;
  error?: { code: number; message: string };
}

export interface McpTool {
  name: string;
  description?: string;
  inputSchema?: Record<string, unknown>;
}

interface CallToolResult {
  content?: { type: string; text?: string }[];
  isError?: boolean;
}

// SessionGone is the server no longer knowing our MCP session -- it
// restarted, say. The answer is a fresh initialize, not an error.
class SessionGone extends Error {
  constructor() {
    super("slop-haus: the server no longer knows this MCP session");
  }
}

// McpClient speaks MCP's streamable HTTP transport to the server's /mcp:
// JSON-RPC over POST, answered as JSON or as a server-sent event stream,
// with the session id the server hands out on initialize echoed on every
// later request.
export class McpClient {
  private session?: Promise<string | undefined>;
  private protocolVersion = MCP_PROTOCOL_VERSION;
  private nextId = 1;
  private readonly endpoint: string;
  private readonly tokens: TokenSource;

  constructor(endpoint: string, tokens: TokenSource) {
    this.endpoint = endpoint;
    this.tokens = tokens;
  }

  async listTools(signal?: AbortSignal): Promise<McpTool[]> {
    const tools: McpTool[] = [];
    let cursor: string | undefined;
    do {
      const result = await this.request("tools/list", cursor ? { cursor } : {}, signal);
      tools.push(...((result?.tools ?? []) as McpTool[]));
      cursor = typeof result?.nextCursor === "string" && result.nextCursor ? result.nextCursor : undefined;
    } while (cursor);
    return tools;
  }

  callTool(name: string, args: unknown, signal?: AbortSignal): Promise<CallToolResult> {
    return this.request("tools/call", { name, arguments: args ?? {} }, signal, TOOL_CALL_TIMEOUT_MS);
  }

  private async request(
    method: string,
    params: unknown,
    signal?: AbortSignal,
    timeoutMs = REQUEST_TIMEOUT_MS,
  ): Promise<any> {
    const session = this.ensureSession(signal);
    try {
      return await this.call(method, params, await session, signal, timeoutMs);
    } catch (err) {
      if (!(err instanceof SessionGone)) throw err;
      if (this.session === session) this.session = undefined;
      return this.call(method, params, await this.ensureSession(signal), signal, timeoutMs);
    }
  }

  private ensureSession(signal?: AbortSignal): Promise<string | undefined> {
    if (!this.session) {
      const pending = this.initialize(signal).catch((err) => {
        if (this.session === pending) this.session = undefined;
        throw err;
      });
      this.session = pending;
    }
    return this.session;
  }

  private async initialize(signal?: AbortSignal): Promise<string | undefined> {
    const { response, sessionId } = await this.post(
      {
        jsonrpc: "2.0",
        id: this.nextId++,
        method: "initialize",
        params: {
          protocolVersion: MCP_PROTOCOL_VERSION,
          capabilities: {},
          clientInfo: { name: "slop-haus-pi", version: "1" },
        },
      },
      undefined,
      signal,
    );
    const version = response?.result?.protocolVersion;
    if (typeof version === "string" && version) this.protocolVersion = version;
    await this.post({ jsonrpc: "2.0", method: "notifications/initialized" }, sessionId, signal);
    return sessionId;
  }

  private async call(
    method: string,
    params: unknown,
    sessionId: string | undefined,
    signal?: AbortSignal,
    timeoutMs = REQUEST_TIMEOUT_MS,
  ) {
    const { response } = await this.post(
      { jsonrpc: "2.0", id: this.nextId++, method, params },
      sessionId,
      signal,
      timeoutMs,
    );
    if (response?.error) throw new Error(`slop-haus: ${method}: ${response.error.message}`);
    return response?.result;
  }

  // post sends one JSON-RPC message. A 401 gets one retry with a freshly
  // refreshed token -- access tokens are short-lived, and the refresh is
  // token.sh's -- and a 404 on an established session means the server has
  // forgotten it.
  private async post(
    message: { id?: number; method: string; [key: string]: unknown },
    sessionId: string | undefined,
    signal?: AbortSignal,
    timeoutMs = REQUEST_TIMEOUT_MS,
  ): Promise<{ response?: JsonRpcResponse; sessionId?: string }> {
    for (let attempt = 0; ; attempt++) {
      const token = this.tokens.get();
      const headers: Record<string, string> = {
        "Content-Type": "application/json",
        Accept: "application/json, text/event-stream",
        Authorization: `Bearer ${await token}`,
      };
      if (sessionId) {
        headers["Mcp-Session-Id"] = sessionId;
        headers["MCP-Protocol-Version"] = this.protocolVersion;
      }
      const timeout = AbortSignal.timeout(timeoutMs);
      const res = await fetch(this.endpoint, {
        method: "POST",
        headers,
        body: JSON.stringify(message),
        signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
      });
      if (res.status === 401 && attempt === 0) {
        await res.body?.cancel();
        await this.tokens.refresh(token);
        continue;
      }
      if (res.status === 404 && sessionId) {
        await res.body?.cancel();
        throw new SessionGone();
      }
      const body = await res.text();
      if (!res.ok) {
        throw new Error(`slop-haus: ${message.method}: HTTP ${res.status}: ${body.slice(0, 500)}`);
      }
      const newSessionId = res.headers.get("mcp-session-id") ?? sessionId;
      if (message.id === undefined) return { sessionId: newSessionId };
      return { response: parseResponse(res.headers.get("content-type") ?? "", body, message.id), sessionId: newSessionId };
    }
  }
}

// parseResponse finds the answer to request id in a response body: plain
// JSON, or a server-sent event stream whose events may also carry
// notifications before it.
export function parseResponse(contentType: string, body: string, id: number): JsonRpcResponse {
  if (!contentType.includes("text/event-stream")) return JSON.parse(body);
  for (const event of body.split(/\r?\n\r?\n/)) {
    const data = event
      .split(/\r?\n/)
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice(5).replace(/^ /, ""))
      .join("\n");
    if (!data) continue;
    const parsed = JSON.parse(data) as JsonRpcResponse;
    if (parsed.id === id) return parsed;
  }
  throw new Error(`slop-haus: no response to request ${id} in the event stream`);
}

function resultText(result: CallToolResult): string {
  return (result.content ?? [])
    .map((part) => (part.type === "text" && typeof part.text === "string" ? part.text : JSON.stringify(part)))
    .join("\n");
}

// -- LLM usage --
//
// What this session's model calls cost and what its tools did, reported to
// the server's /hooks/usage as full totals: each report replaces the last
// for the same transcript key, so sending one twice, or losing one, costs
// nothing the next won't put right. Reporting never holds the agent up:
// a failure is noted once and dropped.

// Programs whose first argument the server counts as a subcommand, with the
// options that may come before it and take a value. go-llm-usage's
// SubcommandPrograms; the shared fixture repeats it, and the tests keep the
// three in step.
const SUBCOMMAND_PROGRAMS: Record<string, string[]> = {
  docker: ["--context", "-H", "--host"],
  gh: ["-R", "--repo"],
  git: ["-C", "-c"],
  go: ["-C"],
  helm: ["-n", "--namespace", "--kube-context"],
  kubectl: ["-n", "--namespace", "--context", "--kubeconfig"],
  npm: ["--prefix"],
  uv: ["--directory", "--project"],
};
const WORD = /^[a-z0-9._-]{1,32}$/;
const ASSIGNMENT = /^[A-Za-z_][A-Za-z0-9_]*\+?=/;
// The word before an array assignment's `(`: NAME= or NAME+=.
const ARRAY_ASSIGNMENT = /^[A-Za-z_][A-Za-z0-9_]*\+?=$/;
const LEADING_TOKENS = new Set(["(", "{", "!", "if", "then", "else", "elif", "while", "until", "do"]);
const CLOSING_TOKENS = new Set([")", "}", "fi", "done", "esac"]);
const COMPOUND_STARTS = new Set(["for", "case", "function"]);
// Wrapper -> its options that take a separate value.
const WRAPPERS: Record<string, string[]> = {
  sudo: ["-u", "-g"],
  env: ["-u"],
  timeout: ["-s", "-k"],
  nohup: [],
  time: [],
  exec: [],
  command: [],
};

// terminator is what ended a simple command. Only a case clause's own
// terminator matters to what follows: it puts the case statement back to
// expecting a pattern.
type Terminator = "list" | "clause"; // list: newline, `;`, `&`, `&&`, `||`, `|` or the end of the line; clause: `;;`, `;&` or `;;&`

interface SimpleCommand {
  words: string[];
  end: Terminator;
}

// skipQuoted returns the index of the quote that closes the one at i, or
// the last index when there is none. Backslash escapes count only in double
// quotes, unless escapes is set (ANSI-C $'...', backticks).
function skipQuoted(line: string, i: number, escapes: boolean): number {
  const quote = line[i];
  escapes ||= quote === '"';
  for (i++; i < line.length && line[i] !== quote; i++) {
    if (escapes && line[i] === "\\") i++;
  }
  return Math.min(i, line.length - 1);
}

// skipBalanced returns the index of the `)` that closes the `(` at i, or the
// last index when there is none. Quotes and backticks hide parentheses.
function skipBalanced(line: string, i: number): number {
  let depth = 0;
  for (; i < line.length; i++) {
    const c = line[i];
    if (c === "(") depth++;
    else if (c === ")" && --depth === 0) return i;
    else if (c === "'" || c === '"') i = skipQuoted(line, i, false);
    else if (c === "`") i = skipQuoted(line, i, true);
    else if (c === "\\") i++;
  }
  return line.length - 1;
}

const isBlank = (c: string | undefined) => c === " " || c === "\t" || c === "\n";

// skipTest returns the index of the last `]` of the `]]` word that closes the
// `[[` at i, or the last index when there is none.
function skipTest(line: string, i: number): number {
  for (i += 2; i < line.length; i++) {
    const c = line[i];
    if (c === "'" || c === '"') i = skipQuoted(line, i, false);
    else if (c === "`") i = skipQuoted(line, i, true);
    else if (c === "\\") i++;
    else if (c === "]" && line.startsWith("]]", i) && isBlank(line[i - 1]) && (i + 2 === line.length || " \t\n;&|)".includes(line[i + 2]))) {
      return i + 1;
    }
  }
  return line.length - 1;
}

// readHeredocDelimiter reads the word after `<<` (or `<<-`) starting at i,
// unquoted, and returns it with the index of its last character.
function readHeredocDelimiter(line: string, i: number): { delimiter: string; end: number } {
  while (line[i] === " " || line[i] === "\t") i++;
  let delimiter = "";
  for (; i < line.length && !" \t\n;|&<>()".includes(line[i]); i++) {
    if (line[i] === "'" || line[i] === '"') {
      const close = line.indexOf(line[i], i + 1);
      const end = close < 0 ? line.length : close;
      delimiter += line.slice(i + 1, end);
      i = end;
    } else if (line[i] === "\\") {
      if (i + 1 < line.length) delimiter += line[++i];
    } else {
      delimiter += line[i];
    }
  }
  return { delimiter, end: i - 1 };
}

interface Heredoc {
  delimiter: string;
  stripTabs: boolean; // <<- strips leading tabs from body lines, delimiter included
}

// skipHeredocBodies returns the index of the newline that ends the last
// here-document body opened on the line ending at the newline at i.
function skipHeredocBodies(line: string, i: number, heredocs: Heredoc[]): number {
  for (const { delimiter, stripTabs } of heredocs) {
    while (i < line.length) {
      const newline = line.indexOf("\n", i + 1);
      const end = newline < 0 ? line.length : newline;
      const body = line.slice(i + 1, end);
      i = end;
      if ((stripTabs ? body.replace(/^\t+/, "") : body) === delimiter) break;
    }
  }
  return i;
}

// onlyLeadingTokens reports whether a command so far is no more than
// keywords, so the next word is in command position.
const onlyLeadingTokens = (words: string[]) => words.every((w) => LEADING_TOKENS.has(w));

// splitCommands tokenises a command line into simple commands. Quotes are
// stripped; a command substitution stays raw inside its word, so what it
// runs is never counted. Shell data -- comments, here-document bodies,
// arithmetic, [[ tests ]], array values -- is skipped whole: read as
// commands, it would be sent as command names, whatever it held.
function splitCommands(line: string): SimpleCommand[] {
  const commands: SimpleCommand[] = [];
  let words: string[] = [];
  let word = "";
  let inWord = false; // tells an empty quoted word ('') from no word
  const add = (s: string) => {
    word += s;
    inWord = true;
  };
  const endWord = () => {
    if (inWord) words.push(word);
    word = "";
    inWord = false;
  };
  const endCommand = (end: Terminator) => {
    endWord();
    // An empty clause still ends its clause.
    if (words.length > 0 || end === "clause") {
      commands.push({ words, end });
      words = [];
    }
  };
  // Here-documents opened on the current line, whose bodies start on the next.
  let heredocs: Heredoc[] = [];
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (c === " " || c === "\t") {
      endWord();
    } else if (c === "\n") {
      endCommand("list");
      i = skipHeredocBodies(line, i, heredocs);
      heredocs = [];
    } else if (c === "#" && !inWord) {
      const newline = line.indexOf("\n", i);
      i = newline < 0 ? line.length - 1 : newline - 1;
    } else if (c === "<" && line[i + 1] === "<" && line[i + 2] === "<") {
      // A here-string's word is an argument, like any other.
      add("<<<");
      i += 2;
    } else if (c === "<" && line[i + 1] === "<") {
      endWord();
      const stripTabs = line[i + 2] === "-";
      const { delimiter, end } = readHeredocDelimiter(line, i + (stripTabs ? 3 : 2));
      heredocs.push({ delimiter, stripTabs });
      i = end;
    } else if (c === ";") {
      if (line[i + 1] === ";") {
        i++;
        if (line[i + 1] === "&") i++;
        endCommand("clause");
      } else if (line[i + 1] === "&") {
        i++;
        endCommand("clause");
      } else {
        endCommand("list");
      }
    } else if (c === "|") {
      if (line[i + 1] === "|") i++;
      endCommand("list");
    } else if (c === "&") {
      // 2>&1, >&file and &>file are redirections, not separators.
      if ((i > 0 && line[i - 1] === ">") || line[i + 1] === ">") add("&");
      else endCommand("list");
    } else if (c === "(" && !inWord && line[i + 1] === "(") {
      // Arithmetic, (( ... )) or for (( ... )), runs no program.
      i = skipBalanced(line, i);
    } else if (c === "(" && inWord && ARRAY_ASSIGNMENT.test(word)) {
      const end = skipBalanced(line, i);
      add(line.slice(i, end + 1));
      i = end;
    } else if (c === "(" || c === ")") {
      // Own word so "(cd x && y)" leaves "cd" and "y" clean.
      endWord();
      words.push(c);
    } else if (c === "[" && !inWord && line[i + 1] === "[" && (i + 2 === line.length || isBlank(line[i + 2])) && onlyLeadingTokens(words)) {
      // A [[ test ]] runs no program; its && and || are not separators.
      i = skipTest(line, i);
    } else if (c === "\\") {
      if (i + 1 < line.length) {
        i++;
        if (line[i] !== "\n") add(line[i]);
      }
    } else if (c === "$" && line[i + 1] === "'") {
      const end = skipQuoted(line, i + 1, true);
      add(line.slice(i, end + 1));
      i = end;
    } else if (c === "'") {
      let end = line.indexOf("'", i + 1);
      if (end < 0) end = line.length;
      add(line.slice(i + 1, end));
      i = end;
    } else if (c === '"') {
      inWord = true;
      for (i++; i < line.length && line[i] !== '"'; i++) {
        if (line[i] === "\\" && i + 1 < line.length) i++;
        word += line[i];
      }
    } else if (c === "`") {
      let end = i + 1;
      for (; end < line.length && line[end] !== "`"; end++) if (line[end] === "\\") end++;
      end = Math.min(end, line.length - 1);
      add(line.slice(i, end + 1));
      i = end;
    } else if (c === "$" && line[i + 1] === "(") {
      const end = skipBalanced(line, i + 1);
      add(line.slice(i, end + 1));
      i = end;
    } else {
      add(c);
    }
  }
  endCommand("list");
  return commands;
}

function skipWrapper(words: string[]): string[] {
  const valueOptions = WRAPPERS[words[0]];
  const isTimeout = words[0] === "timeout";
  let rest = words.slice(1);
  while (rest.length > 0 && rest[0].startsWith("-")) {
    rest = valueOptions.includes(rest[0]) && rest.length > 1 ? rest.slice(2) : rest.slice(1);
  }
  return isTimeout && rest.length > 0 ? rest.slice(1) : rest;
}

function commandOf(words: string[]): string | undefined {
  while (words.length > 0) {
    const head = words[0];
    if (Object.hasOwn(WRAPPERS, head)) words = skipWrapper(words);
    else if (LEADING_TOKENS.has(head) || ASSIGNMENT.test(head)) words = words.slice(1);
    else break;
  }
  if (words.length === 0 || COMPOUND_STARTS.has(words[0]) || (words.length === 1 && CLOSING_TOKENS.has(words[0]))) {
    return undefined;
  }
  const program = words[0].slice(words[0].lastIndexOf("/") + 1);
  if (!WORD.test(program)) return "other";
  const valueOptions = Object.hasOwn(SUBCOMMAND_PROGRAMS, program) ? SUBCOMMAND_PROGRAMS[program] : undefined;
  if (valueOptions) {
    for (let i = 1; i < words.length; i++) {
      if (valueOptions.includes(words[i])) i++;
      else if (!words[i].startsWith("-")) return WORD.test(words[i]) ? `${program} ${words[i]}` : program;
    }
  }
  return program;
}

// A case statement's parsing: its patterns are data, and only its clauses'
// bodies run programs.
//   in:      after `case WORD`, before `in`
//   pattern: expecting a pattern, up to its `)`, or esac
//   body:    in a clause's body, until ;; ;& ;;& or esac
type CaseState = "in" | "pattern" | "body";

// extractCommands is the program, and for a few programs the subcommand,
// each simple command in a bash command line runs -- what a bash call is
// reported as. Only these leave this machine: a command line can hold a
// secret. The rules are go-llm-usage's fixtures/bash_commands.json.
export function extractCommands(line: string): string[] {
  const out: string[] = [];
  const cases: CaseState[] = []; // innermost last
  for (const command of splitCommands(line)) {
    let { words } = command;
    while (words.length > 0) {
      const top = cases.length - 1;
      if (top >= 0 && cases[top] === "in") {
        const inAt = words.indexOf("in");
        if (inAt >= 0) {
          cases[top] = "pattern";
          words = words.slice(inAt + 1);
        } else {
          words = [];
        }
        continue;
      }
      if (top >= 0 && cases[top] === "pattern") {
        const closing = words.indexOf(")");
        if (words[0] === "esac") {
          cases.pop();
          words = words.slice(1);
        } else if (closing >= 0) {
          cases[top] = "body";
          words = words.slice(closing + 1);
        } else {
          // An alternative ending in `|`, as in `a|b)`, is a pattern too.
          words = [];
        }
        continue;
      }
      const first = words.findIndex((w) => !LEADING_TOKENS.has(w));
      if (first >= 0 && words[first] === "case") {
        cases.push("in");
        // The word after `case` is its subject, not `in`.
        words = words.slice(Math.min(first + 2, words.length));
        continue;
      }
      if (top >= 0 && words[0] === "esac") {
        // The last clause needs no ;;.
        cases.pop();
        words = words.slice(1);
        continue;
      }
      const program = commandOf(words);
      if (program !== undefined) out.push(program);
      break;
    }
    const top = cases.length - 1;
    if (command.end === "clause" && top >= 0 && cases[top] === "body") cases[top] = "pattern";
  }
  return out;
}

// The server refuses a whole report for one bad field, so what doesn't fit
// its patterns is left out, or reported as "other", here first.
export const PROVIDER = /^[a-z0-9][a-z0-9._-]{0,31}$/;
export const MODEL = /^[A-Za-z0-9][A-Za-z0-9._:/@+[\]-]{0,127}$/;
export const TOOL = /^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,127}$/;
const TRANSCRIPT_KEY = /^[A-Za-z0-9._:/@+-]{1,250}$/;
// The server's per-report limits: 32 records, 512 tools a record and 1024 a
// report. Past these, a new model is dropped and a new tool counted as
// "other".
const MAX_MODELS = 32;
const MAX_TOOLS = 1000;
const MAX_TOOLS_PER_MODEL = 500;
const USAGE_TIMEOUT_MS = 10_000;

export type BillingMode = "metered" | "subscription";

interface ToolTotals {
  tool: string;
  command: string;
  calls: number;
  errors: number;
  durationMs: number;
  resultBytes: number;
}

interface ModelTotals {
  provider: string;
  model: string;
  billingMode: BillingMode;
  requests: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  reasoning: number;
  costUSD: number;
  startedAt: number;
  endedAt: number;
  tools: Map<string, ToolTotals>;
}

// The parts of pi's assistant message usage this reads.
export interface AssistantUsage {
  input?: number;
  output?: number;
  cacheRead?: number;
  cacheWrite?: number;
  reasoning?: number;
  cost?: { total?: number };
}

const count = (n: unknown) => (typeof n === "number" && Number.isFinite(n) && n > 0 ? Math.round(n) : 0);

// UsageLedger keeps one transcript's totals: per model, and per tool and
// bash command under the model whose turn ran it.
export class UsageLedger {
  private readonly models = new Map<string, ModelTotals>();
  private current?: ModelTotals;
  private toolCount = 0;

  addMessage(provider: string, model: string, billingMode: BillingMode, usage: AssistantUsage, at: number) {
    if (!PROVIDER.test(provider) || !MODEL.test(model)) return;
    const key = `${provider}\n${model}\n${billingMode}`;
    let totals = this.models.get(key);
    if (!totals) {
      if (this.models.size >= MAX_MODELS) return;
      totals = {
        provider, model, billingMode,
        requests: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, reasoning: 0, costUSD: 0,
        startedAt: at, endedAt: at, tools: new Map(),
      };
      this.models.set(key, totals);
    }
    totals.requests++;
    totals.input += count(usage.input);
    totals.output += count(usage.output);
    totals.cacheRead += count(usage.cacheRead);
    totals.cacheWrite += count(usage.cacheWrite);
    totals.reasoning += count(usage.reasoning);
    const cost = usage.cost?.total;
    if (typeof cost === "number" && Number.isFinite(cost) && cost > 0) totals.costUSD += cost;
    totals.endedAt = Math.max(totals.endedAt, at);
    // The tools that run next are this turn's.
    this.current = totals;
  }

  // addTool counts one finished tool call. A bash call counts once for each
  // program it ran; its time, output and outcome go to the last, whose exit
  // status is the call's, so the totals across commands are the call's own.
  addTool(tool: string, commands: string[], isError: boolean, durationMs: number, resultBytes: number) {
    const totals = this.current;
    if (!totals) return;
    if (!TOOL.test(tool)) tool = "other";
    const names = commands.length > 0 ? commands : [""];
    names.forEach((command, i) => {
      const entry = this.toolEntry(totals, tool, command);
      entry.calls++;
      if (i === names.length - 1) {
        if (isError) entry.errors++;
        entry.durationMs += count(durationMs);
        entry.resultBytes += count(resultBytes);
      }
    });
  }

  private toolEntry(totals: ModelTotals, tool: string, command: string): ToolTotals {
    let key = `${tool}\n${command}`;
    let entry = totals.tools.get(key);
    if (entry) return entry;
    if (this.toolCount >= MAX_TOOLS || totals.tools.size >= MAX_TOOLS_PER_MODEL) {
      [tool, command, key] = ["other", "", "other\n"];
      entry = totals.tools.get(key);
      if (entry) return entry;
    }
    entry = { tool, command, calls: 0, errors: 0, durationMs: 0, resultBytes: 0 };
    totals.tools.set(key, entry);
    this.toolCount++;
    return entry;
  }

  isEmpty(): boolean {
    return this.models.size === 0;
  }

  // records is the ledger in go-llm-usage's Record wire format.
  records(): Record<string, unknown>[] {
    return [...this.models.values()].map((m) => {
      const record: Record<string, unknown> = {
        schema_version: 1,
        provider: m.provider,
        model: m.model,
        harness: "pi",
        billing_mode: m.billingMode,
        requests: m.requests,
        input_tokens: m.input,
        output_tokens: m.output,
        cache_read_tokens: m.cacheRead,
        cache_write_tokens: m.cacheWrite,
        // Part of output, and never more than it.
        reasoning_tokens: Math.min(m.reasoning, m.output),
        started_at: new Date(m.startedAt).toISOString(),
        ended_at: new Date(m.endedAt).toISOString(),
        tools: [...m.tools.values()].map((t) => ({
          tool: t.tool,
          ...(t.command ? { command: t.command } : {}),
          calls: t.calls,
          errors: Math.min(t.errors, t.calls),
          duration_ms: t.durationMs,
          result_bytes: t.resultBytes,
        })),
      };
      // pi's own figure from its model registry; the server works out its
      // own estimate besides.
      if (m.costUSD > 0) record.reported_cost_microdollars = Math.round(m.costUSD * 1_000_000);
      return record;
    });
  }
}

export interface UsageReport {
  transcript_key: string;
  harness: "pi";
  session_id: string;
  scope: "main";
  records: Record<string, unknown>[];
}

// UsageReporter sends reports one at a time. A report waiting to go is
// replaced by a newer one for the same transcript -- both are full totals
// -- so however often it is asked, it sends the latest of each, in turn.
// One that fails is tried again with the next, unless a newer one for its
// transcript has come by then: a transcript that is finished -- a ticket
// handed back -- gets no newer report to put it right.
export class UsageReporter {
  private readonly waiting = new Map<string, UsageReport>();
  private readonly failed = new Map<string, UsageReport>();
  private draining?: Promise<void>;
  private warned = false;
  private readonly endpoint: string;
  private readonly tokens: TokenSource;
  private readonly warn: (text: string) => void;

  constructor(server: string, tokens: TokenSource, warn: (text: string) => void) {
    this.endpoint = `${server}/hooks/usage`;
    this.tokens = tokens;
    this.warn = warn;
  }

  // send queues report, and any that failed before, and returns once
  // everything queued has been tried. It never rejects.
  send(report?: UsageReport): Promise<void> {
    for (const [key, failed] of this.failed) if (!this.waiting.has(key)) this.waiting.set(key, failed);
    this.failed.clear();
    if (report) this.waiting.set(report.transcript_key, report);
    if (this.waiting.size === 0) return this.draining ?? Promise.resolve();
    this.draining ??= this.drain().finally(() => (this.draining = undefined));
    return this.draining;
  }

  private async drain() {
    for (let next = this.take(); next; next = this.take()) {
      try {
        await this.post(next);
      } catch (err) {
        if (!this.waiting.has(next.transcript_key)) this.failed.set(next.transcript_key, next);
        // Once a session: a server that is down stays down for a while, and
        // the next report carries these totals anyway.
        if (!this.warned) {
          this.warned = true;
          this.warn(`slop-haus: could not report LLM usage (${err instanceof Error ? err.message : err}); carrying on`);
        }
      }
    }
  }

  private take(): UsageReport | undefined {
    const [key, report] = this.waiting.entries().next().value ?? [];
    if (key !== undefined) this.waiting.delete(key);
    return report;
  }

  private async post(report: UsageReport) {
    for (let attempt = 0; ; attempt++) {
      const token = this.tokens.get();
      const res = await fetch(this.endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${await token}` },
        body: JSON.stringify(report),
        signal: AbortSignal.timeout(USAGE_TIMEOUT_MS),
      });
      const body = await res.text();
      if (res.status === 401 && attempt === 0) {
        await this.tokens.refresh(token);
        continue;
      }
      if (!res.ok) throw new Error(`HTTP ${res.status}: ${body.slice(0, 200)}`);
      return;
    }
  }
}

// billingModeOf is metered unless pi signs in to the provider with OAuth --
// a Claude or ChatGPT subscription -- whose cost is only an estimate.
function billingModeOf(ctx: ExtensionContext, provider: string, model: string): BillingMode {
  try {
    const found = ctx.modelRegistry.find(provider, model);
    return found && ctx.modelRegistry.isUsingOAuth(found) ? "subscription" : "metered";
  } catch {
    return "metered";
  }
}

// hostSession is whether a Remote Control host started this pi (it sets the
// variable, as it does for Claude Code's hooks). The host reports the
// conversation's usage itself, so the extension reporting it too would charge
// the same turns again as overhead. (The Stop and SessionStart hooks need no
// check here: their scripts skip a host's session themselves.)
function hostSession(): boolean {
  return !!process.env.SLOP_HAUS_HOST_SESSION;
}

// The file tools a Remote Control host's session may not point at this
// workspace's credentials, as Claude Code's Read is denied them. A guard
// against a careless read (or a prompt-injected one that names the file), not
// a boundary: a shell reads anything, and so does a session pi runs with it.
const GUARDED_TOOLS = new Set(["read", "grep", "find", "ls", "edit", "write"]);

// resolveExisting follows symlinks through the longest prefix of path that
// exists and appends the rest as written, so a file that is yet to be created
// under a link to a guarded directory is still seen as inside it. A dangling
// link is followed by hand, since a write through it creates its target.
function resolveExisting(path: string, links = 0): string {
  let existing = path;
  let rest = "";
  for (;;) {
    try {
      return join(realpathSync(existing), rest);
    } catch {
      let target: string | undefined;
      try {
        if (lstatSync(existing).isSymbolicLink()) target = resolve(dirname(existing), readlinkSync(existing));
      } catch {
        // Not there at all: look one level up.
      }
      if (target !== undefined) return links < 16 ? resolveExisting(join(target, rest), links + 1) : path;
      const parent = dirname(existing);
      if (parent === existing) return path;
      rest = rest ? join(basename(existing), rest) : basename(existing);
      existing = parent;
    }
  }
}

// isCredentialPath is whether path -- as the model wrote it, symlinks followed
// -- is in a .slop-haus directory or is a .slop-haus.env file.
export function isCredentialPath(path: string, cwd: string): boolean {
  let target = path.startsWith("@") ? path.slice(1) : path;
  if (target === "~" || target.startsWith("~/")) target = join(homedir(), target.slice(1));
  const parts = resolveExisting(resolve(cwd, target)).split(sep);
  return parts.includes(".slop-haus") || parts[parts.length - 1] === ".slop-haus.env";
}

// resultBytes is the size of what a tool handed back to the model.
function resultBytes(result: unknown): number {
  try {
    return Buffer.byteLength(JSON.stringify(result ?? ""), "utf8");
  } catch {
    return 0;
  }
}

async function harnessVersion(): Promise<string> {
  try {
    const pi = await import("@earendil-works/pi-coding-agent");
    return typeof pi.VERSION === "string" ? pi.VERSION : "";
  } catch {
    return "";
  }
}

// sentByHuman is whether pi input came from a person (the terminal, or an
// RPC client driving a session for one) rather than from an extension --
// this one's follow-ups included.
export function sentByHuman(source: string): boolean {
  return source !== "extension";
}

export default function slopHaus(pi: ExtensionAPI) {
  let workspace: Workspace | undefined;
  let client: McpClient | undefined;
  let version = "";
  // Tool names registered so far, and the registration in flight: a server
  // that was down at startup is asked again before the next prompt.
  const registered = new Set<string>();
  let registering: Promise<void> | undefined;
  // This session's LLM usage, reported under the session's id.
  let reporter: UsageReporter | undefined;
  let sessionId = "";
  let ledger = new UsageLedger();
  const toolStarts = new Map<string, { at: number; commands: string[] }>();

  function notify(ctx: ExtensionContext, text: string, level: "info" | "warning" | "error") {
    if (ctx.hasUI) ctx.ui.notify(text, level);
    else process.stderr.write(`${text}\n`);
  }

  async function registerTools(ctx: ExtensionContext) {
    if (!client) return;
    const tools = await client.listTools();
    // pi lets a later registration replace an existing tool, built-ins
    // included. The server names its own tools, and none of them should
    // ever stand in for pi's bash or edit, so a clash is refused, not
    // honoured.
    const taken = new Set(pi.getAllTools().map((tool) => tool.name));
    for (const tool of tools) {
      if (registered.has(tool.name)) continue;
      if (taken.has(tool.name)) {
        notify(ctx, `slop-haus: not registering ${tool.name}: pi already has a tool by that name`, "warning");
        continue;
      }
      const mcp = client;
      pi.registerTool({
        name: tool.name,
        label: `slop-haus ${tool.name}`,
        description: tool.description ?? tool.name,
        // The server's JSON Schema, as is: pi validates plain JSON Schema as
        // well as its own TypeBox ones.
        parameters: (tool.inputSchema ?? { type: "object", properties: {} }) as any,
        async execute(
          toolCallId: string,
          params: unknown,
          signal: AbortSignal | undefined,
          _onUpdate: unknown,
          toolCtx: ExtensionContext | undefined,
        ) {
          const result = await mcp.callTool(tool.name, params, signal);
          const text = resultText(result);
          // Throwing is how a pi tool reports failure to the model.
          if (result.isError) throw new Error(text || `${tool.name} failed`);
          const content = [{ type: "text" as const, text }];
          return { content, details: {} };
        },
      });
      registered.add(tool.name);
    }
    if (registered.size === 0) notify(ctx, "slop-haus: the server offered no tools", "warning");
  }

  function ensureTools(ctx: ExtensionContext): Promise<void> {
    if (registered.size > 0 || !client) return Promise.resolve();
    registering ??= registerTools(ctx)
      .catch((err) => notify(ctx, `slop-haus: could not load its tools: ${err instanceof Error ? err.message : err}`, "error"))
      .finally(() => (registering = undefined));
    return registering;
  }

  // hook runs one of the workspace's hook scripts with a Claude Code-shaped
  // payload, plus what this run knows about itself for describe-session.py
  // to report: the harness, pi's version, and the model in use right now --
  // which is how a model switched mid-run reaches the server, at the next
  // settle.
  function hook(name: string, payload: Record<string, unknown>, ctx: ExtensionContext): Promise<ScriptResult> {
    const ws = workspace!;
    return runScript(ws.script(name), [`-server=${ws.server}`], {
      input: JSON.stringify(payload),
      env: {
        ...process.env,
        SLOP_HAUS_HARNESS: "pi",
        SLOP_HAUS_HARNESS_VERSION: version,
        SLOP_HAUS_PROVIDER: ctx.model?.provider ?? "",
        SLOP_HAUS_MODEL: ctx.model?.id ?? "",
      },
    });
  }

  pi.on("session_start", async (event, ctx) => {
    if (!workspace) {
      try {
        workspace = new Workspace(fileURLToPath(import.meta.url));
      } catch (err) {
        notify(ctx, `slop-haus: not set up here: ${err instanceof Error ? err.message : err}`, "error");
        return;
      }
      const tokens = new TokenSource(workspace);
      client = new McpClient(`${workspace.server}/mcp`, tokens);
      reporter = new UsageReporter(workspace.server, tokens, (text) => {
        try {
          notify(ctx, text, "warning");
        } catch {
          // The session that set this up is gone; nobody to tell.
        }
      });
      version = await harnessVersion();
    }
    sessionId = ctx.sessionManager.getSessionId();
    await ensureTools(ctx);

    const result = await hook(
      "hook-session-start.sh",
      {
        hook_event_name: "SessionStart",
        session_id: ctx.sessionManager.getSessionId(),
        cwd: ctx.cwd,
        source: event.reason,
      },
      ctx,
    );
    if (result.code !== 0 || !result.stdout.trim()) return;
    let answer: any;
    try {
      answer = JSON.parse(result.stdout);
    } catch {
      return;
    }
    const briefing = answer?.hookSpecificOutput?.additionalContext;
    if (typeof briefing === "string" && briefing) {
      // With the first prompt, as Claude Code's SessionStart context would
      // be: nothing is sent to the model until the human says so.
      pi.sendMessage({ customType: MESSAGE_TYPE, content: briefing, display: false }, { deliverAs: "nextTurn" });
    }
    if (typeof answer?.systemMessage === "string" && answer.systemMessage) {
      notify(ctx, answer.systemMessage, "warning");
    }
  });

  // A human typing to the agent gives it a fresh budget of Stop blocks (the
  // server's UserPromptSubmit handler says why), as Claude Code's
  // UserPromptSubmit hook does. Only input a person sent: this extension's
  // own follow-ups arrive as "extension", and must not hand a runaway loop
  // a new budget. Not awaited, and its failure ignored: the prompt must not
  // wait on the server, and a workspace updated before this script existed
  // simply has nothing to run.
  pi.on("input", async (event, ctx) => {
    if (workspace && sentByHuman(event.source)) {
      void hook(
        "hook-user-prompt.sh",
        { hook_event_name: "UserPromptSubmit", session_id: ctx.sessionManager.getSessionId(), cwd: ctx.cwd },
        ctx,
      ).catch(() => undefined);
    }
    return { action: "continue" };
  });

  pi.on("before_agent_start", async (_event, ctx) => {
    await ensureTools(ctx);
  });

  // reportUsage sends the session's totals so far. It never rejects, and
  // nothing need wait for it.
  function reportUsage(): Promise<void> {
    if (hostSession()) return Promise.resolve();
    const key = sessionId;
    if (!reporter) return Promise.resolve();
    if (ledger.isEmpty() || !TRANSCRIPT_KEY.test(key)) return reporter.send();
    return reporter.send({ transcript_key: key, harness: "pi", session_id: sessionId, scope: "main", records: ledger.records() });
  }

  pi.on("tool_call", async (event, ctx) => {
    if (!hostSession() || !GUARDED_TOOLS.has(event.toolName)) return undefined;
    const path = (event.input as { path?: unknown } | undefined)?.path;
    if (typeof path !== "string" || !isCredentialPath(path, ctx.cwd)) return undefined;
    return { block: true, reason: "this workspace's slop-haus credentials are not for the model to read or change" };
  });

  pi.on("message_end", async (event, ctx) => {
    const message: any = event.message;
    if (message?.role !== "assistant" || !message.usage) return;
    const provider = String(message.provider ?? "");
    const model = String(message.model ?? "");
    ledger.addMessage(provider, model, billingModeOf(ctx, provider, model), message.usage, Date.now());
  });

  pi.on("tool_execution_start", async (event) => {
    const commands = event.toolName === "bash" && typeof event.args?.command === "string" ? extractCommands(event.args.command) : [];
    toolStarts.set(event.toolCallId, { at: Date.now(), commands });
  });

  pi.on("tool_execution_end", async (event) => {
    const start = toolStarts.get(event.toolCallId);
    toolStarts.delete(event.toolCallId);
    ledger.addTool(event.toolName, start?.commands ?? [], event.isError, start ? Date.now() - start.at : 0, resultBytes(event.result));
  });

  pi.on("session_shutdown", async () => {
    // pi may exit straight after: give the last totals a moment to go, but
    // never hold the quit up for a server that isn't answering.
    await Promise.race([reportUsage(), new Promise((resolve) => setTimeout(resolve, USAGE_TIMEOUT_MS).unref())]);
  });

  // stopReason asks the Stop hook whether the session may stop, and returns
  // the server's reason to keep going, or undefined to stop.
  async function stopReason(ctx: ExtensionContext): Promise<string | undefined> {
    const result = await hook(
      "hook-stop.sh",
      { hook_event_name: "Stop", session_id: ctx.sessionManager.getSessionId(), cwd: ctx.cwd },
      ctx,
    );
    if (result.code !== 2) return undefined;
    return result.stderr.trim() || "slop-haus: there is still work assigned to you.";
  }

  pi.on("agent_settled", async () => {
    // Settled, nothing is running: a call that never reported its end
    // won't now.
    toolStarts.clear();
    void reportUsage();
  });

  // The keep-working loop. pi fires this each time it is about to settle,
  // and again after every continuation it asks for, so it loops just as
  // Claude Code's Stop hook does -- bounded by the server's loop breaker.
  pi.on("agent_before_settle", async (event, ctx) => {
    // Only a turn that finished on its own. An aborted one is the human
    // pressing Esc, and sending the agent straight back would take the
    // terminal away from them; an errored one would just error again.
    if (!workspace || event.outcome !== "completed") return;
    const reason = await stopReason(ctx);
    if (reason === undefined) return;
    return {
      entries: [...event.entries, { type: "custom_message", customType: MESSAGE_TYPE, content: reason, display: true }],
      continue: true,
    };
  });
}
