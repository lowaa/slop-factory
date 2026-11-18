import { describe, expect, it } from "vitest";
import { UNREVIEWED_FILTER_MODEL } from "@/domain/unreviewed_filter";
import { FILTER_STATUSES, type FilterValues } from "@/domain/ticket_filter_model";
import { DEFAULT_PAGE_SIZE, TicketQueryModel, toUrlSearchParams } from "./ticket_query";

const MODEL = new TicketQueryModel(UNREVIEWED_FILTER_MODEL);

function filtered(patch: FilterValues): FilterValues {
  return { ...UNREVIEWED_FILTER_MODEL.defaults(), ...patch };
}

describe("TicketQueryModel", () => {
  it("starts from the filter's defaults, newest first, at the first page", () => {
    const query = MODEL.initial();

    expect(query.filter).toEqual(UNREVIEWED_FILTER_MODEL.defaults());
    expect(query.sort).toBe("newest");
    expect(query.page).toEqual({ limit: DEFAULT_PAGE_SIZE });
  });

  describe("toApi", () => {
    it("sends an explicit status list even at the filter's default, and always names the sort", () => {
      const api = MODEL.toApi(MODEL.initial());

      expect(api.status).toEqual(["preparing", "open", "in_progress", "in_merge_queue", "blocked", "discussion", "review", "done"]);
      expect(api.params.status).toEqual(api.status);
      expect(api.params.sort).toBe("newest");
      expect(api.params.limit).toBe("50");
      expect(api.matchesNothing).toBe(false);
    });

    it("names the sort the query asks for, so the server's per-filter default never decides", () => {
      expect(MODEL.toApi(MODEL.initial(undefined, "recently_closed")).params.sort).toBe("recently_closed");
    });

    it("carries the text, dates, limit and cursor", () => {
      const query = {
        ...MODEL.initial(filtered({ text: "zebra", closed: { from: "2026-01-01", to: "2026-02-01" } })),
        page: { limit: 20, cursor: "c2" },
      };

      expect(MODEL.toApi(query).params).toMatchObject({
        q: "zebra",
        closed_from: "2026-01-01",
        closed_to: "2026-02-01",
        limit: "20",
        cursor: "c2",
      });
    });

    it("says nothing can match when every status is unticked", () => {
      const api = MODEL.toApi(MODEL.initial(filtered({ status: [...FILTER_STATUSES] })));

      expect(api.status).toEqual([]);
      expect(api.matchesNothing).toBe(true);
    });
  });

  it("keeps the page cursor out of the address", () => {
    const query = { ...MODEL.initial(filtered({ text: "zebra" })), page: { limit: 50, cursor: "c2" } };

    expect(MODEL.toSearch(query)).toEqual(UNREVIEWED_FILTER_MODEL.toSearch(query.filter));
    expect(Object.keys(MODEL.toSearch(query))).not.toContain("cursor");
  });

  describe("key", () => {
    it("is the same however far the list has paged", () => {
      const first = MODEL.initial(filtered({ text: "zebra" }));
      const later = { ...first, page: { ...first.page, cursor: "c9" } };

      expect(MODEL.key(later)).toBe(MODEL.key(first));
    });

    it("changes with the filter, the status set, the sort and the page size", () => {
      const base = MODEL.initial();
      const keys = [
        MODEL.key(base),
        MODEL.key(MODEL.initial(filtered({ text: "a" }))),
        MODEL.key(MODEL.initial(filtered({ status: ["done"] }))),
        MODEL.key({ ...base, page: { limit: 10 } }),
        MODEL.key({ ...base, sort: "recently_closed" }),
      ];

      expect(new Set(keys).size).toBe(keys.length);
    });
  });

  it("withFilter starts again from the first page", () => {
    const paged = { ...MODEL.initial(), page: { limit: 20, cursor: "c2" } };

    expect(MODEL.withFilter(paged, filtered({ text: "x" })).page).toEqual({ limit: 20 });
  });
});

describe("toUrlSearchParams", () => {
  it("repeats list params and drops unset ones", () => {
    const search = toUrlSearchParams({ status: ["open", "blocked"], priority: ["high"], q: "a b", cursor: undefined });

    expect(search.toString()).toBe("status=open&status=blocked&priority=high&q=a+b");
  });

  it("refuses to serialise an empty status list, which the server would read as every status", () => {
    expect(() => toUrlSearchParams({ status: [] })).toThrow(/matches nothing/);
  });
});
