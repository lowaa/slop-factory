import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeAttempt as attempt, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

describe("RunSection", () => {
  stubTicketDetailDefaults();

  function renderWithTicket(ticket: ReturnType<typeof makeTicket>) {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    render(<TicketDetail ticketId={ticket.id} />);
  }

  it("shows nothing before the attempt's first lease", async () => {
    renderWithTicket(makeTicket({ id: 5, attempts: [attempt()] }));

    expect(await screen.findByRole("heading", { level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Ran with" })).not.toBeInTheDocument();
  });

  it("shows what each lease ran with, and says so when a field was not recorded", async () => {
    renderWithTicket(
      makeTicket({
        id: 5,
        attempts: [
          attempt({
            runs: [
              {
                acquired_at: "2026-01-01T00:00:00Z",
                harness: "claude",
                harness_version: "2.1.0",
                model: "opus",
                bundle_hash: "abc123",
                fleet_instructions: { name: "house", version: 3 },
              },
              {
                acquired_at: "2026-01-02T00:00:00Z",
                harness: null,
                harness_version: null,
                model: null,
                bundle_hash: null,
                fleet_instructions: null,
              },
            ],
          }),
        ],
      }),
    );

    expect(await screen.findByRole("heading", { name: "Ran with" })).toBeInTheDocument();
    const [first, second] = screen.getAllByRole("group", { name: /^Lease at/ });
    expect(within(first).getByText("claude 2.1.0")).toBeInTheDocument();
    expect(within(first).getByText("opus")).toBeInTheDocument();
    expect(within(first).getByText("abc123")).toBeInTheDocument();
    expect(within(first).getByText("house v3")).toBeInTheDocument();
    expect(within(second).getAllByText("not recorded")).toHaveLength(3);
    expect(within(second).getByText("none served")).toBeInTheDocument();
  });
});
