import { describe, expect, it, vi } from "vitest";
import type { Ticket } from "@/api";
import { ApiError } from "@/api";
import { EVERY_STATUS_EXCLUDED, TEST_FILTER_MODEL } from "./test_filter_model";
import { TicketQueryModel, type TicketQuery } from "../ticket_query/ticket_query";
import { PagedCollection, type FetchedPage, type LiveChanges } from "./paged_collection";

const MODEL = new TicketQueryModel(TEST_FILTER_MODEL);

// The n-th second of a day: later is newer. The server moves updated_at on
// every change, but `revision` only on title and body edits, so fixtures
// move the stamp and leave revision alone.
const at = (n: number) => `2026-01-01T00:00:${String(n).padStart(2, "0")}Z`;

function ticket(id: number, patch: Partial<Ticket> = {}): Ticket {
  return {
    id,
    revision: 0,
    status: "open",
    priority: "normal",
    title: `t${id}`,
    closed_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: at(1),
    ...patch,
  } as Ticket;
}

// One attempt's card of a ticket with several attempts.
function card(id: number, attempt: number, patch: Partial<Ticket> = {}): Ticket {
  return {
    ...ticket(id, patch),
    attempt: { number: attempt, total: 2, status: "in_progress", ticket_status: "in_progress", awaiting_choice: false, variant: null, worker_cost: [] },
  };
}

const cardIds = (items: readonly Ticket[]) => items.map((t) => `${t.id}/${t.attempt?.number}`);

function page(ids: number[], nextCursor?: string): FetchedPage {
  return { tickets: ids.map((id) => ticket(id)), nextCursor };
}

interface Call {
  params: Record<string, unknown>;
  signal: AbortSignal;
  resolve(p: FetchedPage): void;
  reject(e: unknown): void;
}

// Every fetch is a promise the test settles, so order and timing are the
// test's to choose.
function setup(opts: { query?: TicketQuery } = {}) {
  const calls: Call[] = [];
  let changes: LiveChanges | undefined;
  const unsubscribe = vi.fn();
  const logger = { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn(), event: vi.fn() };
  const collection = new PagedCollection({
    key: "unreviewed",
    queryModel: MODEL,
    logger,
    fetchPage: (params, signal) =>
      new Promise<FetchedPage>((resolve, reject) => {
        calls.push({ params, signal, resolve, reject });
      }),
    subscribe: (c) => {
      changes = c;
      return unsubscribe;
    },
  });
  const query = opts.query ?? MODEL.initial();
  collection.start(query);
  const ids = () => collection.getSnapshot().items.map((t) => t.id);
  // Settle the call and let the collection's continuation run.
  const settle = async (call: Call, p: FetchedPage) => {
    call.resolve(p);
    await Promise.resolve();
    await Promise.resolve();
  };
  const fail = async (call: Call, e: unknown) => {
    call.reject(e);
    await Promise.resolve();
    await Promise.resolve();
  };
  return { collection, calls, ids, settle, fail, logger, unsubscribe, live: () => changes!, query };
}

describe("PagedCollection", () => {
  describe("paging", () => {
    it("loads the first page, then the next with the cursor, and stops without one", async () => {
      const t = setup();
      expect(t.collection.getSnapshot().loading).toBe(true);
      await t.settle(t.calls[0], page([9, 8], "c1"));
      expect(t.ids()).toEqual([9, 8]);
      expect(t.collection.getSnapshot()).toMatchObject({ loaded: true, loading: false, hasMore: true });
      expect(t.calls[0].params.cursor).toBeUndefined();

      t.collection.loadMore();
      expect(t.collection.getSnapshot().loadingMore).toBe(true);
      expect(t.calls[1].params.cursor).toBe("c1");
      await t.settle(t.calls[1], page([7, 6]));
      expect(t.ids()).toEqual([9, 8, 7, 6]);
      expect(t.collection.getSnapshot()).toMatchObject({ hasMore: false, loadingMore: false });

      t.collection.loadMore();
      expect(t.calls).toHaveLength(2);
    });

    it("does not start a second page while one is in flight", async () => {
      const t = setup();
      await t.settle(t.calls[0], page([9], "c1"));
      t.collection.loadMore();
      t.collection.loadMore();
      expect(t.calls).toHaveLength(2);
    });

    it("keeps a ticket once when pages overlap, and the newer copy of it", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [ticket(9), ticket(8, { updated_at: at(3), title: "new" })], nextCursor: "c" });
      t.collection.loadMore();
      await t.settle(t.calls[1], { tickets: [ticket(8, { updated_at: at(2), title: "old" }), ticket(7)] });
      expect(t.ids()).toEqual([9, 8, 7]);
      expect(t.collection.getSnapshot().items[1].title).toBe("new");
    });
  });

  it("keeps the order the server sent, whatever the ids", async () => {
    const t = setup();
    await t.settle(t.calls[0], page([5, 9, 7], "c"));
    t.collection.loadMore();
    await t.settle(t.calls[1], page([6, 8]));
    expect(t.ids()).toEqual([5, 9, 7, 6, 8]);
  });

  describe("query changes", () => {
    it("starts over for a different filter and ignores the old query's late reply", async () => {
      const t = setup();
      await t.settle(t.calls[0], page([9], "c1"));
      t.collection.setQuery(MODEL.withFilter(t.query, { ...t.query.filter, text: "zebra" }));
      expect(t.collection.getSnapshot()).toMatchObject({ items: [], loaded: false, loading: true });
      expect(t.calls[1].params.q).toBe("zebra");

      await t.settle(t.calls[1], page([5]));
      expect(t.ids()).toEqual([5]);
    });

    it("aborts the request of the query it replaces, and ignores its reply", async () => {
      const t = setup();
      t.collection.setQuery(MODEL.withFilter(t.query, { ...t.query.filter, text: "zebra" }));
      expect(t.calls[0].signal.aborted).toBe(true);
      await t.settle(t.calls[0], page([1]));
      expect(t.ids()).toEqual([]);
      await t.settle(t.calls[1], page([2]));
      expect(t.ids()).toEqual([2]);
    });

    it("keeps the list for an equal query, whatever its cursor", async () => {
      const t = setup();
      await t.settle(t.calls[0], page([9], "c1"));
      t.collection.setQuery({ ...t.query, page: { ...t.query.page, cursor: "zzz" } });
      expect(t.calls).toHaveLength(1);
      expect(t.ids()).toEqual([9]);
    });
  });

  describe("stale replies", () => {
    it("ignores a first page that arrives after a reload's", async () => {
      const t = setup();
      t.collection.reload();
      await t.settle(t.calls[1], page([2]));
      await t.settle(t.calls[0], page([1]));
      expect(t.ids()).toEqual([2]);
    });

    it("ignores a failure of a request that was overtaken", async () => {
      const t = setup();
      t.collection.reload();
      await t.fail(t.calls[0], new Error("boom"));
      expect(t.collection.getSnapshot().error).toBeNull();
      expect(t.collection.getSnapshot().loading).toBe(true);
    });

    it("drops a next page the list was reloaded under", async () => {
      const t = setup();
      await t.settle(t.calls[0], page([9], "c1"));
      t.collection.loadMore();
      t.collection.reload();
      await t.settle(t.calls[1], page([1]));
      await t.settle(t.calls[2], page([9, 8]));
      expect(t.ids()).toEqual([9, 8]);
    });
  });

  describe("nothing to show", () => {
    it("is an empty list with no request when every status is unticked", () => {
      const all = MODEL.initial();
      const none = MODEL.withFilter(all, { ...all.filter, status: EVERY_STATUS_EXCLUDED });
      const t = setup({ query: none });
      expect(t.calls).toHaveLength(0);
      expect(t.collection.getSnapshot()).toMatchObject({ items: [], loaded: true, loading: false, hasMore: false });
    });

    it("ignores live changes while it matches nothing", () => {
      const all = MODEL.initial();
      const none = MODEL.withFilter(all, { ...all.filter, status: EVERY_STATUS_EXCLUDED });
      const t = setup({ query: none });
      t.live().onTicket(ticket(5));
      expect(t.ids()).toEqual([]);
      expect(t.calls).toHaveLength(0);
    });
  });

  describe("errors", () => {
    it("shows the server's words, keeps what is loaded, and retries the page that failed", async () => {
      const t = setup();
      await t.settle(t.calls[0], page([9], "c1"));
      t.collection.loadMore();
      await t.fail(t.calls[1], new ApiError(500, "server says no"));
      expect(t.collection.getSnapshot()).toMatchObject({ error: "server says no", loadingMore: false, hasMore: true });
      expect(t.ids()).toEqual([9]);
      expect(t.logger.error).toHaveBeenCalled();

      t.collection.retry();
      expect(t.collection.getSnapshot().error).toBeNull();
      expect(t.calls[2].params.cursor).toBe("c1");
      await t.settle(t.calls[2], page([8]));
      expect(t.ids()).toEqual([9, 8]);
    });

    it("keeps what is shown when a reload fails, and retries the reload", async () => {
      const t = setup();
      await t.settle(t.calls[0], page([9, 8], "c"));
      t.collection.reload();
      await t.fail(t.calls[1], new Error("boom"));
      expect(t.ids()).toEqual([9, 8]);
      expect(t.collection.getSnapshot()).toMatchObject({ error: "Could not load tickets.", loading: false, loaded: true });
      t.collection.retry();
      expect(t.calls).toHaveLength(3);
    });

    it("retries a failed first page from the top", async () => {
      const t = setup();
      await t.fail(t.calls[0], new Error("boom"));
      expect(t.collection.getSnapshot()).toMatchObject({ error: "Could not load tickets.", loading: false, loaded: false });
      t.collection.retry();
      expect(t.calls[1].params.cursor).toBeUndefined();
      await t.settle(t.calls[1], page([3]));
      expect(t.ids()).toEqual([3]);
      expect(t.collection.getSnapshot().error).toBeNull();
    });

    it("does not report an AbortError from a request that is still current", async () => {
      const t = setup();
      await t.fail(t.calls[0], new DOMException("aborted", "AbortError"));
      expect(t.logger.error).not.toHaveBeenCalled();
      expect(t.collection.getSnapshot().error).toBeNull();
    });

    it("does not report an abort as an error", async () => {
      const t = setup();
      t.collection.close();
      await t.fail(t.calls[0], new DOMException("aborted", "AbortError"));
      expect(t.logger.error).not.toHaveBeenCalled();
    });
  });

  describe("live changes", () => {
    async function loaded(ids: number[], next?: string) {
      const t = setup();
      await t.settle(t.calls[0], page(ids, next));
      return t;
    }

    it("updates a loaded ticket in place", async () => {
      const t = await loaded([9, 8, 7]);
      t.live().onTicket(ticket(8, { updated_at: at(2), title: "renamed" }));
      expect(t.ids()).toEqual([9, 8, 7]);
      expect(t.collection.getSnapshot().items[1].title).toBe("renamed");
    });

    it("ignores an older copy of a loaded ticket, even one that would drop it", async () => {
      const t = await loaded([9, 8]);
      t.live().onTicket(ticket(8, { updated_at: at(0), title: "older" }));
      t.live().onTicket(ticket(8, { updated_at: at(0), status: "cancelled" }));
      expect(t.ids()).toEqual([9, 8]);
      expect(t.collection.getSnapshot().items[1].title).toBe("t8");
    });

    it("does not bring back a ticket a change removed when an older page then carries it", async () => {
      const t = await loaded([9, 8], "c");
      t.collection.loadMore();
      t.live().onTicket(ticket(8, { updated_at: at(2), status: "cancelled" }));
      t.live().onTicket(ticket(7, { updated_at: at(2), status: "cancelled" }));
      expect(t.ids()).toEqual([9]);
      await t.settle(t.calls[1], page([8, 7, 6]));
      expect(t.ids()).toEqual([9, 6]);
    });

    it("takes a ticket from a page that is newer than the change seen live", async () => {
      const t = await loaded([9], "c");
      t.collection.loadMore();
      t.live().onTicket(ticket(7, { updated_at: at(2), status: "cancelled" }));
      await t.settle(t.calls[1], { tickets: [ticket(7, { updated_at: at(3) })] });
      expect(t.ids()).toEqual([9, 7]);
    });

    it("drops a loaded ticket that no longer matches", async () => {
      const t = await loaded([9, 8, 7]);
      t.live().onTicket(ticket(8, { updated_at: at(2), status: "cancelled" }));
      expect(t.ids()).toEqual([9, 7]);
    });

    it("drops a loaded ticket whose priority no longer matches", async () => {
      const all = MODEL.initial();
      const urgent = MODEL.withFilter(all, { ...all.filter, priority: "urgent" });
      const t = setup({ query: urgent });
      await t.settle(t.calls[0], { tickets: [ticket(9, { priority: "urgent" }), ticket(8, { priority: "urgent" })] });
      t.live().onTicket(ticket(8, { updated_at: at(2), priority: "low" }));
      expect(t.ids()).toEqual([9]);
    });

    it("ignores an unloaded ticket that matches no filter", async () => {
      const t = await loaded([9, 8], "c");
      t.live().onTicket(ticket(20, { status: "cancelled" }));
      expect(t.ids()).toEqual([9, 8]);
      expect(t.calls).toHaveLength(1);
    });

    it("inserts an unloaded matching ticket that sorts within the loaded range", async () => {
      const t = await loaded([9, 7], "c");
      t.live().onTicket(ticket(8));
      expect(t.ids()).toEqual([9, 8, 7]);
      expect(t.calls).toHaveLength(1);
    });

    it("inserts a matching ticket newer than everything loaded", async () => {
      const t = await loaded([9, 8], "c");
      t.live().onTicket(ticket(10, { status: "done" }));
      expect(t.ids()).toEqual([10, 9, 8]);
    });

    it("leaves a matching ticket beyond the loaded range for a later page", async () => {
      const t = await loaded([9, 8], "c");
      t.live().onTicket(ticket(3));
      expect(t.ids()).toEqual([9, 8]);
    });

    it("inserts at the end when everything is loaded", async () => {
      const t = await loaded([9, 8]);
      t.live().onTicket(ticket(3));
      expect(t.ids()).toEqual([9, 8, 3]);
    });

    it("reloads the first page for an unloaded ticket the server must judge", async () => {
      const t = setup({ query: MODEL.withFilter(MODEL.initial(), { ...MODEL.initial().filter, text: "zebra" }) });
      await t.settle(t.calls[0], page([9, 8], "c"));
      t.live().onTicket(ticket(20));
      expect(t.calls).toHaveLength(2);
      expect(t.calls[1].params.cursor).toBeUndefined();
      // What is shown stays until the new page lands.
      expect(t.ids()).toEqual([9, 8]);
      await t.settle(t.calls[1], page([20, 9]));
      expect(t.ids()).toEqual([20, 9]);
    });

    it("does not reload for an unloaded ticket the server need not judge when text search is on but it fails another filter", async () => {
      const t = setup({ query: MODEL.withFilter(MODEL.initial(), { ...MODEL.initial().filter, text: "zebra" }) });
      await t.settle(t.calls[0], page([9], "c"));
      t.live().onTicket(ticket(20, { status: "cancelled" }));
      expect(t.calls).toHaveLength(1);
    });

    it("fetches the first page again when a change arrives while it is on its way", async () => {
      const t = setup();
      t.live().onTicket(ticket(5));
      await t.settle(t.calls[0], page([9]));
      expect(t.calls).toHaveLength(2);
      await t.settle(t.calls[1], page([9, 5]));
      expect(t.ids()).toEqual([9, 5]);
    });

    it("reloads on a reload signal", async () => {
      const t = await loaded([9]);
      t.live().onReload();
      expect(t.calls).toHaveLength(2);
    });

    it("does not bring back a ticket a status change removed when an older page then carries it", async () => {
      const t = await loaded([9, 8], "c");
      t.collection.loadMore();
      t.live().onTicket(ticket(7, { status: "cancelled", updated_at: at(2) }));
      await t.settle(t.calls[1], page([7, 6]));
      expect(t.ids()).toEqual([9, 8, 6]);
    });

    it("keeps a priority change made live when an older page then carries the ticket", async () => {
      const t = await loaded([9, 8], "c");
      t.collection.loadMore();
      t.live().onTicket(ticket(7, { priority: "low", updated_at: at(2) }));
      await t.settle(t.calls[1], page([7, 6]));
      expect(t.ids()).toEqual([9, 8, 7, 6]);
      expect(t.collection.getSnapshot().items[2].priority).toBe("low");
    });

    it("ignores a live copy older than the one shown when only the status differs", async () => {
      const t = await loaded([9, 8]);
      t.live().onTicket(ticket(8, { status: "done", updated_at: at(3) }));
      t.live().onTicket(ticket(8, { status: "cancelled", updated_at: at(2) }));
      expect(t.ids()).toEqual([9, 8]);
      expect(t.collection.getSnapshot().items[1].status).toBe("done");
    });

    it("does not blink the spinner off and on when it reloads", async () => {
      const t = await loaded([9]);
      t.collection.loadMore();
      const seen: boolean[] = [];
      t.collection.subscribe(() => seen.push(t.collection.getSnapshot().loading));
      t.collection.reload();
      expect(seen).not.toContain(false);
      expect(t.collection.getSnapshot()).toMatchObject({ loading: true, loadingMore: false });
    });
  });

  describe("choosing a sort", () => {
    it("starts over under the new sort and keeps the filter", async () => {
      const filter = { ...TEST_FILTER_MODEL.defaults(), text: "zebra" };
      const t = setup({ query: MODEL.initial(filter) });
      await t.settle(t.calls[0], page([3, 2], "c2"));
      t.collection.loadMore();
      await t.settle(t.calls[1], page([1]));
      expect(t.calls[1].params.cursor).toBe("c2");

      t.collection.setQuery(MODEL.withSort(t.query, "oldest"));

      expect(t.collection.getSnapshot()).toMatchObject({ items: [], loading: true, loaded: false });
      expect(t.calls[2].params).toMatchObject({ sort: "oldest", q: "zebra" });
      expect(t.calls[2].params).not.toHaveProperty("cursor");
    });

    it("ignores the old sort's reply when it arrives after the change", async () => {
      const t = setup();
      t.collection.setQuery(MODEL.withSort(t.query, "oldest"));
      expect(t.calls[0].signal.aborted).toBe(true);

      await t.settle(t.calls[0], page([9]));
      await t.settle(t.calls[1], page([1, 2]));

      expect(t.ids()).toEqual([1, 2]);
    });

    it("does not start over for the sort it already shows", async () => {
      const t = setup();
      await t.settle(t.calls[0], page([2, 1]));
      t.collection.setQuery(MODEL.withSort(t.query, "newest"));
      expect(t.calls).toHaveLength(1);
    });
  });

  describe("placing a live change by the sort", () => {
    async function sorted(sort: "oldest" | "recently_updated" | "priority", tickets: Ticket[], next?: string) {
      const t = setup({ query: MODEL.initial(undefined, sort) });
      await t.settle(t.calls[0], { tickets, nextCursor: next });
      return t;
    }

    it("oldest: a new ticket goes after the older ones", async () => {
      const t = await sorted("oldest", [ticket(2), ticket(5)]);
      t.live().onTicket(ticket(7));
      expect(t.ids()).toEqual([2, 5, 7]);
    });

    it("recently updated: a touched ticket moves to the top", async () => {
      const t = await sorted("recently_updated", [ticket(5, { updated_at: at(9) }), ticket(2, { updated_at: at(5) }), ticket(1, { updated_at: at(2) })]);
      t.live().onTicket(ticket(1, { updated_at: at(20) }));
      expect(t.ids()).toEqual([1, 5, 2]);
    });

    it("recently updated: updates a microsecond apart are ordered, not left to the id", async () => {
      const stamp = (frac: string) => `2026-01-01T00:00:05.${frac}Z`;
      const t = await sorted("recently_updated", [ticket(1, { updated_at: stamp("000002") }), ticket(9, { updated_at: stamp("000001") })]);
      t.live().onTicket(ticket(5, { updated_at: stamp("0000015") }));
      expect(t.ids()).toEqual([1, 5, 9]);
    });

    it("a stale copy a microsecond older than the one shown does not replace it", async () => {
      const stamp = (frac: string) => `2026-01-01T00:00:05.${frac}Z`;
      const t = await sorted("oldest", [ticket(1, { title: "new", updated_at: stamp("000002") })]);
      t.live().onTicket(ticket(1, { title: "stale", updated_at: stamp("000001") }));
      expect(t.collection.getSnapshot().items[0].title).toBe("new");
    });

    it("priority: a ticket raised to urgent moves above the rest", async () => {
      const t = await sorted("priority", [ticket(9, { priority: "high" }), ticket(8, { priority: "normal" }), ticket(2, { priority: "low" })]);
      t.live().onTicket(ticket(2, { priority: "urgent", updated_at: at(3) }));
      expect(t.ids()).toEqual([2, 9, 8]);
    });

    it("priority: within a priority the newest comes first", async () => {
      const t = await sorted("priority", [ticket(9, { priority: "high" }), ticket(4, { priority: "high" })]);
      t.live().onTicket(ticket(6, { priority: "high" }));
      expect(t.ids()).toEqual([9, 6, 4]);
    });
  });

  describe("a sort that is not by id (recently closed)", () => {
    const closed = (id: number, day: number, patch: Partial<Ticket> = {}) =>
      ticket(id, { status: "done", closed_at: `2026-01-${String(day).padStart(2, "0")}T00:00:00Z`, ...patch });

    async function recent(tickets: Ticket[], next?: string) {
      const t = setup({ query: MODEL.initial(undefined, "recently_closed") });
      await t.settle(t.calls[0], { tickets, nextCursor: next });
      return t;
    }

    it("asks for the sort by name", async () => {
      const t = await recent([]);
      expect(t.calls[0].params.sort).toBe("recently_closed");
    });

    it("inserts by closed_at, not by id", async () => {
      // 3 was closed last although its id is lowest.
      const t = await recent([closed(5, 20), closed(9, 10)], "c");
      t.live().onTicket(closed(3, 15));
      expect(t.ids()).toEqual([5, 3, 9]);
    });

    it("leaves a ticket closed before the last loaded one for a later page, whatever its id", async () => {
      const t = await recent([closed(5, 20), closed(2, 10)], "c");
      t.live().onTicket(closed(99, 5));
      expect(t.ids()).toEqual([5, 2]);
    });

    it("shows a ticket closed after the last loaded one, even with a lower id", async () => {
      const t = await recent([closed(5, 20), closed(9, 10)], "c");
      t.live().onTicket(closed(1, 12));
      expect(t.ids()).toEqual([5, 1, 9]);
    });

    it("moves a loaded ticket to the top when it is closed again", async () => {
      const t = await recent([closed(5, 20), closed(9, 10), closed(7, 8)]);
      t.live().onTicket(closed(7, 25, { updated_at: at(2) }));
      expect(t.ids()).toEqual([7, 5, 9]);
    });

    it("drops a moved ticket that now belongs beyond the loaded range", async () => {
      const t = await recent([closed(5, 20), closed(9, 10)], "c");
      t.live().onTicket(closed(5, 2, { updated_at: at(2) }));
      expect(t.ids()).toEqual([9]);
    });

    it("updates a ticket in place when its position is unchanged", async () => {
      const t = await recent([closed(5, 20), closed(9, 10)], "c");
      t.live().onTicket(closed(9, 10, { title: "renamed", updated_at: at(2) }));
      expect(t.ids()).toEqual([5, 9]);
      expect(t.collection.getSnapshot().items[1].title).toBe("renamed");
    });
  });

  describe("attempt cards", () => {
    it("keeps every card of a ticket, by (ticket, attempt), in the server's order", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [card(5, 2), card(5, 1), ticket(4)] });
      expect(cardIds(t.collection.getSnapshot().items)).toEqual(["5/2", "5/1", "4/undefined"]);
    });

    it("keeps a card once when pages overlap, and the newer copy of it", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [card(5, 2), card(5, 1)], nextCursor: "c1" });
      t.collection.loadMore();
      await t.settle(t.calls[1], { tickets: [card(5, 1, { title: "newer", updated_at: at(5) })] });
      expect(cardIds(t.collection.getSnapshot().items)).toEqual(["5/2", "5/1"]);
      expect(t.collection.getSnapshot().items[1].title).toBe("newer");
    });

    it("redraws every card of a ticket from the cards a change carries", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [card(5, 2), card(5, 1), ticket(4)] });
      t.live().onTicket(ticket(5, { updated_at: at(9) }), [
        card(5, 2, { title: "two", updated_at: at(9) }),
        card(5, 1, { title: "one", updated_at: at(9) }),
      ]);
      expect(t.collection.getSnapshot().items.map((c) => c.title)).toEqual(["two", "one", "t4"]);
    });

    it("drops a card of the ticket that its cards no longer include", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [card(5, 2), card(5, 1)] });
      t.live().onTicket(ticket(5, { updated_at: at(9) }), [card(5, 2, { updated_at: at(9) })]);
      expect(cardIds(t.collection.getSnapshot().items)).toEqual(["5/2"]);
    });

    it("replaces the cards with the ticket card when the ticket has one attempt again", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [card(5, 2), card(5, 1)] });
      t.live().onTicket(ticket(5, { updated_at: at(9) }), []);
      expect(cardIds(t.collection.getSnapshot().items)).toEqual(["5/undefined"]);
    });

    it("places a card that arrives live beside its ticket's others, in the id's direction", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [card(5, 3), card(5, 1)] });
      t.live().onTicket(ticket(5, { updated_at: at(9) }), [card(5, 3), card(5, 2, { updated_at: at(9) }), card(5, 1)]);
      expect(cardIds(t.collection.getSnapshot().items)).toEqual(["5/3", "5/2", "5/1"]);
    });

    it("ignores a late, older change that would put back a card a newer one dropped", async () => {
      const t = setup();
      await t.settle(t.calls[0], { tickets: [card(5, 2), card(5, 1)] });
      t.live().onTicket(ticket(5, { updated_at: at(9) }), [card(5, 2, { updated_at: at(9) })]);
      t.live().onTicket(ticket(5, { updated_at: at(4) }), [card(5, 2, { updated_at: at(4) }), card(5, 1, { updated_at: at(4) })]);
      expect(cardIds(t.collection.getSnapshot().items)).toEqual(["5/2"]);
    });

    it("keeps a live card over an older page copy when only the attempt's own stamp moved", async () => {
      const t = setup();
      t.live().onTicket(ticket(5, { updated_at: at(1) }), [card(5, 2, { title: "live", updated_at: at(9) }), card(5, 1)]);
      await t.settle(t.calls[0], { tickets: [card(5, 2, { title: "stale", updated_at: at(3) }), card(5, 1)] });
      expect(t.collection.getSnapshot().items.map((c) => c.title)).toEqual(["live", "t5"]);
    });

    it("does not bring back a card a change removed when an older page lands", async () => {
      const t = setup();
      t.live().onTicket(ticket(5, { updated_at: at(9) }), [card(5, 2, { updated_at: at(9) })]);
      await t.settle(t.calls[0], { tickets: [card(5, 2), card(5, 1)] });
      expect(cardIds(t.collection.getSnapshot().items)).toEqual(["5/2"]);
    });
  });

  describe("close", () => {
    it("aborts the request, unsubscribes, and ignores everything after", async () => {
      const t = setup();
      const listener = vi.fn();
      t.collection.subscribe(listener);
      t.collection.close();
      expect(t.calls[0].signal.aborted).toBe(true);
      expect(t.unsubscribe).toHaveBeenCalledTimes(1);
      await t.settle(t.calls[0], page([1]));
      t.collection.reload();
      t.collection.loadMore();
      t.live().onTicket(ticket(2));
      expect(t.ids()).toEqual([]);
      expect(t.calls).toHaveLength(1);
      expect(listener).not.toHaveBeenCalled();
      t.collection.close();
      expect(t.unsubscribe).toHaveBeenCalledTimes(1);
    });
  });
});
