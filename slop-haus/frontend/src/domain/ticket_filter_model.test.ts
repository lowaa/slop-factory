import { describe, expect, it } from "vitest";
import { APPROVALS_FILTER_MODEL } from "./approvals_filter";
import { FILTER_STATUSES, TicketFilterModel, type FilterValues } from "./ticket_filter_model";

const MODEL = new TicketFilterModel("test", [
  { type: "text", id: "text", param: "q", label: "Search" },
  { type: "flag", id: "done", param: "done", label: "Include done" },
  { type: "status", id: "status", param: "exclude", defaultExcluded: ["parked", "cancelled"] },
  { type: "priority", id: "priority", param: "priority" },
  { type: "date_range", id: "closed", fromParam: "from", toParam: "to", label: "Merged", dateField: "closed" },
]);

describe("TicketFilterModel", () => {
  it("starts from each field's default, none of which is in the address", () => {
    const values = MODEL.defaults();

    expect(values).toEqual({
      text: "",
      done: false,
      status: ["parked", "cancelled"],
      priority: "",
      closed: { from: "", to: "" },
    });
    expect(MODEL.toSearch(values)).toEqual({
      q: undefined,
      done: undefined,
      exclude: undefined,
      priority: undefined,
      from: undefined,
      to: undefined,
    });
    expect(MODEL.isFiltered(values)).toBe(false);
  });

  it("round-trips every field through the address", () => {
    const values: FilterValues = {
      text: "a b",
      done: true,
      status: ["done"],
      priority: "high",
      closed: { from: "2026-01-01", to: "2026-02-01" },
    };

    const search = MODEL.toSearch(values);

    expect(search).toEqual({
      q: "a b",
      done: true,
      exclude: "done",
      priority: "high",
      from: "2026-01-01",
      to: "2026-02-01",
    });
    expect(MODEL.fromSearch(search)).toEqual(values);
    expect(MODEL.isFiltered(values)).toBe(true);
  });

  it("can spell out showing every status, which differs from the default", () => {
    const values = { ...MODEL.defaults(), status: [] };

    expect(MODEL.toSearch(values).exclude).toBe("none");
    expect(MODEL.fromSearch({ exclude: "none" }).status).toEqual([]);
    expect(MODEL.isFiltered(values)).toBe(true);
  });

  it("takes statuses in any order and drops those it does not know", () => {
    expect(MODEL.fromSearch({ exclude: "done,bogus,open" }).status).toEqual(["open", "done"]);
    expect(MODEL.toSearch({ ...MODEL.defaults(), status: ["cancelled", "parked"] }).exclude).toBeUndefined();
  });

  it("treats a status list with nothing it knows as the default, not as hiding nothing", () => {
    expect(MODEL.fromSearch({ exclude: "bogus" }).status).toEqual(["parked", "cancelled"]);
  });

  it("falls back to the default for what the address holds wrongly", () => {
    const values = MODEL.fromSearch({ done: "yes", priority: "huge", from: "yesterday", to: 5, q: ["x"] });

    expect(values).toEqual(MODEL.defaults());
  });

  it("accepts a flag as the boolean an in-app navigation gives or the text an address gives", () => {
    expect(MODEL.fromSearch({ done: true }).done).toBe(true);
    expect(MODEL.fromSearch({ done: "true" }).done).toBe(true);
  });

  it("names every param when patching an address, so navigating clears what the values leave out", () => {
    expect(Object.keys(MODEL.searchPatch(MODEL.defaults())).sort()).toEqual(
      ["done", "exclude", "from", "priority", "q", "to"],
    );
    expect(MODEL.searchPatch({ ...MODEL.defaults(), text: "x" }).q).toBe("x");
  });

  it("asks the server for statuses only when the status filter is not its default", () => {
    expect(MODEL.includedStatuses(MODEL.defaults(), "status")).toBeUndefined();

    const included = MODEL.includedStatuses({ ...MODEL.defaults(), status: ["done"] }, "status");
    expect(included).toContain("open");
    expect(included).not.toContain("done");
  });

  it("refuses a field or an address param listed twice, and a second text field", () => {
    const text = { type: "text", id: "a", param: "q", label: "A" } as const;
    expect(() => new TicketFilterModel("x", [text, { ...text, param: "r" }])).toThrow(/listed twice/);
    expect(() => new TicketFilterModel("x", [text, { ...text, id: "b" }])).toThrow(/used twice/);
    expect(() => new TicketFilterModel("x", [text, { ...text, id: "b", param: "r" }])).toThrow(/at most one text/);
  });

  it("leaves an address key it does not own alone when its patch is merged into the address", () => {
    const address = { column: "review", q: "old", done: true, other: "kept" };

    const next = { ...address, ...MODEL.searchPatch({ ...MODEL.defaults(), text: "new" }) };

    expect(next.column).toBe("review");
    expect(next.other).toBe("kept");
    expect(next.q).toBe("new");
    expect(next.done).toBeUndefined();
  });

  it("reads and writes only the keys of its own fields", () => {
    const raw = { column: "review", other: "x", after: "c2", q: "zebra" };

    expect(MODEL.fromSearch(raw)).toEqual({ ...MODEL.defaults(), text: "zebra" });
    expect(Object.keys(MODEL.parseSearch(raw)).sort()).toEqual(["done", "exclude", "from", "priority", "q", "to"]);
    expect(Object.keys(MODEL.searchPatch(MODEL.defaults()))).not.toContain("column");
  });

  describe("activeCount", () => {
    it("is zero at the defaults", () => {
      expect(MODEL.activeCount(MODEL.defaults())).toBe(0);
    });

    it("counts each differing field once", () => {
      expect(MODEL.activeCount({ ...MODEL.defaults(), text: "bug", priority: "high" })).toBe(2);
    });

    it("counts a date range once, even with only one end set", () => {
      expect(MODEL.activeCount({ ...MODEL.defaults(), closed: { from: "2026-01-01", to: "" } })).toBe(1);
      expect(MODEL.activeCount({ ...MODEL.defaults(), closed: { from: "2026-01-01", to: "2026-02-01" } })).toBe(1);
    });

    it("counts excluded statuses only where they differ from the page's default", () => {
      const approvals = APPROVALS_FILTER_MODEL;
      expect(approvals.activeCount(approvals.defaults())).toBe(0);
      expect(approvals.activeCount({ ...approvals.defaults(), status: ["parked", "cancelled", "done", "blocked"] })).toBe(1);
      // Showing a status the page hides by default is a change too.
      expect(approvals.activeCount({ ...approvals.defaults(), status: ["parked", "cancelled"] })).toBe(1);
    });

    it("is zero exactly when the filter is not filtered", () => {
      const cases: FilterValues[] = [
        MODEL.defaults(),
        { ...MODEL.defaults(), done: true },
        { ...MODEL.defaults(), closed: { from: "", to: "2026-02-01" } },
        { ...MODEL.defaults(), status: [] },
      ];
      for (const values of cases) {
        expect(MODEL.activeCount(values) === 0).toBe(!MODEL.isFiltered(values));
      }
    });
  });

  describe("query", () => {
    it("always names the statuses, spelling out the default's included set", () => {
      const query = MODEL.query(MODEL.defaults());

      expect(query.status).toEqual(["preparing", "open", "in_progress", "in_merge_queue", "blocked", "discussion", "review", "done"]);
      expect(query.params).toEqual({});
    });

    it("turns each field into its API param", () => {
      const query = MODEL.query({
        text: "  zebra ",
        done: true,
        status: ["done", "cancelled", "parked"],
        priority: "high",
        closed: { from: "2026-01-01", to: "" },
      });

      expect(query.status).toEqual(["preparing", "open", "in_progress", "in_merge_queue", "blocked", "discussion", "review"]);
      expect(query.params).toEqual({ q: "  zebra ", priority: ["high"], closed_from: "2026-01-01" });
    });

    it("leaves out a blank text and gives an empty status list when every status is unticked", () => {
      const query = MODEL.query({ ...MODEL.defaults(), text: "   ", status: [...FILTER_STATUSES] });

      expect(query.status).toEqual([]);
      expect(query.params).toEqual({});
    });

    it("adds a flag's status when it is on, to the statuses always asked for", () => {
      const model = new TicketFilterModel(
        "flags",
        [
          { type: "flag", id: "done", param: "done", label: "Done", status: "done" },
          { type: "flag", id: "parked", param: "parked", label: "Parked", status: "parked" },
        ],
        ["open", "blocked"],
      );

      expect(model.query({ done: false, parked: false }).status).toEqual(["open", "blocked"]);
      expect(model.query({ done: true, parked: true }).status).toEqual(["parked", "open", "blocked", "done"]);
    });

    it("lets a status field narrow what a flag adds", () => {
      const model = new TicketFilterModel("both", [
        { type: "flag", id: "done", param: "done", label: "Done", status: "done" },
        { type: "status", id: "status", param: "exclude", defaultExcluded: ["parked"] },
      ], ["open"]);

      expect(model.query({ done: true, status: ["done"] }).status).toEqual(["open"]);
      expect(model.query({ done: true, status: [] }).status).toEqual(["open", "done"]);
    });

    it("asks for every status from a filter that names none", () => {
      const model = new TicketFilterModel("plain", [{ type: "text", id: "t", param: "q", label: "T" }]);

      expect(model.query({ t: "" }).status).toEqual([...FILTER_STATUSES]);
    });
  });
});
