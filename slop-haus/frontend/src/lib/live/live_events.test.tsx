import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { apiClient, type BoardChange } from "@/api";
import { useResource } from "@/lib/resource";
import { makeTicket } from "@/test/fixtures";
import { liveKeys } from "./live_keys";
import { liveEvents, ticketTopic, type LiveEvent } from "./live_events";
import { useLiveInvalidate } from "./use_live_invalidate";

interface OpenedStream {
  since: number | undefined;
  send: (change: Partial<BoardChange> & { revision: number }) => void;
  close: ReturnType<typeof vi.fn>;
}

describe("liveEvents", () => {
  let streams: OpenedStream[];
  const open = () => streams.filter((s) => s.close.mock.calls.length === 0);

  beforeEach(() => {
    streams = [];
    vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((onChange, since) => {
      const close = vi.fn();
      streams.push({ since, send: (change) => act(() => onChange(change.revision, change)), close });
      return close;
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shares one connection between subscribers and closes it with the last", () => {
    const unsubscribe = [1, 2, 3].map(() => liveEvents.subscribe("board", vi.fn()));
    expect(streams).toHaveLength(1);

    unsubscribe[0]();
    unsubscribe[1]();
    expect(open()).toHaveLength(1);
    unsubscribe[2]();
    expect(open()).toHaveLength(0);
  });

  it("hands every subscriber the event, and a ticket's topic only the changes that name it", () => {
    const board = vi.fn();
    const seven = vi.fn();
    const eight = vi.fn();
    const stops = [
      liveEvents.subscribe("board", board),
      liveEvents.subscribe(ticketTopic(7), seven),
      liveEvents.subscribe(ticketTopic(8), eight),
    ];

    streams[0].send({ revision: 1, tickets: [makeTicket({ id: 7 })] });
    expect(board).toHaveBeenCalledTimes(1);
    expect(seven).toHaveBeenCalledTimes(1);
    expect(eight).not.toHaveBeenCalled();

    // A change that cannot say which tickets is for every ticket.
    streams[0].send({ revision: 2, reload: true });
    expect(eight).toHaveBeenCalledTimes(1);
    stops.forEach((stop) => stop());
  });

  it("reopens from the last revision seen when the page becomes visible again", () => {
    const received: LiveEvent[] = [];
    const stop = liveEvents.subscribe("board", (e) => received.push(e));
    streams[0].send({ revision: 5 });

    const visibility = vi.spyOn(document, "visibilityState", "get");
    visibility.mockReturnValue("hidden");
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    expect(open()).toHaveLength(0);
    expect(received.at(-1)).toEqual({ kind: "pause" });

    visibility.mockReturnValue("visible");
    act(() => void document.dispatchEvent(new Event("visibilitychange")));
    expect(open()).toHaveLength(1);
    expect(open()[0].since).toBe(5);
    expect(received.at(-1)).toEqual({ kind: "resync" });
    stop();
  });

  it("gives a subscriber that joins an open stream a resync", async () => {
    const first = liveEvents.subscribe("board", vi.fn());
    streams[0].send({ revision: 1 });
    const late = vi.fn();
    const second = liveEvents.subscribe("board", late);
    await Promise.resolve();
    expect(late).toHaveBeenCalledWith({ kind: "resync" });
    expect(streams).toHaveLength(1);
    first();
    second();
  });

  it("invalidates the key a view is showing when the board changes", async () => {
    let served = "old";
    const fetcher = vi.fn(async () => served);
    const key = liveKeys.agentMessages();
    const { result } = renderHook(() => {
      useLiveInvalidate([key]);
      return useResource(key, fetcher);
    });
    await vi.waitFor(() => expect(result.current.data).toBe("old"));

    served = "new";
    streams[0].send({ revision: 1, reload: true });
    // The first event after mount is throttled behind the mount fetch's window.
    await vi.waitFor(() => expect(result.current.data).toBe("new"), { timeout: 5000 });
    expect(open()).toHaveLength(1);
  });
});
