import { describe, expect, it } from "vitest";
import { TicketFilterModel } from "@/domain/ticket_filter_model";
import { UNREVIEWED_FILTER_MODEL } from "@/domain/unreviewed_filter";
import { TicketQueryModel } from "./ticket_query";
import { SORT_IDS, SortModel } from "./ticket_sort";

const SORT = new SortModel(["newest", "oldest", "priority"], "newest");
const QUERY = new TicketQueryModel(UNREVIEWED_FILTER_MODEL, SORT);

describe("SortModel", () => {
  it("leaves the default out of the address and writes any other under its own key", () => {
    expect(SORT.toSearch("newest")).toEqual({ sort: undefined });
    expect(SORT.toSearch("priority")).toEqual({ sort: "priority" });
  });

  it("reads a sort back from the address, and anything it does not offer is the default", () => {
    expect(SORT.fromSearch({ sort: "oldest" })).toBe("oldest");
    expect(SORT.fromSearch({})).toBe("newest");
    // Registered, but not offered here.
    expect(SORT.fromSearch({ sort: "recently_closed" })).toBe("newest");
    expect(SORT.fromSearch({ sort: "id; DROP TABLE tickets" })).toBe("newest");
    expect(SORT.fromSearch({ sort: ["oldest"] })).toBe("newest");
    expect(SORT.fromSearch({ sort: "toString" })).toBe("newest");
  });

  it("refuses a default it does not offer", () => {
    expect(() => new SortModel(["oldest"], "newest")).toThrow(/not offered/);
  });
});

describe("TicketQueryModel with a sort", () => {
  it("starts on the page's default sort", () => {
    const model = new TicketQueryModel(UNREVIEWED_FILTER_MODEL, new SortModel(["newest", "recently_closed"], "recently_closed"));
    expect(model.initial().sort).toBe("recently_closed");
  });

  it("changes the sort without touching the filter, and starts paging again", () => {
    const filter = { ...UNREVIEWED_FILTER_MODEL.defaults(), text: "zebra" };
    const query = { ...QUERY.initial(filter), page: { limit: 20, cursor: "c9" } };

    const next = QUERY.withSort(query, "priority");

    expect(next).toEqual({ filter, sort: "priority", page: { limit: 20 } });
  });

  it("is a different list under a different sort, and the same one past a cursor", () => {
    const query = QUERY.initial();
    expect(QUERY.key(QUERY.withSort(query, "oldest"))).not.toEqual(QUERY.key(query));
    expect(QUERY.key({ ...query, page: { limit: 50, cursor: "c2" } })).toEqual(QUERY.key(query));
  });

  it("keeps the sort and the filter in separate address keys", () => {
    const filter = { ...UNREVIEWED_FILTER_MODEL.defaults(), text: "zebra" };

    expect(QUERY.toSearch(QUERY.withSort(QUERY.initial(filter), "oldest"))).toMatchObject({ q: "zebra", sort: "oldest" });
    expect(QUERY.toSearch(QUERY.initial(filter))).toMatchObject({ q: "zebra", sort: undefined });
    // The filter alone does not know the sort, so clearing the filter leaves it.
    expect(UNREVIEWED_FILTER_MODEL.searchPatch(UNREVIEWED_FILTER_MODEL.defaults())).not.toHaveProperty("sort");
    expect(UNREVIEWED_FILTER_MODEL.toSearch(filter)).not.toHaveProperty("sort");
  });

  it("is not 'filtered' by a non-default sort", () => {
    const query = QUERY.withSort(QUERY.initial(), "oldest");
    expect(UNREVIEWED_FILTER_MODEL.isFiltered(query.filter)).toBe(false);
  });

  it("round-trips the address, dropping what the page does not own", () => {
    const raw = { q: "zebra", sort: "priority", after: "c9", column: "open" };
    const parsed = QUERY.parseSearch<Record<string, unknown>>(raw);

    expect(parsed).toMatchObject({ q: "zebra", sort: "priority" });
    expect(parsed).not.toHaveProperty("after");
    expect(QUERY.parseSearch({ sort: "newest" })).toMatchObject({ sort: undefined });
  });

  it("names every registered sort to the server as itself", () => {
    for (const id of SORT_IDS) expect(QUERY.toApi(QUERY.initial(undefined, id)).params.sort).toBe(id);
  });

  it("refuses a date range that already uses the sort's address key", () => {
    for (const [fromParam, toParam] of [["sort", "to"], ["from", "sort"]]) {
      const clash = new TicketFilterModel("clash", [{ type: "date_range", id: "d", fromParam, toParam, label: "D", dateField: "closed" }]);
      expect(() => new TicketQueryModel(clash, SORT)).toThrow(/belongs to the sort/);
    }
  });

  it("refuses a filter that already uses the sort's address key", () => {
    const clash = new TicketFilterModel("clash", [{ type: "text", id: "text", param: "sort", label: "Text" }]);
    expect(() => new TicketQueryModel(clash, SORT)).toThrow(/belongs to the sort/);
  });
});
