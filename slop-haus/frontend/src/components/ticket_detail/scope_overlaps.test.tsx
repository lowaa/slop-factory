import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

describe("ScopeOverlaps", () => {
  stubTicketDetailDefaults();

  function show(overrides: Parameters<typeof makeTicket>[0]) {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 12, ...overrides }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    render(<TicketDetail ticketId={12} />);
  }

  it("lists each live ticket the scopes overlap, with the paths that do", async () => {
    show({
      scope_overlaps: [
        { ticket_id: 297, title: "GitHub App credential", status: "in_progress", path: "app/src/", other: "app/src/panel/" },
        { ticket_id: 308, title: "Approvals panel", status: "open", path: "app/src/", other: "app/src/x.go" },
      ],
    });

    const region = await screen.findByRole("region", { name: "Scope overlaps" });
    expect(region.querySelectorAll("li")).toHaveLength(2);
    expect(region).toHaveTextContent("#297 GitHub App credential (in_progress): app/src/ overlaps app/src/panel/");
    expect(region).toHaveTextContent("#308 Approvals panel (open): app/src/ overlaps app/src/x.go");
  });

  it("marks an overlap inside a shared path as blocking nothing, with how a clash is resolved", async () => {
    show({
      scope_overlaps: [
        { ticket_id: 5, title: "Migrate", status: "in_progress", path: "db/", other: "db/migrations/", resolve: "migration" },
        { ticket_id: 6, title: "Real", status: "open", path: "db/", other: "db/x.go" },
        { ticket_id: 7, title: "Codegen", status: "open", path: "gen/", other: "gen/a.ts", resolve: "regenerate" },
      ],
    });

    const region = await screen.findByRole("region", { name: "Scope overlaps" });
    const [shared, real, generated] = region.querySelectorAll("li");
    expect(generated).toHaveTextContent("on a clash: regenerate, re-run the generator");
    expect(shared).toHaveTextContent("shared path, blocks nothing; on a clash: migration, renumber to follow what landed");
    expect(real).not.toHaveTextContent("shared path");
  });

  it("shows nothing when there are no overlaps", async () => {
    show({ scope_overlaps: [] });
    await screen.findByRole("heading", { name: /#12\b/ });
    expect(screen.queryByRole("region", { name: "Scope overlaps" })).toBeNull();
  });

  it("says a real conflict blocks nothing when the tenant has scope locking off", async () => {
    show({
      scope_overlaps: [{ ticket_id: 6, title: "Real", status: "open", path: "db/", other: "db/x.go", locking_off: true }],
    });

    const region = await screen.findByRole("region", { name: "Scope overlaps" });
    expect(region).toHaveTextContent("Scope locking is off for this tenant");
    expect(region.querySelector("li")).toHaveTextContent("scope locking is off, blocks nothing");
  });
});
