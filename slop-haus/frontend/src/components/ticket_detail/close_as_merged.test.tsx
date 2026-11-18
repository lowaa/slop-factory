import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { apiClient, ApiError, type CloseAsMergedResult, type MergeFacts } from "@/api";
import { makeTicket } from "@/test/fixtures";
import { TicketDetail, stubTicketDetailDefaults } from "./ticket_detail_test_support";

const PR = "https://example.test/org/repo/pull/12";

function facts(overrides: Partial<MergeFacts> = {}): MergeFacts {
  return {
    pr_url: PR,
    base: "main",
    merged: true,
    merged_into_base: true,
    merged_by: "merge-bot",
    merge_sha: "c".repeat(40),
    merged_head: "a".repeat(40),
    approved_head: "a".repeat(40),
    head_approved: true,
    checks: "passed",
    merge_on_base: true,
    confirmations: [],
    ignored: {
      merger_login: "merge-bot",
      reason: "the merger, GitHub user merge-bot (id 77), is not a member here with that GitHub account linked",
      at: "2026-10-27T10:00:00Z",
    },
    ...overrides,
  };
}

function stubTicket() {
  vi.spyOn(apiClient, "getTicket").mockResolvedValue(makeTicket({ id: 5, status: "review", pr_url: PR }));
  vi.spyOn(apiClient, "listComments").mockResolvedValue([]);
  vi.spyOn(apiClient, "getBlockerCandidates").mockResolvedValue([]);
}

describe("CloseAsMergedPanel", () => {
  stubTicketDetailDefaults();

  it("says why the merge did not close the ticket, with what the host shows", async () => {
    stubTicket();
    vi.spyOn(apiClient, "getTicketMergeFacts").mockResolvedValue(facts());

    render(<TicketDetail ticketId={5} />);

    const notice = await screen.findByRole("region", { name: "Merged outside slop-haus" });
    expect(notice).toHaveTextContent("PR #12 was merged by merge-bot, so the ticket wasn't closed.");
    expect(notice).toHaveTextContent("is not a member here");
    expect(notice).toHaveTextContent("is the approved head");
    expect(notice).toHaveTextContent("Checks on that head passed.");
    expect(notice).toHaveTextContent("is on main now");
    expect(screen.getByRole("button", { name: "Close as merged" })).toBeEnabled();
  });

  it("shows nothing to a member the facts are refused to", async () => {
    stubTicket();

    render(<TicketDetail ticketId={5} />);

    await screen.findByRole("heading", { name: /#5/ });
    expect(apiClient.getTicketMergeFacts).toHaveBeenCalledWith(5);
    expect(screen.queryByRole("button", { name: "Close as merged" })).not.toBeInTheDocument();
  });

  it("offers no button for a change not merged into the base", async () => {
    stubTicket();
    vi.spyOn(apiClient, "getTicketMergeFacts").mockResolvedValue(facts({ merged_into_base: false, ignored: undefined }));

    render(<TicketDetail ticketId={5} />);

    await screen.findByRole("heading", { name: /#5/ });
    expect(screen.queryByRole("button", { name: "Close as merged" })).not.toBeInTheDocument();
  });

  it("closes straight away when nothing is in doubt", async () => {
    const user = userEvent.setup();
    stubTicket();
    vi.spyOn(apiClient, "getTicketMergeFacts").mockResolvedValue(facts());
    const closed: CloseAsMergedResult = {
      closed: true,
      needs_confirmation: false,
      confirmations: [],
      ticket: makeTicket({ id: 5, status: "done", pr_url: PR }),
    };
    vi.spyOn(apiClient, "closeTicketAsMerged").mockResolvedValue(closed);

    render(<TicketDetail ticketId={5} />);
    await user.click(await screen.findByRole("button", { name: "Close as merged" }));

    expect(apiClient.closeTicketAsMerged).toHaveBeenCalledWith(5, false);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Close as merged" })).not.toBeInTheDocument());
  });

  it("names what is in doubt and closes only once confirmed", async () => {
    const user = userEvent.setup();
    stubTicket();
    vi.spyOn(apiClient, "getTicketMergeFacts").mockResolvedValue(facts({ checks: "failed", head_approved: false }));
    const asked: CloseAsMergedResult = {
      closed: false,
      needs_confirmation: true,
      confirmations: ["the checks on the merged head aaaa did not pass (failed)", "the merged head aaaa was never approved"],
    };
    const closeSpy = vi.spyOn(apiClient, "closeTicketAsMerged").mockResolvedValueOnce(asked).mockResolvedValueOnce({
      closed: true,
      needs_confirmation: false,
      confirmations: [],
      ticket: makeTicket({ id: 5, status: "done", pr_url: PR }),
    });

    render(<TicketDetail ticketId={5} />);
    await user.click(await screen.findByRole("button", { name: "Close as merged" }));

    const dialog = await screen.findByRole("alertdialog", { name: "Confirm close as merged" });
    expect(dialog).toHaveTextContent("did not pass (failed)");
    expect(dialog).toHaveTextContent("was never approved");
    expect(closeSpy).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Close as merged anyway" }));

    expect(closeSpy).toHaveBeenLastCalledWith(5, true);
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("cancelling the confirmation closes nothing", async () => {
    const user = userEvent.setup();
    stubTicket();
    vi.spyOn(apiClient, "getTicketMergeFacts").mockResolvedValue(facts({ checks: "failed" }));
    const closeSpy = vi
      .spyOn(apiClient, "closeTicketAsMerged")
      .mockResolvedValue({ closed: false, needs_confirmation: true, confirmations: ["checks failed"] });

    render(<TicketDetail ticketId={5} />);
    await user.click(await screen.findByRole("button", { name: "Close as merged" }));
    await user.click(await screen.findByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(closeSpy).toHaveBeenCalledTimes(1);
  });

  it("reports the server's refusal", async () => {
    const user = userEvent.setup();
    stubTicket();
    vi.spyOn(apiClient, "getTicketMergeFacts").mockResolvedValue(facts());
    vi.spyOn(apiClient, "closeTicketAsMerged").mockRejectedValue(new ApiError(409, "ticket: the change is not merged into the ticket's base branch on the code host"));

    render(<TicketDetail ticketId={5} />);
    await user.click(await screen.findByRole("button", { name: "Close as merged" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("not merged into the ticket's base branch");
  });
});
