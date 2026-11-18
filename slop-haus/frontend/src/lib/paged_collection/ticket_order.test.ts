import { describe, expect, it } from "vitest";
import type { Priority, Ticket } from "@/api";
import { SORT_IDS } from "../ticket_query/ticket_sort";
import fixture from "../../../../testdata/ticket_sort_orders.json";
import { COMPARATORS } from "./ticket_order";

const tickets = fixture.tickets.map(
  (t) => ({ id: t.id, priority: t.priority as Priority, updated_at: t.updated_at, closed_at: t.closed_at }) as Ticket,
);

describe("COMPARATORS", () => {
  it("has an order fixture for every sort, and the fixture no sort the client lacks", () => {
    expect(Object.keys(fixture.orders).sort()).toEqual([...SORT_IDS].sort());
  });

  // The fixture is the order Postgres returns (ticket/sort_test.go).
  for (const [sort, want] of Object.entries(fixture.orders)) {
    it(`orders ${sort} as the server does, from any starting order`, () => {
      const compare = COMPARATORS[sort as keyof typeof COMPARATORS];
      for (const start of [tickets, [...tickets].reverse(), [...tickets].sort((a, b) => ((a.id * 7) % 13) - ((b.id * 7) % 13))]) {
        expect([...start].sort(compare).map((t) => t.id)).toEqual(want);
      }
    });
  }
});
