import { describe, expect, it } from "vitest";
import { FILTER_STATUSES } from "./ticket_filter_model";
import { APPROVALS_FILTER_MODEL, DEFAULT_APPROVALS_VALUES, approvalsTicketFilter, parseApprovalsSearch } from "./approvals_filter";

describe("approvals filter", () => {
  it("is remembered under the tab's name, which is how the Back link finds it", () => {
    expect(APPROVALS_FILTER_MODEL.key).toBe("approvals");
  });

  it("hides parked, cancelled and done by default, and leaves every default out of the address", () => {
    expect([...(DEFAULT_APPROVALS_VALUES.status as string[])].sort()).toEqual(["cancelled", "done", "parked"]);
    expect(parseApprovalsSearch({})).toEqual(expect.not.objectContaining({ exclude: expect.anything() }));
    expect(Object.values(parseApprovalsSearch({})).every((v) => v === undefined)).toBe(true);
    const status = approvalsTicketFilter(DEFAULT_APPROVALS_VALUES)!.status as string[];
    expect(status).toContain("in_progress");
    for (const hidden of ["parked", "cancelled", "done"]) expect(status).not.toContain(hidden);
  });

  it("round-trips the text, priority and both date ranges through the address", () => {
    const search = { q: "login", priority: "high", created_from: "2026-01-01", updated_to: "2026-02-01", exclude: "done" };
    expect(parseApprovalsSearch(search)).toEqual(expect.objectContaining(search));
    expect(approvalsTicketFilter(APPROVALS_FILTER_MODEL.fromSearch(search))).toEqual(
      expect.objectContaining({ q: "login", priority: ["high"], created_from: "2026-01-01", updated_to: "2026-02-01" }),
    );
    expect(approvalsTicketFilter(APPROVALS_FILTER_MODEL.fromSearch(search))!.status).toContain("parked");
  });

  it("asks for nothing when every status is hidden", () => {
    expect(approvalsTicketFilter(APPROVALS_FILTER_MODEL.fromSearch({ exclude: "none" }))).not.toBeNull();
    const all = { ...DEFAULT_APPROVALS_VALUES, status: FILTER_STATUSES };
    expect(approvalsTicketFilter(all)).toBeNull();
  });
});
