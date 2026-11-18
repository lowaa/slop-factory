import { describe, expect, it } from "vitest";
import type { Ticket } from "@/api";
import { TEST_FILTER_MODEL } from "./test_filter_model";
import { TicketQueryModel } from "../ticket_query/ticket_query";
import { matchTicket } from "./ticket_matcher";

const MODEL = new TicketQueryModel(TEST_FILTER_MODEL);
const base = { id: 1, status: "done", priority: "normal", closed_at: "2026-03-10T12:00:00Z", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" } as Ticket;

function verdict(t: Partial<Ticket>, filter: Record<string, unknown> = {}) {
  const query = MODEL.initial({ ...TEST_FILTER_MODEL.defaults(), ...filter } as never);
  return matchTicket({ ...base, ...t }, MODEL.toApi(query));
}

describe("matchTicket", () => {
  it("matches a ticket in an offered status and not one outside them", () => {
    expect(verdict({})).toBe("match");
    expect(verdict({ status: "cancelled" })).toBe("no_match");
  });

  it("filters on priority", () => {
    expect(verdict({ priority: "low" }, { priority: "urgent" })).toBe("no_match");
    expect(verdict({ priority: "urgent" }, { priority: "urgent" })).toBe("match");
  });

  it("filters on a date range, leaving the boundary days to the server", () => {
    const range = { closed: { from: "2026-03-10", to: "2026-03-20" } };
    expect(verdict({ closed_at: "2026-03-15T00:00:00Z" }, range)).toBe("match");
    expect(verdict({ closed_at: "2026-03-09T23:00:00Z" }, range)).toBe("no_match");
    expect(verdict({ closed_at: "2026-03-21T00:00:00Z" }, range)).toBe("no_match");
    expect(verdict({ closed_at: "2026-03-10T09:00:00Z" }, range)).toBe("needs_server");
    expect(verdict({ closed_at: null }, range)).toBe("no_match");
  });

  it("leaves text search to the server", () => {
    expect(verdict({}, { text: "zebra" })).toBe("needs_server");
    expect(verdict({ status: "cancelled" }, { text: "zebra" })).toBe("no_match");
  });
});
