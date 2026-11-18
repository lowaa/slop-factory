import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "@/lib/use_async_action";
import { apiClient, type ConversationDetail, type ConversationEvent } from "@/api";
import { lastSeq, mergeEvents } from "@/domain/transcript";

// Host presence changes without any transcript event, so a conversation on
// show is read again now and then.
export const DETAIL_REFRESH_MS = 15_000;

// Events that change what the header shows: whether the conversation is still
// live, and (a repo state) where the working copy stands and which scopes its
// changes overlap.
const STATE_EVENTS = new Set<ConversationEvent["kind"]>(["ready", "failed", "stopped", "turn_end", "repo_state", "context_cleared", "context_compacted"]);

// How many turns a read brings. The first read is the most recent turns only;
// the rest come as the person scrolls back.
export const PAGE_TURNS = 10;

// Where the earlier part of the transcript stands. The first read brings only
// the recent turns, so the rest is read on demand, one request at a time.
export interface OlderEvents {
  hasMore: boolean;
  // What to ask the next read with, from the last page.
  nextBefore: number;
  loading: boolean;
  error: string | null;
}

type Loaded = Extract<ConversationState, { status: "loaded" }>;

export type ConversationState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | {
      status: "loaded";
      detail: ConversationDetail;
      // A suffix of the transcript: the recent turns, those read since, and
      // what the stream brought.
      events: ConversationEvent[];
      older: OlderEvents;
      // The owner's recent prompts as the first read gave them, which the
      // loaded events may not hold, and the newest seq that read covered:
      // prompts sent after it are in the events.
      prompts: { recent: string[]; coveredSeq: number };
      // The oldest seq the first read brought: what is from there on is the
      // part the person opened to, and is always rendered whole.
      openedFromSeq: number;
    };

export interface UseConversation {
  state: ConversationState;
  // Adds an event the caller got from an action (a sent message), ahead of
  // the stream bringing it.
  addEvent: (event: ConversationEvent) => void;
  reloadDetail: () => Promise<void>;
  // Reads the next older turns. Does nothing while a read is in flight or
  // when there is nothing older; after a failure it is the retry.
  loadOlder: () => void;
}

// useConversation reads a conversation's detail and its recent turns, then
// keeps the transcript current from its stream. The stream is opened after the
// first read is in, from the last event of it, so nothing is shown twice or
// missed in between; events key on seq, so a transcript that starts part way
// is merged the same as a whole one.
export function useConversation(conductorId: string, conversationId: string): UseConversation {
  const [state, setState] = useState<ConversationState>({ status: "loading" });
  // Numbered, so a slow older read cannot put back what a newer one replaced.
  const issued = useRef(0);
  // Bumped when the conversation changes, so an older read of the previous
  // one landing late is dropped.
  const generation = useRef(0);
  const readingOlder = useRef(false);
  // Mirrors the state's older, which loadOlder reads without being re-made.
  const older = useRef<OlderEvents | null>(null);

  const reloadDetail = useCallback(async () => {
    const request = ++issued.current;
    try {
      const detail = await apiClient.getConversation(conductorId, conversationId);
      if (request !== issued.current) return;
      setState((was) => (was.status === "loaded" ? { ...was, detail } : was));
    } catch {
      // A failed refresh leaves what is shown; the first load reports its own.
    }
  }, [conductorId, conversationId]);

  const addEvent = useCallback((event: ConversationEvent) => {
    setState((was) => (was.status === "loaded" ? { ...was, events: mergeEvents(was.events, [event]) } : was));
  }, []);

  const loadOlder = useCallback(() => {
    const was = older.current;
    if (!was?.hasMore || readingOlder.current) return;
    const mine = generation.current;
    readingOlder.current = true;
    const settle = (change: (loaded: Loaded) => Loaded) => {
      setState((s) => (s.status === "loaded" ? change(s) : s));
    };
    older.current = { ...was, loading: true, error: null };
    settle((s) => ({ ...s, older: { ...s.older, loading: true, error: null } }));
    void (async () => {
      try {
        const page = await apiClient.listRecentConversationEvents(conductorId, conversationId, { before: was.nextBefore, turns: PAGE_TURNS });
        if (mine !== generation.current) return;
        const next = { hasMore: page.hasMore, nextBefore: page.nextBefore, loading: false, error: null };
        older.current = next;
        settle((s) => ({ ...s, events: mergeEvents(s.events, page.events), older: next }));
      } catch (err) {
        if (mine !== generation.current) return;
        const next = { ...was, loading: false, error: errorMessage(err, "Failed to load earlier messages.") };
        older.current = next;
        settle((s) => ({ ...s, older: next }));
      } finally {
        if (mine === generation.current) readingOlder.current = false;
      }
    })();
  }, [conductorId, conversationId]);

  useEffect(() => {
    let stopped = false;
    generation.current++;
    readingOlder.current = false;
    older.current = null;
    let closeStream: (() => void) | null = null;
    let timer: ReturnType<typeof setInterval> | null = null;

    async function load() {
      try {
        // The detail after the transcript, never before: a state event that
        // lands in between is in the transcript but is not streamed (the
        // stream starts after it), so a detail read earlier would keep
        // showing the old state until the next timed refresh.
        const first = await apiClient.listRecentConversationEvents(conductorId, conversationId, { before: 0, turns: PAGE_TURNS });
        const events = first.events;
        const detail = await apiClient.getConversation(conductorId, conversationId);
        if (stopped) return;
        const olderNow = { hasMore: first.hasMore, nextBefore: first.nextBefore, loading: false, error: null };
        older.current = olderNow;
        // Without the server's list (an older server) the loaded user
        // messages are all there is to offer.
        const recent = first.prompts ?? events.filter((e) => e.kind === "user").map((e) => e.body);
        setState({ status: "loaded", detail, events, older: olderNow, prompts: { recent, coveredSeq: lastSeq(events) }, openedFromSeq: events[0]?.seq ?? 0 });
        // Only now: a refresh landing while the first load is still reading
        // the transcript would be dropped, and the older detail would win.
        timer = setInterval(() => void reloadDetail(), DETAIL_REFRESH_MS);
        closeStream = apiClient.streamConversation(conductorId, conversationId, lastSeq(events), (event) => {
          if (stopped) return;
          setState((was) => (was.status === "loaded" ? { ...was, events: mergeEvents(was.events, [event]) } : was));
          if (STATE_EVENTS.has(event.kind)) void reloadDetail();
        });
      } catch (err) {
        if (!stopped) setState({ status: "error", message: errorMessage(err, "Failed to load the conversation.") });
      }
    }
    void load();
    return () => {
      stopped = true;
      closeStream?.();
      if (timer) clearInterval(timer);
    };
  }, [conductorId, conversationId, reloadDetail]);

  return { state, addEvent, reloadDetail, loadOlder };
}
