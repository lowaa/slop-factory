import type { ConversationEvent, RepoStateBody } from "@/api";

// What the chat draws, folded from the stored events. The text is always
// plain: an assistant's words are the model's output and a user's the owner's,
// and neither is ever rendered as markup.
export type TranscriptItem =
  | { type: "user"; seq: number; text: string; at: string }
  // The host reports an assistant's text in batches, so one reply is several
  // events of the same turn; it is one item here, finished by the turn's end.
  | { type: "assistant"; seq: number; turn: number | null; parts: AssistantPart[]; done: boolean }
  | { type: "note"; seq: number; kind: NoteKind; text: string };

// A reply is its model messages (blocks) and the tool calls between them, in
// the order they happened. Text from before the host reported block ids has
// block null, and runs together as it always did.
export type AssistantPart =
  | { type: "text"; block: string | null; text: string }
  | { type: "activity"; calls: ActivityCall[] };

// One tool call: its name and the model's own one-line description of it.
export interface ActivityCall {
  id: string;
  tool: string;
  line: string;
  status: "running" | "done" | "failed";
}

type NoteKind = "ready" | "failed" | "stopped" | "truncated" | "repo_state" | "interrupted" | "unsupported" | "context_cleared" | "context_compacted";

// mergeEvents adds events to a transcript by seq, once each and in order, so
// the same event arriving from the history and again from the stream, or
// twice from a reconnect, shows once.
export function mergeEvents(have: ConversationEvent[], incoming: ConversationEvent[]): ConversationEvent[] {
  if (incoming.length === 0) return have;
  const bySeq = new Map(have.map((e) => [e.seq, e]));
  for (const e of incoming) bySeq.set(e.seq, e);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

export function lastSeq(events: ConversationEvent[]): number {
  return events.length === 0 ? 0 : events[events.length - 1].seq;
}

// The turn's repo state, as the host reported it: where the repo is and what
// the turn did to it. The body is the host's JSON, which is only trusted for
// the fields read here and shown as text.
function repoStateText(body: string): string {
  const unreadable = "The repo state could not be read.";
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return unreadable;
  }
  if (typeof parsed !== "object" || parsed === null) return unreadable;
  const repo = parsed as Partial<RepoStateBody>;
  const where = typeof repo.branch === "string" && repo.branch !== "" ? `On ${repo.branch}` : "On a detached head";
  const parts = [`${where} at ${shortCommit(typeof repo.head === "string" ? repo.head : "")}`];
  const commits = list(repo.commits).filter((c) => typeof c?.subject === "string");
  if (commits.length > 0) parts.push(`${plural(commits.length, "commit")} this turn (${commits.map((c) => c.subject).join("; ")})`);
  const pushes = list(repo.pushes).filter((p) => typeof p?.ref === "string");
  if (pushes.length > 0) parts.push(`pushed ${pushes.map((p) => p.ref).join(", ")}`);
  if (repo.dirty === true) parts.push(plural(count(repo.dirty_count), "uncommitted change"));
  if (count(repo.ahead) > 0) parts.push(`${count(repo.ahead)} ahead`);
  if (count(repo.behind) > 0) parts.push(`${count(repo.behind)} behind`);
  return parts.join(", ");
}

// The host's lists and counts are only trusted for their shape.
function list<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : 0;
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

// The host's activity body is JSON, trusted only for the shape read here and
// shown as text. A body that is not one is dropped.
function parseActivity(body: string): ActivityCall | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const a = parsed as Record<string, unknown>;
  if (typeof a.call_id !== "string" || typeof a.tool !== "string") return null;
  const status = a.status === "done" || a.status === "failed" ? a.status : "running";
  return { id: a.call_id, tool: a.tool, line: typeof a.line === "string" ? a.line : "", status };
}

function addText(item: Extract<TranscriptItem, { type: "assistant" }>, block: string | null, text: string) {
  const last = item.parts[item.parts.length - 1];
  if (last?.type === "text" && last.block === block) last.text += text;
  else item.parts.push({ type: "text", block, text });
}

function addActivity(item: Extract<TranscriptItem, { type: "assistant" }>, call: ActivityCall) {
  for (const part of item.parts) {
    const known = part.type === "activity" ? part.calls.find((c) => c.id === call.id) : undefined;
    if (known) {
      Object.assign(known, call);
      return;
    }
  }
  const last = item.parts[item.parts.length - 1];
  if (last?.type === "activity") last.calls.push(call);
  else item.parts.push({ type: "activity", calls: [call] });
}

function noteText(kind: NoteKind, body: string): string {
  switch (kind) {
    case "repo_state":
      return repoStateText(body);
    case "ready":
      return "Connected";
    case "failed":
      return `Could not start: ${body}`;
    case "stopped":
      // The reason is the server's own wording ("disconnected by its owner",
      // "replaced by another session").
      return body === "" ? "Disconnected" : body.charAt(0).toUpperCase() + body.slice(1);
    case "interrupted":
      return "Stopped by you.";
    case "unsupported":
      return `The Remote Control host does not support "${body}" (it is on an older version; restart it).`;
    case "context_cleared":
      return "Context cleared";
    case "context_compacted":
      return "Context compacted";
    case "truncated":
      return "The rest of the reply was not kept: this conversation is at its size limit.";
  }
}

export function shortCommit(commit: string): string {
  return commit.slice(0, 7);
}

export function buildTranscript(events: ConversationEvent[]): TranscriptItem[] {
  const items: TranscriptItem[] = [];
  for (const e of events) {
    const last = items[items.length - 1];
    switch (e.kind) {
      case "user":
        items.push({ type: "user", seq: e.seq, text: e.body, at: e.created_at });
        break;
      case "assistant":
      case "activity": {
        const call = e.kind === "activity" ? parseActivity(e.body) : null;
        if (e.kind === "activity" && call === null) break;
        let item = last;
        if (item?.type !== "assistant" || item.done || item.turn !== e.turn) {
          item = { type: "assistant", seq: e.seq, turn: e.turn, parts: [], done: false };
          items.push(item);
        }
        if (call) addActivity(item, call);
        else addText(item, e.block ?? null, e.body);
        break;
      }
      case "turn_end":
        // The host sends no turn on assistant text, so the reply is the
        // assistant text since the owner's last message, not a turn match.
        for (let i = items.length - 1; i >= 0; i--) {
          const item = items[i];
          if (item.type === "assistant") item.done = true;
          if (item.type === "user") break;
        }
        break;
      case "interrupt_requested":
        // Shown by the Stop button's own states, not as a note.
        break;
      default:
        items.push({ type: "note", seq: e.seq, kind: e.kind, text: noteText(e.kind, e.body) });
    }
  }
  return items;
}

// Whether the model is mid-reply: the last item is an assistant's that its
// turn has not ended, or the owner has spoken and nothing has answered yet.
export function isReplying(items: TranscriptItem[]): boolean {
  const last = items[items.length - 1];
  return last?.type === "user" || (last?.type === "assistant" && !last.done);
}

// What the model last did, for the indicator: the newest tool call of the
// reply in progress.
export function currentActivity(items: TranscriptItem[]): ActivityCall | null {
  const last = items[items.length - 1];
  if (last?.type !== "assistant" || last.done) return null;
  for (let i = last.parts.length - 1; i >= 0; i--) {
    const part = last.parts[i];
    if (part.type === "activity") return part.calls[part.calls.length - 1];
  }
  return null;
}

// When the turn in progress began: the owner's last message.
export function turnStartedAt(items: TranscriptItem[]): string | null {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.type === "user") return item.at;
  }
  return null;
}

// The turn a reply in progress is: the host numbers turns from 1, and a turn
// that has ended is a turn_end event, so it is the one after the last end.
// What Stop names to the server. Read from the loaded events: the first read
// always brings whole turns, so the last end is among them.
export function runningTurn(events: ConversationEvent[]): number {
  let ended = 0;
  for (const e of events) if (e.kind === "turn_end" && e.turn !== null) ended = Math.max(ended, e.turn);
  return ended + 1;
}

// Whether the owner has asked to stop the turn in progress. A request for a
// turn that has since ended names an earlier turn, so it no longer counts.
export function isStopping(events: ConversationEvent[]): boolean {
  const turn = runningTurn(events);
  return events.some((e) => e.kind === "interrupt_requested" && e.turn === turn);
}
