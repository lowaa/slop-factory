import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { apiClient, ApiError, type ConversationEvent } from "@/api";
import { makeConversationDetail, makeConversationEvent } from "@/test/fixtures";
import { NO_EVENTS, recentEventsOf } from "@/test/recent_events";
import { DETAIL_REFRESH_MS, PAGE_TURNS, useConversation } from "./use_conversation";

function ev(seq: number, kind: ConversationEvent["kind"] = "assistant") {
  return makeConversationEvent({ seq, kind, body: `e${seq}`, turn: 1 });
}

let push: (e: ConversationEvent) => void;

describe("useConversation", () => {
  beforeEach(() => {
    vi.spyOn(apiClient, "streamConversation").mockImplementation((_c, _id, _after, onEvent) => {
      push = onEvent;
      return () => {};
    });
  });
  afterEach(() => vi.useRealTimers());

  // n turns of user / assistant / turn_end, seq 1..3n.
  function chat(turns: number): ConversationEvent[] {
    return Array.from({ length: turns }, (_, i) => [
      makeConversationEvent({ seq: i * 3 + 1, kind: "user", body: `q${i + 1}`, turn: null }),
      makeConversationEvent({ seq: i * 3 + 2, kind: "assistant", body: `a${i + 1}`, turn: i + 1 }),
      makeConversationEvent({ seq: i * 3 + 3, kind: "turn_end", body: "", turn: i + 1 }),
    ]).flat();
  }

  function loadedOf(result: { current: ReturnType<typeof useConversation> }) {
    const s = result.current.state;
    if (s.status !== "loaded") throw new Error(`not loaded: ${s.status}`);
    return s;
  }

  it("opens with the most recent turns only, and streams after the newest", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    const read = vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(recentEventsOf(chat(25)));

    const { result } = renderHook(() => useConversation("c", "v"));

    await waitFor(() => expect(result.current.state.status).toBe("loaded"));
    const loaded = loadedOf(result);
    expect(loaded.events).toHaveLength(PAGE_TURNS * 3);
    expect(loaded.events[0].seq).toBe(46);
    expect(loaded.older).toMatchObject({ hasMore: true, nextBefore: 46, loading: false });
    expect(read).toHaveBeenCalledTimes(1);
    expect(read).toHaveBeenCalledWith("c", "v", { before: 0, turns: PAGE_TURNS });
    expect(apiClient.streamConversation).toHaveBeenCalledWith("c", "v", 75, expect.any(Function));
  });

  it("reads the next older turns from next_before, one read at a time, and merges them in order", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    const history = recentEventsOf(chat(25));
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const read = vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(async (c, id, range) => {
      if (range.before !== 0) await gate;
      return history(c, id, range);
    });
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    act(() => result.current.loadOlder());
    act(() => result.current.loadOlder());
    await waitFor(() => expect(loadedOf(result).older.loading).toBe(true));
    expect(read).toHaveBeenCalledTimes(2);
    expect(read).toHaveBeenLastCalledWith("c", "v", { before: 46, turns: PAGE_TURNS });

    release();
    await waitFor(() => expect(loadedOf(result).older.loading).toBe(false));
    const loaded = loadedOf(result);
    expect(loaded.events.map((e) => e.seq)).toEqual(Array.from({ length: 60 }, (_, i) => i + 16));
    expect(loaded.older).toMatchObject({ hasMore: true, nextBefore: 16 });
  });

  it("stops at the start of the session: nothing more is read once has_more is false", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    const read = vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(recentEventsOf(chat(12)));
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    act(() => result.current.loadOlder());
    await waitFor(() => expect(loadedOf(result).events).toHaveLength(36));
    expect(loadedOf(result).older.hasMore).toBe(false);
    act(() => result.current.loadOlder());

    expect(read).toHaveBeenCalledTimes(2);
  });

  it("keeps what is shown on a failed read, says so, and reads again on retry", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    const history = recentEventsOf(chat(25));
    let failing = false;
    vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(async (c, id, range) => {
      if (failing && range.before !== 0) throw new ApiError(503, "down");
      return history(c, id, range);
    });
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    failing = true;
    act(() => result.current.loadOlder());
    await waitFor(() => expect(loadedOf(result).older.error).toBe("down"));
    expect(loadedOf(result).events).toHaveLength(30);
    expect(loadedOf(result).older).toMatchObject({ hasMore: true, nextBefore: 46, loading: false });

    failing = false;
    act(() => result.current.loadOlder());
    await waitFor(() => expect(loadedOf(result).events).toHaveLength(60));
    expect(loadedOf(result).older.error).toBeNull();
  });

  it("appends the stream to a partial transcript, and an older read does not displace it", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(recentEventsOf(chat(25)));
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    act(() => push(ev(76)));
    act(() => result.current.loadOlder());
    await waitFor(() => expect(loadedOf(result).events).toHaveLength(61));

    const seqs = loadedOf(result).events.map((e) => e.seq);
    expect(seqs[0]).toBe(16);
    expect(seqs[seqs.length - 1]).toBe(76);
  });

  it("offers the server's recent prompts for the composer, and the prompts sent since", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue({
      events: chat(1),
      hasMore: true,
      nextBefore: 1,
      prompts: ["from long ago", "q1"],
    });
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    expect(loadedOf(result).prompts).toEqual({ recent: ["from long ago", "q1"], coveredSeq: 3 });
  });

  it("falls back to the loaded user messages when the server gives no prompts", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue({ ...NO_EVENTS, events: chat(2), prompts: null });
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    expect(loadedOf(result).prompts.recent).toEqual(["q1", "q2"]);
  });

  it("reports a failed first read of the transcript, and opens no stream", async () => {
    vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    vi.spyOn(apiClient, "listRecentConversationEvents").mockRejectedValue(new ApiError(503, "down"));

    const { result } = renderHook(() => useConversation("c", "v"));

    await waitFor(() => expect(result.current.state).toEqual({ status: "error", message: "down" }));
    expect(apiClient.streamConversation).not.toHaveBeenCalled();
  });

  it("reads the detail again when a state event arrives, and shows what it says", async () => {
    const get = vi
      .spyOn(apiClient, "getConversation")
      .mockResolvedValueOnce(makeConversationDetail({ state: "starting" }))
      .mockResolvedValue(makeConversationDetail({ state: "ready" }));
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue(NO_EVENTS);
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    act(() => push(ev(1, "ready")));

    await waitFor(() => expect(result.current.state.status === "loaded" && result.current.state.detail.state).toBe("ready"));
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("reads the detail after the transcript, so a state event that lands while the transcript is read is not missed", async () => {
    let ready = false;
    vi.spyOn(apiClient, "getConversation").mockImplementation(async () =>
      makeConversationDetail({ state: ready ? "ready" : "starting" }),
    );
    // The host's ready is committed while the transcript is being read, on its
    // last page: it is in the history, so the stream (which starts after it)
    // will not bring it, and a detail read before the last page misses it.
    vi.spyOn(apiClient, "listRecentConversationEvents").mockImplementation(async () => {
      ready = true;
      return { ...NO_EVENTS, events: [ev(1), ev(2, "ready")] };
    });

    const { result } = renderHook(() => useConversation("c", "v"));

    await waitFor(() => expect(result.current.state.status).toBe("loaded"));
    expect(result.current.state.status === "loaded" && result.current.state.detail.state).toBe("ready");
  });

  it("reads the detail again when a repo state arrives, and shows what it says", async () => {
    const overlap = { ticket_id: 4, title: "t", status: "in_progress", path: "a.go", held: "a.go" };
    vi.spyOn(apiClient, "getConversation")
      .mockResolvedValueOnce(makeConversationDetail())
      .mockResolvedValue(makeConversationDetail({ scope_overlaps: [overlap] }));
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue(NO_EVENTS);
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    act(() => push(ev(1, "repo_state")));

    await waitFor(() =>
      expect(result.current.state.status === "loaded" && result.current.state.detail.scope_overlaps).toEqual([overlap]),
    );
  });

  it("does not read the detail again for assistant text, which changes nothing in it", async () => {
    const get = vi.spyOn(apiClient, "getConversation").mockResolvedValue(makeConversationDetail());
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue(NO_EVENTS);
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    act(() => push(ev(1, "assistant")));

    expect(get).toHaveBeenCalledTimes(1);
  });

  it("reads the detail again on a timer, once loaded, for host presence", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const get = vi
      .spyOn(apiClient, "getConversation")
      .mockResolvedValueOnce(makeConversationDetail({ host_online: true }))
      .mockResolvedValue(makeConversationDetail({ host_online: false }));
    vi.spyOn(apiClient, "listRecentConversationEvents").mockResolvedValue(NO_EVENTS);
    const { result } = renderHook(() => useConversation("c", "v"));
    await waitFor(() => expect(result.current.state.status).toBe("loaded"));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(DETAIL_REFRESH_MS);
    });

    expect(get).toHaveBeenCalledTimes(2);
    expect(result.current.state.status === "loaded" && result.current.state.detail.host_online).toBe(false);
  });
});
