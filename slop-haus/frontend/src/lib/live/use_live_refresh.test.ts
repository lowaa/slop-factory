import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { apiClient, type BoardChange } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { LIVE_REFRESH_THROTTLE_MS, useLiveRefresh } from "./use_live_refresh";

let visibility: DocumentVisibilityState = "visible";

function setVisibility(state: DocumentVisibilityState) {
  visibility = state;
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

function pageShow(persisted: boolean) {
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted }));
  });
}

function pageHide() {
  act(() => {
    window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true }));
  });
}

// The stream as the hook sees it: each open, with the revision it resumed
// from, the function that reports a change to it, and whether it is closed.
interface OpenedStream {
  since: number | undefined;
  change: (revision: number, change?: Partial<BoardChange>) => void;
  close: ReturnType<typeof vi.fn>;
}

describe("useLiveRefresh", () => {
  let streams: OpenedStream[];

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    visibility = "visible";
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
    streams = [];
    vi.spyOn(apiClient, "streamBoardChanges").mockImplementation((onChange, since) => {
      const close = vi.fn();
      streams.push({ since, change: (revision, change) => act(() => onChange(revision, { revision, ...change })), close });
      return close;
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  const open = () => streams.filter((s) => s.close.mock.calls.length === 0);
  const wait = (ms: number) => act(() => vi.advanceTimersByTime(ms));

  it("refetches when the stream says the board changed, no sooner than the throttle allows", () => {
    const refresh = vi.fn();
    renderHook(() => useLiveRefresh(refresh));
    expect(open()).toHaveLength(1);

    // The board's own first fetch has just been made: this one waits.
    streams[0].change(1);
    expect(refresh).not.toHaveBeenCalled();
    wait(LIVE_REFRESH_THROTTLE_MS);
    expect(refresh).toHaveBeenCalledTimes(1);

    // A while later, at once.
    wait(LIVE_REFRESH_THROTTLE_MS);
    streams[0].change(2);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("folds a burst of changes into one trailing refetch", () => {
    const refresh = vi.fn();
    renderHook(() => useLiveRefresh(refresh));
    wait(LIVE_REFRESH_THROTTLE_MS);

    streams[0].change(1);
    streams[0].change(2);
    streams[0].change(3);
    expect(refresh).toHaveBeenCalledTimes(1);

    wait(LIVE_REFRESH_THROTTLE_MS - 1);
    expect(refresh).toHaveBeenCalledTimes(1);
    wait(1);
    expect(refresh).toHaveBeenCalledTimes(2);
    wait(LIVE_REFRESH_THROTTLE_MS * 3);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it("closes the stream while the tab is hidden, and refetches and reopens it from the last revision on return", () => {
    const refresh = vi.fn();
    renderHook(() => useLiveRefresh(refresh));
    streams[0].change(41);
    wait(LIVE_REFRESH_THROTTLE_MS);
    refresh.mockClear();

    setVisibility("hidden");
    expect(open()).toHaveLength(0);
    wait(LIVE_REFRESH_THROTTLE_MS);

    setVisibility("visible");
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(open()).toHaveLength(1);
    expect(open()[0].since).toBe(41);
  });

  // Leaving drops a refetch still waiting for its turn: it would run in a
  // hidden tab, and coming back asks for one anyway.
  it.each([
    ["hidden", () => setVisibility("hidden"), () => setVisibility("visible")],
    ["put in the back/forward cache", pageHide, () => pageShow(true)],
  ])("drops a waiting refetch when the page is %s, and refetches on return", (_, leave, back) => {
    const refresh = vi.fn();
    renderHook(() => useLiveRefresh(refresh));
    streams[0].change(1);

    leave();
    wait(LIVE_REFRESH_THROTTLE_MS);
    expect(refresh).not.toHaveBeenCalled();

    back();
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it("throttles fast tab switching", () => {
    const refresh = vi.fn();
    renderHook(() => useLiveRefresh(refresh));
    wait(LIVE_REFRESH_THROTTLE_MS);

    for (let i = 0; i < 5; i++) {
      setVisibility("hidden");
      setVisibility("visible");
    }
    expect(refresh).toHaveBeenCalledTimes(1);
    wait(LIVE_REFRESH_THROTTLE_MS);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(open()).toHaveLength(1);
  });

  // A page restored from the back/forward cache never remounts: pageshow is
  // the only sign. The stream was closed on the way out so the page could be
  // cached at all.
  it("closes the stream on pagehide, and refetches and reopens it on a restore from the cache", () => {
    const refresh = vi.fn();
    renderHook(() => useLiveRefresh(refresh));
    wait(LIVE_REFRESH_THROTTLE_MS);

    pageHide();
    expect(open()).toHaveLength(0);

    pageShow(true);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(open()).toHaveLength(1);
  });

  it("leaves an ordinary page load to the board's own fetch", () => {
    const refresh = vi.fn();
    renderHook(() => useLiveRefresh(refresh));
    wait(LIVE_REFRESH_THROTTLE_MS);

    pageShow(false);

    expect(refresh).not.toHaveBeenCalled();
    expect(streams).toHaveLength(1);
  });

  it("opens no stream in a tab that starts hidden, until it is shown", () => {
    visibility = "hidden";
    renderHook(() => useLiveRefresh(vi.fn()));
    expect(streams).toHaveLength(0);

    setVisibility("visible");
    expect(open()).toHaveLength(1);
    expect(open()[0].since).toBeUndefined();
  });

  it("closes the stream and drops a pending refetch on unmount", () => {
    const refresh = vi.fn();
    const { unmount } = renderHook(() => useLiveRefresh(refresh));
    streams[0].change(1);

    unmount();
    wait(LIVE_REFRESH_THROTTLE_MS);

    expect(open()).toHaveLength(0);
    expect(refresh).not.toHaveBeenCalled();
  });

  it("calls the latest refresh it was given", () => {
    const first = vi.fn();
    const second = vi.fn();
    const { rerender } = renderHook(({ refresh }) => useLiveRefresh(refresh), { initialProps: { refresh: first } });
    rerender({ refresh: second });
    wait(LIVE_REFRESH_THROTTLE_MS);

    streams[0].change(1);

    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
    expect(streams).toHaveLength(1);
  });

  describe("a stream event that names tickets", () => {
    it("hands them over at once and refreshes the panels without the columns", () => {
      const refresh = vi.fn();
      const onTickets = vi.fn();
      renderHook(() => useLiveRefresh(refresh, undefined, onTickets));
      wait(LIVE_REFRESH_THROTTLE_MS);
      const changed = [makeTicket({ id: 4 })];

      streams[0].change(1, { tickets: changed });

      expect(onTickets).toHaveBeenCalledWith(changed, []);
      expect(refresh).toHaveBeenCalledWith({ columns: false });
    });

    it("asks for the columns when the event is a reload, or names nothing", () => {
      const refresh = vi.fn();
      const onTickets = vi.fn();
      renderHook(() => useLiveRefresh(refresh, undefined, onTickets));
      wait(LIVE_REFRESH_THROTTLE_MS);

      streams[0].change(1, { reload: true, tickets: [makeTicket({ id: 4 })] });
      wait(LIVE_REFRESH_THROTTLE_MS);
      streams[0].change(2);

      expect(onTickets).not.toHaveBeenCalled();
      expect(refresh.mock.calls).toEqual([[{ columns: true }], [{ columns: true }]]);
    });

    it("keeps the columns wanted when a reload is folded with a later named change", () => {
      const refresh = vi.fn();
      renderHook(() => useLiveRefresh(refresh, undefined, vi.fn()));

      streams[0].change(1, { reload: true });
      streams[0].change(2, { tickets: [makeTicket({ id: 4 })] });
      wait(LIVE_REFRESH_THROTTLE_MS);

      expect(refresh).toHaveBeenCalledTimes(1);
      expect(refresh).toHaveBeenCalledWith({ columns: true });
    });
  });
});
