import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Ticket } from "@/api";
import { TicketQueryModel } from "../ticket_query/ticket_query";
import type { FetchedPage, LiveChanges } from "./paged_collection";
import { TEST_FILTER_MODEL } from "./test_filter_model";
import { usePagedCollection } from "./use_paged_collection";

const MODEL = new TicketQueryModel(TEST_FILTER_MODEL);
const logger = { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn(), event: vi.fn() };

function setup() {
  const calls: { signal: AbortSignal; resolve(p: FetchedPage): void }[] = [];
  const unsubscribe = vi.fn();
  let changes: LiveChanges | undefined;
  const deps = {
    key: "unreviewed",
    queryModel: MODEL,
    logger,
    fetchPage: (_p: unknown, signal: AbortSignal) => new Promise<FetchedPage>((resolve) => calls.push({ signal, resolve })),
    subscribe: (c: LiveChanges) => {
      changes = c;
      return unsubscribe;
    },
  };
  return { calls, unsubscribe, deps, live: () => changes! };
}

const ticket = (id: number) => ({ id, revision: 1, status: "open", priority: "normal", closed_at: null, created_at: "", updated_at: "" }) as unknown as Ticket;

describe("usePagedCollection", () => {
  it("loads, follows a query change, applies live changes and cleans up on unmount", async () => {
    const t = setup();
    const { result, rerender, unmount } = renderHook(({ q }) => usePagedCollection(t.deps, q), { initialProps: { q: MODEL.initial() } });
    expect(t.calls).toHaveLength(1);
    expect(result.current.loading).toBe(true);

    await act(async () => t.calls[0].resolve({ tickets: [ticket(2)] }));
    expect(result.current.items.map((x) => x.id)).toEqual([2]);

    act(() => t.live().onTicket(ticket(3)));
    expect(result.current.items.map((x) => x.id)).toEqual([3, 2]);

    rerender({ q: MODEL.withFilter(MODEL.initial(), { ...MODEL.initial().filter, text: "z" }) });
    expect(t.calls).toHaveLength(2);
    expect(result.current.items).toEqual([]);

    unmount();
    expect(t.calls[1].signal.aborted).toBe(true);
    expect(t.unsubscribe).toHaveBeenCalled();
  });

  it("is a new list for a new key: the old one is closed and the new one starts afresh", () => {
    const t = setup();
    const { rerender } = renderHook(({ key }) => usePagedCollection({ ...t.deps, key }, MODEL.initial()), { initialProps: { key: "board:done" } });
    expect(t.calls).toHaveLength(1);

    rerender({ key: "board:parked" });

    expect(t.calls).toHaveLength(2);
    expect(t.calls[0].signal.aborted).toBe(true);
    expect(t.calls[1].signal.aborted).toBe(false);
    expect(t.unsubscribe).toHaveBeenCalledTimes(1);
  });

  it("returns the same actions across renders, so a component can depend on them", () => {
    const t = setup();
    const { result, rerender } = renderHook(() => usePagedCollection(t.deps, MODEL.initial()));
    rerender();
    const first = result.current;
    rerender();
    expect(result.current.loadMore).toBe(first.loadMore);
    expect(result.current.retry).toBe(first.retry);
    expect(result.current.reload).toBe(first.reload);
  });
});
