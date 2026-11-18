import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError } from "@/api";
import { chooseFromDropdown, selectedValueOf } from "@/test/widgets";
import { makeAttempt as attempt, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

const VARIANT_SELECT = /Only agents on this variant version/;
const FAST_V1 = { id: "v1", name: "fast", version: 1, harness: "pi" as const };
const FAST_V2 = { id: "v2", name: "fast", version: 2, harness: "pi" as const };

describe("VariantSection", () => {
  stubTicketDetailDefaults();

  function renderWithTicket(ticket: ReturnType<typeof makeTicket>) {
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(ticket);
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    render(<TicketDetail ticketId={ticket.id} />);
  }

  it("shows nothing when no variant exists and none is set", async () => {
    renderWithTicket(makeTicket({ id: 5, attempts: [attempt()] }));

    expect(await screen.findByRole("heading", { level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Variant" })).not.toBeInTheDocument();
  });

  it("routes the ticket to a chosen version and shows the server's answer", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([FAST_V2]);
    renderWithTicket(makeTicket({ id: 5, attempts: [attempt()] }));
    const routed = attempt({ variant: { version_id: "v2", name: "fast", version: 2, archived: false } });
    const set = vi.spyOn(apiClient, "setTicketVariant").mockResolvedValue(makeTicket({ id: 5, attempts: [routed] }));

    await chooseFromDropdown(user, VARIANT_SELECT, "fast v2");

    expect(set).toHaveBeenCalledWith(5, "v2");
    await waitFor(() => expect(selectedValueOf(VARIANT_SELECT)).toContain("fast v2"));
  });

  it("clears the variant when none is chosen", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([FAST_V2]);
    renderWithTicket(
      makeTicket({
        id: 5,
        attempts: [attempt({ variant: { version_id: "v2", name: "fast", version: 2, archived: false } })],
      }),
    );
    const set = vi.spyOn(apiClient, "setTicketVariant").mockResolvedValue(makeTicket({ id: 5, attempts: [attempt()] }));

    await chooseFromDropdown(user, VARIANT_SELECT, "None: only agents in no variant claim it");

    expect(set).toHaveBeenCalledWith(5, null);
  });

  it("reports a refusal beside the picker and keeps the ticket on show", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([FAST_V2]);
    renderWithTicket(makeTicket({ id: 5, attempts: [attempt()] }));
    vi.spyOn(apiClient, "setTicketVariant").mockRejectedValue(
      new ApiError(409, "the attempt has had a lease, so its variant can no longer change"),
    );

    await chooseFromDropdown(user, VARIANT_SELECT, "fast v2");

    expect(await screen.findByRole("alert")).toHaveTextContent("the attempt has had a lease");
    await screen.findByRole("combobox", { name: VARIANT_SELECT });
    expect(selectedValueOf(VARIANT_SELECT)).toContain("None");
  });

  it("is read-only text once the attempt has had a lease", async () => {
    vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([FAST_V2]);
    renderWithTicket(
      makeTicket({
        id: 5,
        attempts: [
          attempt({
            variant_settable: false,
            variant: { version_id: "v2", name: "fast", version: 2, archived: false },
          }),
        ],
      }),
    );

    expect(await screen.findByText(/Only agents on variant fast v2 claim this/)).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: VARIANT_SELECT })).not.toBeInTheDocument();
  });

  it("says why when no enabled agent is on the variant", async () => {
    renderWithTicket(
      makeTicket({
        id: 5,
        no_eligible_agent: true,
        attempts: [attempt({ variant: { version_id: "v2", name: "fast", version: 2, archived: false } })],
      }),
    );

    expect(await screen.findByRole("status")).toHaveTextContent("No enabled agent is on variant fast v2");
  });

  it("keeps an archived version the attempt already names selectable as itself", async () => {
    vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([]);
    renderWithTicket(
      makeTicket({
        id: 5,
        attempts: [attempt({ variant: { version_id: "old", name: "fast", version: 1, archived: true } })],
      }),
    );

    await screen.findByRole("combobox", { name: VARIANT_SELECT });
    expect(selectedValueOf(VARIANT_SELECT)).toContain("fast v1");
  });

  it("says the variants could not be loaded rather than offering an empty picker silently", async () => {
    vi.spyOn(apiClient, "listVariantOptions").mockRejectedValue(new Error("network down"));
    renderWithTicket(makeTicket({ id: 5, attempts: [attempt()] }));

    expect(await screen.findByText(/Couldn't load the variants/)).toBeInTheDocument();
  });

  it("shows what the server answered, not what was asked for", async () => {
    const user = userEvent.setup();
    vi.spyOn(apiClient, "listVariantOptions").mockResolvedValue([FAST_V1, FAST_V2]);
    renderWithTicket(makeTicket({ id: 5, attempts: [attempt()] }));
    vi.spyOn(apiClient, "setTicketVariant").mockResolvedValue(
      makeTicket({
        id: 5,
        attempts: [attempt({ variant: { version_id: "v1", name: "fast", version: 1, archived: false } })],
      }),
    );

    await chooseFromDropdown(user, VARIANT_SELECT, "fast v2");

    await waitFor(() => expect(selectedValueOf(VARIANT_SELECT)).toContain("fast v1"));
  });
});
