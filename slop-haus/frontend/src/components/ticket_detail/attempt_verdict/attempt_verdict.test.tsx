import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, type AttemptVerdict } from "@/api";
import { makeAttempt, makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "../ticket_detail_test_support";

const verdict = (overrides: Partial<AttemptVerdict> = {}): AttemptVerdict => ({
  verdict: "would_merge",
  note: "",
  set_at: "2026-01-02T00:00:00Z",
  set_by_user_id: null,
  history: [],
  ...overrides,
});

describe("an attempt's verdict", () => {
  stubTicketDetailDefaults();

  async function open(attempts: ReturnType<typeof makeAttempt>[]) {
    // The tabs exist for a ticket with several attempts: the sibling is the other tab.
    const sibling = makeAttempt({ number: 2, status: "queued", promoted: false, shadow: true });
    vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "review", attempts: [...attempts, sibling] }));
    vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
    vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
    render(<TicketDetail ticketId={5} initialAttempt={attempts[0].number} />);
    await screen.findByRole("heading", { name: "Attempt " + attempts[0].number });
  }

  it.each(["queued", "in_progress", "discussion", "in_merge_queue"] as const)(
    "is not offered on an attempt that is %s",
    async (status) => {
      await open([makeAttempt({ number: 1, status })]);
      expect(screen.queryByRole("region", { name: "Your verdict" })).toBeNull();
    },
  );

  it.each(["review", "done", "closed"] as const)("is offered on an attempt that is %s", async (status) => {
    await open([makeAttempt({ number: 1, status })]);
    const region = screen.getByRole("region", { name: "Your verdict" });
    expect(within(region).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Would merge",
      "Would merge with changes",
      "Would not merge",
    ]);
    expect(within(region).getByText("No verdict yet.")).toBeInTheDocument();
  });

  it("sends the click with the note and shows the verdict it gets back", async () => {
    const set = vi
      .spyOn(apiClient, "setAttemptVerdict")
      .mockResolvedValue(verdict({ verdict: "would_merge_with_changes", note: "rename it" }));
    await open([makeAttempt({ number: 1, status: "review" })]);
    const region = screen.getByRole("region", { name: "Your verdict" });

    await userEvent.type(within(region).getByLabelText(/Note/), "  rename it ");
    await userEvent.click(within(region).getByRole("button", { name: "Would merge with changes" }));

    expect(set).toHaveBeenCalledWith(5, 1, "would_merge_with_changes", "rename it");
    expect(await within(region).findByText("rename it")).toBeInTheDocument();
    expect(within(region).queryByText("No verdict yet.")).toBeNull();
    expect(within(region).getByLabelText(/Note/)).toHaveValue("");
  });

  it("shows the current verdict, who and when, and the earlier ones", async () => {
    await open([
      makeAttempt({
        number: 1,
        status: "done",
        verdict: verdict({
          verdict: "would_not_merge",
          history: [verdict({ verdict: "would_merge", note: "first impression", set_at: "2026-01-01T00:00:00Z" })],
        }),
      }),
    ]);
    const region = screen.getByRole("region", { name: "Your verdict" });

    expect(within(region).getByText("Would not merge", { selector: "strong" })).toBeInTheDocument();
    expect(within(region).getByText("Earlier verdicts (1)")).toBeInTheDocument();
    expect(within(region).getByText("first impression")).toBeInTheDocument();
  });

  it("reports a refusal and keeps the note", async () => {
    const { ApiError } = await import("@/api");
    vi.spyOn(apiClient, "setAttemptVerdict").mockRejectedValue(new ApiError(409, "the attempt has not finished"));
    await open([makeAttempt({ number: 1, status: "review" })]);
    const region = screen.getByRole("region", { name: "Your verdict" });

    await userEvent.type(within(region).getByLabelText(/Note/), "hmm");
    await userEvent.click(within(region).getByRole("button", { name: "Would not merge" }));

    await waitFor(() => expect(within(region).getByRole("alert")).toHaveTextContent("the attempt has not finished"));
    expect(within(region).getByLabelText(/Note/)).toHaveValue("hmm");
  });
});
